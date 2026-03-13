# ---- builder ----
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# modernc.org/sqlite is pure Go — no CGO needed.
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/containershipd .

# ---- runtime ----
# docker:cli gives us the Docker CLI + compose plugin on Alpine.
FROM docker:27-cli
RUN apk add --no-cache git ca-certificates tzdata

COPY --from=builder /app/bin/containershipd /usr/local/bin/containershipd

VOLUME ["/var/lib/containershipd"]
EXPOSE 8080

ENTRYPOINT ["containershipd"]
