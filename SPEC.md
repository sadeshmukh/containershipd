# containershipd — Specification

## Overview

`containershipd` is a Go daemon that manages Docker Compose deployments on behalf of a frontend platform. It is the sole interface to the host's Docker daemon. The frontend site handles user auth, GitHub OAuth, and billing; containershipd handles everything container-related.

## Responsibilities

### containershipd owns
- User records and quota enforcement
- Docker Compose lifecycle (clone, build, up, stop, restart, redeploy, teardown)
- GitHub repo cloning and pulling (tokens passed by caller, stored encrypted at rest)
- Resource limit injection via generated `docker-compose.override.yml`
- Metrics collection via `docker stats` (polled every 30s, 24h retention)
- Log streaming over WebSocket
- GitHub push webhook receiver for auto-redeploy

### Frontend site owns
- User authentication (sessions, OAuth)
- GitHub OAuth (acquiring and storing user GitHub tokens)
- Billing and plan management
- Repo/branch browser UI
- Calling containershipd admin API as a backend-to-backend service
- Issuing short-lived user tokens (via admin API) for browser WebSocket access

---

## Authentication

### Admin API
All `/admin/*` routes require the shared admin secret. Two accepted forms, checked in order:

```
X-Admin-Key: <ADMIN_SECRET>          ← preferred (proxy-safe)
Authorization: Bearer <ADMIN_SECRET> ← fallback
```

`X-Admin-Key` is preferred because HTTP proxies have special handling for `Authorization` headers and may drop or intercept them.

### User-scoped tokens (WebSocket)
- Admin API issues short-lived JWTs scoped to a `userId`
- Site passes these to the browser for direct WebSocket connections
- `POST /admin/users/:id/token` → `{ token, expiresAt }`
- JWT signed with `JWT_SECRET` env var; claims: `sub` = userId, `exp` = 1h

---

## Data Models

### User
```json
{
  "id": "uuid",
  "externalId": "site-user-id (opaque)",
  "status": "active | suspended",
  "quota": {
    "maxDeployments": 3,
    "maxCpuCores": 2.0,
    "maxMemoryMb": 2048,
    "maxStorageGb": 10.0,
    "maxBandwidthGbMonth": 100.0
  },
  "createdAt": "RFC3339",
  "updatedAt": "RFC3339"
}
```

### Deployment
```json
{
  "id": "uuid",
  "userId": "uuid",
  "name": "my-app",
  "status": "provisioning | running | stopped | error | deleted",
  "errorMessage": "only present on error status",
  "github": {
    "repoUrl": "https://github.com/owner/repo",
    "branch": "main",
    "composeFile": "docker-compose.yml",
    "commitSha": "abc123",
    "webhookId": 12345,
    "autoRedeploy": true
  },
  "resourceLimits": {
    "cpuLimit": 0.5,
    "memoryLimitMb": 512,
    "storageLimitGb": 2.0
  },
  "env": { "KEY": "VALUE" },
  "proxy": {
    "subdomain": "my-app",
    "service": "web",
    "port": 3000
  },
  "createdAt": "RFC3339",
  "updatedAt": "RFC3339",
  "lastDeployedAt": "RFC3339 | omitted if never deployed"
}
```

`proxy` is omitted when no subdomain is configured. When present, Traefik routes `subdomain.BASE_DOMAIN` → nginx sidecar → `service:port` within the Compose network.

Note: `env` values are stored AES-256-GCM encrypted and decrypted on read. The GitHub token is stored encrypted and never returned in any API response.

**Port security**: containershipd strips all `ports` declarations from user Compose files before running them. No container ever publishes ports directly to the host. All external traffic must flow through Traefik on ports 80/443.

### Metrics snapshot
```json
{
  "deploymentId": "uuid",
  "timestamp": "RFC3339",
  "services": [
    {
      "name": "web",
      "cpuPercent": 2.3,
      "memoryMb": 128.4,
      "networkRxMb": 0.5,
      "networkTxMb": 0.1
    }
  ]
}
```

---

## Admin API Routes

### Users

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/admin/users` | Create user |
| `GET` | `/admin/users` | List users (`?status=`, `?limit=`, `?offset=`) |
| `GET` | `/admin/users/:id` | Get user |
| `PATCH` | `/admin/users/:id` | Update quota / status / externalId |
| `DELETE` | `/admin/users/:id` | Delete user record (see notes) |
| `POST` | `/admin/users/:id/token` | Issue short-lived user JWT |
| `GET` | `/admin/users/:id/usage` | Current resource usage vs quota |

#### POST /admin/users
```json
// Request — quota fields are optional; defaults applied for any zero values
{
  "externalId": "site-123",
  "quota": {
    "maxDeployments": 3,
    "maxCpuCores": 2.0,
    "maxMemoryMb": 2048,
    "maxStorageGb": 10,
    "maxBandwidthGbMonth": 100
  }
}

