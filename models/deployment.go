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

type Port struct {
	Service       string `json:"service"`
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
}

type Deployment struct {
	ID             string            `json:"id"`
	UserID         string            `json:"userId"`
	Name           string            `json:"name"`
	Status         DeploymentStatus  `json:"status"`
	ErrorMessage   string            `json:"errorMessage,omitempty"`
	Github         GithubConfig      `json:"github"`
	ResourceLimits ResourceLimits    `json:"resourceLimits"`
	Env            map[string]string `json:"env,omitempty"`
	Ports          []Port            `json:"ports,omitempty"`
	WebhookSecret  string            `json:"-"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	LastDeployedAt *time.Time        `json:"lastDeployedAt,omitempty"`
}
