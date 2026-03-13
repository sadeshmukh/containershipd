# ---- builder ----
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# modernc.org/sqlite is pure Go — no CGO needed.
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/containershipd .

# ---- runtime ----
# Use debian:bookworm-slim instead of docker:cli (Alpine) to avoid a WSL2
# kernel incompatibility where runc tries to set net.ipv4.ip_unprivileged_port_start
# in the container network namespace and gets permission denied.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates curl \
    && install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/debian/gpg \
         -o /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
         https://download.docker.com/linux/debian bookworm stable" \
         > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        docker-ce-cli docker-compose-plugin \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/bin/containershipd /usr/local/bin/containershipd

VOLUME ["/var/lib/containershipd"]
EXPOSE 8080

ENTRYPOINT ["containershipd"]
