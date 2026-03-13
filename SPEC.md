# containershipd — Specification

## Overview

`containershipd` is a Go daemon that manages Docker Compose deployments on behalf of a frontend platform. It is the sole interface to the host's Docker daemon. The frontend site handles user auth, GitHub OAuth, and billing; containershipd handles everything container-related.

## Responsibilities

### containershipd owns
- User records and quota enforcement
- Docker Compose lifecycle (clone, build, up, stop, restart, redeploy, teardown)
- GitHub repo cloning and pulling (tokens passed by caller, stored encrypted)
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
All `/admin/*` routes require:
```
Authorization: Bearer <ADMIN_SECRET>
```
Set via `ADMIN_SECRET` env var. The site uses this for all operations.

### User-scoped tokens (WebSocket)
- Admin API issues short-lived JWTs scoped to a `userId`
- Site passes these to the browser for direct WebSocket connections
- `POST /admin/users/:userId/token` → `{ token, expiresAt }`
- JWT signed with `JWT_SECRET` env var, `sub` = userId, `exp` = 1h

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
  "errorMessage": "...",
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
  "ports": [
    { "service": "web", "hostPort": 12345, "containerPort": 80 }
  ],
  "createdAt": "RFC3339",
  "updatedAt": "RFC3339",
  "lastDeployedAt": "RFC3339"
}
```

### Metrics
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
| `DELETE` | `/admin/users/:id` | Delete user (tears down all deployments) |
| `POST` | `/admin/users/:id/token` | Issue short-lived user JWT |
| `GET` | `/admin/users/:id/usage` | Current resource usage vs quota |

#### POST /admin/users
```json
// Request
{ "externalId": "site-123", "quota": { "maxDeployments": 3, "maxCpuCores": 2.0, "maxMemoryMb": 2048, "maxStorageGb": 10, "maxBandwidthGbMonth": 100 } }

// Response 201
{ "id": "uuid", "externalId": "site-123", "status": "active", "quota": {...}, "createdAt": "..." }
```

#### GET /admin/users/:id/usage
```json
{
  "userId": "uuid",
  "quota": { ... },
  "usage": { "deployments": 2, "cpuCores": 0.8, "memoryMb": 768, "storageGb": 1.2, "bandwidthGbThisMonth": 4.3 },
  "available": { "deployments": 1, "cpuCores": 1.2, "memoryMb": 1280, "storageGb": 8.8 }
}
```

---

### Deployments

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/admin/deployments` | Create and deploy |
| `GET` | `/admin/deployments` | List (`?userId=`, `?status=`, `?limit=`, `?offset=`) |
| `GET` | `/admin/deployments/:id` | Get deployment |
| `PATCH` | `/admin/deployments/:id` | Update env / limits / branch (triggers reconfigure) |
| `DELETE` | `/admin/deployments/:id` | Tear down and delete |
| `POST` | `/admin/deployments/:id/start` | Start stopped deployment |
| `POST` | `/admin/deployments/:id/stop` | Stop (data preserved) |
| `POST` | `/admin/deployments/:id/restart` | Restart all services |
| `POST` | `/admin/deployments/:id/redeploy` | Pull latest + rebuild |
| `GET` | `/admin/deployments/:id/metrics` | Latest metrics snapshot |
| `GET` | `/admin/deployments/:id/metrics/history` | Time-series (`?from=`, `?to=`, `?resolution=5m`) |
| `GET` | `/admin/deployments/:id/logs` | Last N log lines (`?service=`, `?lines=100`) |

#### POST /admin/deployments
```json
// Request
{
  "userId": "uuid",
  "name": "my-app",
  "github": {
    "repoUrl": "https://github.com/owner/repo",
    "branch": "main",
    "composeFile": "docker-compose.yml",
    "githubToken": "ghp_...",
    "autoRedeploy": true
  },
  "resourceLimits": { "cpuLimit": 0.5, "memoryLimitMb": 512, "storageLimitGb": 2 },
  "env": { "DATABASE_URL": "postgres://..." }
}

// Response 202 — provisioning happens asynchronously
{ "id": "uuid", "status": "provisioning" }
```

#### Deployment status flow
```
provisioning → running | error
running      → stopped (via /stop)
stopped      → running (via /start)
any          → deleted (via DELETE)
```

---

### Webhooks

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/webhooks/github/:deploymentId` | GitHub push event receiver |

- Registered automatically when `autoRedeploy: true` and `WEBHOOK_BASE_URL` is set
- Validates `X-Hub-Signature-256` using per-deployment HMAC secret
- Triggers redeploy on matching branch push

---

### WebSocket (user-token auth)

```
WS /ws/deployments/:id/logs?token=<jwt>    — live log stream (text frames = log lines)
WS /ws/deployments/:id/events?token=<jwt>  — container lifecycle events (JSON frames)
```

---

## Resource Enforcement

### At deploy time
containershipd generates a `docker-compose.override.yml` that injects resource limits across all services:
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
CPU and memory are divided evenly across services (minimum 0.1 CPU / 64MB per service).

### Quota enforcement
Before creating or starting a deployment, containershipd sums all active deployments for the user and rejects with `429 QUOTA_EXCEEDED` if the new deployment would exceed the quota.

### Runtime monitoring
- Docker stats polled every 30 seconds
- 24-hour metric history retained
- Events emitted via WebSocket when a service sustains >90% memory limit for >5 minutes

---

## Error Response Format

```json
{
  "error": "QUOTA_EXCEEDED",
  "message": "This deployment would exceed the user's CPU quota.",
  "details": { "requested": 0.5, "used": 1.8, "limit": 2.0 }
}
```

### Error codes
`QUOTA_EXCEEDED` · `DEPLOYMENT_NOT_FOUND` · `USER_NOT_FOUND` · `GITHUB_CLONE_FAILED` · `COMPOSE_INVALID` · `USER_SUSPENDED` · `UNAUTHORIZED` · `INVALID_REQUEST` · `CONFLICT`

---

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `ADMIN_SECRET` | yes | — | Shared secret for admin API auth |
| `JWT_SECRET` | yes | — | Secret for signing user-scoped JWTs |
| `ENCRYPTION_KEY` | yes | — | 32+ char key for encrypting GitHub tokens and env vars |
| `LISTEN_ADDR` | no | `:8080` | HTTP listen address |
| `DATABASE_PATH` | no | `/var/lib/containershipd/containershipd.db` | SQLite file path |
| `DATA_DIR` | no | `/var/lib/containershipd` | Root dir for deployment workdirs |
| `WEBHOOK_BASE_URL` | no | — | Public base URL for GitHub webhook registration (e.g. `https://myapp.com`) |

---

## Data Directory Layout

```
$DATA_DIR/
└── deployments/
    └── {deployment-id}/
        ├── repo/                        ← git clone
        │   ├── docker-compose.yml
        │   └── ...
        ├── docker-compose.override.yml  ← auto-generated resource limits
        └── .env                         ← decrypted env vars for compose
```

Docker Compose project name: `csd` + first 12 hex chars of deployment UUID (hyphens stripped).

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
| Encryption | AES-256-GCM (stdlib `crypto/aes`) |
| Docker | Shell out to `docker compose` CLI + `docker stats` |
| Git | Shell out to `git` |
| GitHub API | Raw HTTP (webhook registration) |