// Response 201 — full user object
{ "id": "uuid", "externalId": "site-123", "status": "active", "quota": { ... }, "createdAt": "..." }
```

#### PATCH /admin/users/:id
```json
// All fields optional
{ "externalId": "new-id", "status": "suspended", "quota": { ... } }
```

#### DELETE /admin/users/:id
- Rejects with `409 CONFLICT` if the user has any deployments, unless `?force=true` is passed.
- With `?force=true`: deletes the user record; deployments are cascade-deleted from the DB but **running containers are not stopped** — the caller must delete individual deployments first to ensure clean teardown.

#### GET /admin/users/:id/usage
```json
{
  "userId": "uuid",
  "quota": { "maxDeployments": 3, "maxCpuCores": 2.0, "maxMemoryMb": 2048, "maxStorageGb": 10, "maxBandwidthGbMonth": 100 },
  "usage": { "deployments": 2, "cpuCores": 0.8, "memoryMb": 768, "storageGb": 1.2, "bandwidthGbThisMonth": 0 },
  "available": { "maxDeployments": 1, "maxCpuCores": 1.2, "maxMemoryMb": 1280, "maxStorageGb": 8.8, "maxBandwidthGbMonth": 100 }
}
```

Note: `bandwidthGbThisMonth` is tracked in the usage struct but currently always 0 (not yet collected from metrics).

---

### Deployments

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/admin/deployments` | Create and begin provisioning |
| `GET` | `/admin/deployments` | List (`?userId=`, `?status=`, `?limit=`, `?offset=`) |
| `GET` | `/admin/deployments/:id` | Get deployment |
| `PATCH` | `/admin/deployments/:id` | Update env / limits / branch / autoRedeploy |
| `DELETE` | `/admin/deployments/:id` | Tear down containers + delete record |
| `POST` | `/admin/deployments/:id/start` | Start a stopped deployment |
| `POST` | `/admin/deployments/:id/stop` | Stop all services (data preserved) |
| `POST` | `/admin/deployments/:id/restart` | Restart all services |
| `POST` | `/admin/deployments/:id/redeploy` | Pull latest commit + rebuild + up |
| `GET` | `/admin/deployments/:id/metrics` | Latest metrics snapshot |
| `GET` | `/admin/deployments/:id/metrics/history` | Time-series (`?from=RFC3339`, `?to=RFC3339`) |
| `GET` | `/admin/deployments/:id/logs` | Stream last 200 log lines (`?service=name`) |

#### POST /admin/deployments
```json
// Request
{
  "userId": "uuid",
  "name": "my-app",
  "github": {
    "repoUrl": "https://github.com/owner/repo",
    "branch": "main",                    // default: "main"
    "composeFile": "docker-compose.yml", // default: "docker-compose.yml"
    "githubToken": "ghp_...",            // stored encrypted; never returned
    "autoRedeploy": true
  },
  "resourceLimits": {
    "cpuLimit": 0.5,       // default: 0.5
    "memoryLimitMb": 512,  // default: 512
    "storageLimitGb": 2.0  // default: 2.0
  },
  "env": { "DATABASE_URL": "postgres://..." },
  "proxy": {                             // omit for no public subdomain
    "subdomain": "my-app",              // required; must be unique across all deployments
    "service": "web",                   // default: "web"
    "port": 3000                        // default: 80
  }
}

// Response 202 — full deployment object at time of creation; status = "provisioning"
// Provisioning (clone → build → up) runs asynchronously. Poll GET /admin/deployments/:id
// to observe status transitions.
{ "id": "uuid", "status": "provisioning", "userId": "...", ... }
```

#### PATCH /admin/deployments/:id
```json
// All fields optional
{
  "name": "new-name",
  "github": {
    "branch": "production",
    "composeFile": "docker/compose.yml",
    "autoRedeploy": false,
    "githubToken": "ghp_new..."
  },
  "resourceLimits": { "cpuLimit": 1.0, "memoryLimitMb": 1024, "storageLimitGb": 4 },
  "env": { "KEY": "value" },
  "proxy": {
    "subdomain": "new-name",            // all fields optional; merged with current proxy config
    "service": "api",
    "port": 8080
  },
  "clearProxy": true                    // set true to remove proxy config entirely
}
```
If the deployment is `running` and `resourceLimits`, `env`, `proxy`, or `clearProxy` are changed, a live reconfigure (`docker compose up -d` with new override) is triggered asynchronously.

