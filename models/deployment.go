package models

import "time"

type DeploymentStatus string

const (
	StatusProvisioning DeploymentStatus = "provisioning"
	StatusRunning      DeploymentStatus = "running"
	StatusStopped      DeploymentStatus = "stopped"
	StatusError        DeploymentStatus = "error"
	StatusDeleted      DeploymentStatus = "deleted"
)

type GithubConfig struct {
	RepoURL      string `json:"repoUrl"`
	Branch       string `json:"branch"`
	ComposeFile  string `json:"composeFile"`
	CommitSha    string `json:"commitSha,omitempty"`
	WebhookID    int64  `json:"webhookId,omitempty"`
	AutoRedeploy bool   `json:"autoRedeploy"`
}

type ResourceLimits struct {
	CPULimit       float64 `json:"cpuLimit"`
	MemoryLimitMb  int     `json:"memoryLimitMb"`
	StorageLimitGb float64 `json:"storageLimitGb"`
}

// ProxyConfig configures the Traefik reverse proxy for a deployment.
// When non-nil, the deployment gets a public subdomain routed to the
// specified Compose service and container port.
type ProxyConfig struct {
	Subdomain string `json:"subdomain"`      // DNS slug, e.g. "my-app"
	Service   string `json:"service"`        // Compose service name, default "web"
	Port      int    `json:"port"`           // Container port, default 80
}

type Deployment struct {
	ID             string            `json:"id"`
	UserID         string            `json:"userId"`
	Name           string            `json:"name"`
	Status         DeploymentStatus  `json:"status"`
	ErrorMessage   string            `json:"errorMessage,omitempty"`
	Github         GithubConfig      `json:"github"`
	ResourceLimits ResourceLimits    `json:"resourceLimits"`
	Env            map[string]string `json:"env"`
	Proxy          *ProxyConfig      `json:"proxy,omitempty"`
	WebhookSecret  string            `json:"-"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	LastDeployedAt *time.Time        `json:"lastDeployedAt,omitempty"`
}
