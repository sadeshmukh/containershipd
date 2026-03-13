package models

import "time"

type ServiceMetrics struct {
	Name        string  `json:"name"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryMb    float64 `json:"memoryMb"`
	NetworkRxMb float64 `json:"networkRxMb"`
	NetworkTxMb float64 `json:"networkTxMb"`
}

type DeploymentMetrics struct {
	DeploymentID string           `json:"deploymentId"`
	Timestamp    time.Time        `json:"timestamp"`
	Services     []ServiceMetrics `json:"services"`
}