**Subdomain rules**: lowercase alphanumeric and hyphens only; no leading/trailing hyphens; 1–63 characters. Rejected with `409 CONFLICT` if already in use by another deployment. Rejected with `400 INVALID_REQUEST` if `BASE_DOMAIN` is not configured.

#### Deployment status flow
```
provisioning ──→ running
             └─→ error       (clone/build/up failed; errorMessage set)

running      ──→ stopping*   (internal; reflected as stopped once done)
             └─→ provisioning (via /restart or /redeploy)

stopped      ──→ provisioning (via /start; transitions to running on success)

any          ──→ (deleted)   (via DELETE; no status set — record is removed)
```
\* Stop is async: the API returns 202 immediately and the status updates to `stopped` once `docker compose stop` completes.

#### Async operation timeouts
All lifecycle operations run in background goroutines with hard timeouts:

| Operation | Timeout |
|-----------|---------|
| provision, redeploy | 15 minutes |
| start, reconfigure | 5 minutes |
| stop, restart, teardown | 2 minutes |

If a timeout is exceeded the subprocess is killed and `status` is set to `error` with a descriptive `errorMessage`.

---

### Webhooks

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/webhooks/github/:deploymentId` | GitHub push event receiver |

- Registered on GitHub automatically when `autoRedeploy: true` and `WEBHOOK_BASE_URL` is set
- Validated via `X-Hub-Signature-256` HMAC (per-deployment secret)
- Triggers a redeploy only if the pushed branch matches the deployment's configured branch
- No-ops silently if the deployment is not in `running` or `stopped` state

---

### WebSocket (user-token auth)

```
WS /ws/deployments/:id/logs?token=<jwt>
```
Streams `docker compose logs --follow --timestamps --tail=200` output as text frames. Optional `?service=name` to filter to one service. Keepalive ping sent every 30 seconds.

```
WS /ws/deployments/:id/events?token=<jwt>
```
Polls deployment status every 5 seconds and emits a JSON frame on each change:
```json
{ "type": "status_change", "status": "running" }
```

Both endpoints require a user-scoped JWT in `?token=` (issued by `POST /admin/users/:id/token`). The JWT subject must match the deployment's `userId`.

---

## Resource Enforcement

### At deploy time
containershipd generates a `docker-compose.override.yml` alongside the cloned repo that injects resource limits into every service:
```yaml
services:
  web:
    deploy:
      resources:
        limits:
          cpus: "0.25"
          memory: "256m"
  worker:
    deploy:
      resources:
        limits:
          cpus: "0.25"
          memory: "256m"
```
CPU and memory are divided evenly across all services. Minimums: 0.1 CPU and 64 MB per service.

### Quota enforcement
Before `POST /admin/deployments` or `POST /admin/deployments/:id/start`, containershipd sums `resource_cpu_limit`, `resource_memory_limit_mb`, and `resource_storage_limit_gb` across all active (non-deleted, non-error) deployments for the user and rejects with `429 QUOTA_EXCEEDED` if the new deployment would exceed any limit.

### Metrics collection
- `docker stats --no-stream` polled every 30 seconds for all `running` deployments
- Each collection run has a 15-second per-deployment timeout to prevent a stuck container from blocking the loop
- Metrics older than 24 hours are purged hourly

---

## Error Response Format

```json
{ "error": "ERROR_CODE", "message": "Human-readable description." }
```

### Error codes

| Code | HTTP status | Meaning |
|------|-------------|---------|
| `UNAUTHORIZED` | 401 | Missing or invalid admin key |
| `NOT_FOUND` | 404 | User or deployment does not exist |
| `INVALID_REQUEST` | 400 | Malformed JSON or missing required field |
| `CONFLICT` | 409 | Operation not valid for current state (e.g. stopping a stopped deployment, or deleting a user who has deployments) |
| `QUOTA_EXCEEDED` | 429 | Requested deployment would exceed user's resource quota |
| `USER_SUSPENDED` | 403 | User account is suspended |
| `INTERNAL_ERROR` | 500 | Unexpected server error |

---

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `ADMIN_SECRET` | yes | — | Shared secret for admin API (`X-Admin-Key` or `Authorization: Bearer`) |
| `JWT_SECRET` | yes | — | Secret for signing user-scoped JWTs |
| `ENCRYPTION_KEY` | yes | — | 32+ character key for AES-256-GCM encryption of GitHub tokens and env vars |
| `LISTEN_ADDR` | no | `:8080` | HTTP listen address |
| `DATABASE_PATH` | no | `/var/lib/containershipd/containershipd.db` | SQLite file path |
| `DATA_DIR` | no | `/var/lib/containershipd` | Root dir for deployment workdirs and SQLite DB |
| `WEBHOOK_BASE_URL` | no | — | Public base URL for GitHub webhook registration (e.g. `https://myapp.com`). Required for `autoRedeploy` to work. |
| `BASE_DOMAIN` | no | — | Base domain for deployment subdomains (e.g. `app.example.com`). Required for `proxy` to work. |
| `ACME_EMAIL` | no | — | Email for Let's Encrypt registration. Required when `BASE_DOMAIN` is set. |

