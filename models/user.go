package models

import "time"

type UserStatus string

const (
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended"
)

type Quota struct {
	MaxDeployments      int     `json:"maxDeployments"`
	MaxCPUCores         float64 `json:"maxCpuCores"`
	MaxMemoryMb         int     `json:"maxMemoryMb"`
	MaxStorageGb        float64 `json:"maxStorageGb"`
	MaxBandwidthGbMonth float64 `json:"maxBandwidthGbMonth"`
}

type Usage struct {
	Deployments          int     `json:"deployments"`
	CPUCores             float64 `json:"cpuCores"`
	MemoryMb             int     `json:"memoryMb"`
	StorageGb            float64 `json:"storageGb"`
	BandwidthGbThisMonth float64 `json:"bandwidthGbThisMonth"`
}

type User struct {
	ID         string     `json:"id"`
	ExternalID string     `json:"externalId"`
	Status     UserStatus `json:"status"`
	Quota      Quota      `json:"quota"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}