---

## Traefik Proxy

### Overview
When `BASE_DOMAIN` is configured, containershipd starts and manages a Traefik v3.2 container that handles all public HTTPS routing. No user container ever exposes ports to the host; Traefik is the sole listener on ports 80 and 443.

### Architecture
```
Internet
  │
  ├─ :80  ──→ Traefik (HTTP → HTTPS redirect + ACME challenge)
  └─ :443 ──→ Traefik
                │
                └─ csd-traefik Docker network
                     │
                     └─ csd-sidecar (nginx:alpine, injected per-deployment)
                          │
                          └─ default Compose network
                               │
                               └─ user's service (e.g. web:3000)
```

### Traefik management
- On startup, containershipd writes `$DATA_DIR/traefik/docker-compose.yml` and runs `docker compose -p csd-traefik up -d`.
- TLS certificates are obtained via Let's Encrypt HTTP-01 challenge; stored in `$DATA_DIR/traefik/letsencrypt/acme.json`.
- The `csd-traefik` Docker network is created by the Traefik compose stack and referenced as an external network by deployment override files.

### Per-deployment sidecar
When a deployment has a `proxy` config, the generated `docker-compose.override.yml` injects a `csd-sidecar` service:
- Image: `nginx:alpine`
- nginx.conf written to `$DATA_DIR/deployments/{id}/nginx.conf`; proxies to `service:port` within the Compose network
- Joined to both the default Compose network (to reach user services) and `csd-traefik` (for Traefik to reach it)
- Traefik Docker labels configure routing: `Host(subdomain.BASE_DOMAIN)` → HTTPS → `csd-sidecar:80`
- Fixed resource cap: 0.10 CPU / 64 MB (not counted against user quota)

When `proxy` is absent, no sidecar is injected and no Traefik labels are set.

### Port stripping
containershipd sanitizes all user Compose files before running them, removing every `ports` mapping from every service. The sanitized copy is stored at `$DATA_DIR/deployments/{id}/docker-compose.sanitized.yml` and used for all Docker Compose operations. The original file in the repo is never modified.

---

## Data Directory Layout

```
$DATA_DIR/
├── traefik/
│   ├── docker-compose.yml               ← Traefik compose (managed by containershipd)
│   └── letsencrypt/
│       └── acme.json                    ← Let's Encrypt certificate store (mode 0600)
└── deployments/
    └── {deployment-id}/
        ├── repo/                        ← git clone target
        │   ├── docker-compose.yml
        │   └── ...
        ├── docker-compose.sanitized.yml ← port-stripped copy of user compose (never commit)
        ├── docker-compose.override.yml  ← resource limits + optional nginx sidecar (never commit)
        ├── nginx.conf                   ← generated nginx reverse-proxy config (when proxy set)
        └── .env                         ← decrypted env vars written at deploy time (never commit)
```

Docker Compose project name: `csd` + first 12 hex chars of the deployment UUID (hyphens stripped). Example: UUID `a1b2c3d4-...` → project `csda1b2c3d4e5f6`.

---

## Tech Stack

| Concern | Choice |
|---------|--------|
| Language | Go 1.22 |
| HTTP router | `github.com/go-chi/chi/v5` |
| Database | SQLite via `modernc.org/sqlite` (pure Go, no CGO) |
| WebSocket | `github.com/gorilla/websocket` |
| JWT | `github.com/golang-jwt/jwt/v5` |
| YAML parsing | `gopkg.in/yaml.v3` |
| Encryption | AES-256-GCM (`crypto/aes` + `crypto/cipher`) |
| Docker | Shell out to `docker compose` CLI + `docker stats` |
| Git | Shell out to `git clone` / `git fetch` / `git reset` |
| GitHub API | Raw HTTP calls (webhook register/delete only) |

### Runtime dependencies (must be present in container / on host)
- `docker` CLI with Compose plugin (`docker compose` subcommand)
- `git`
