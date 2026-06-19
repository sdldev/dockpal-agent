package docker

import (
	"github.com/moby/moby/api/types/container"
)

// ContainerInfo mirrors the Server's ContainerInfo for JSON compatibility.
type ContainerInfo struct {
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	Image         string                  `json:"image"`
	Status        string                  `json:"status"`
	State         string                  `json:"state"`
	Ports         []container.PortSummary `json:"ports"`
	Created       int64                   `json:"created"`
	RestartPolicy string                  `json:"restart_policy,omitempty"`
	Networks      map[string]string       `json:"networks,omitempty"`
	Labels        map[string]string       `json:"labels,omitempty"`
}

// ContainerDetail mirrors the Server's ContainerDetail for JSON compatibility.
type ContainerDetail struct {
	ContainerInfo
	Platform      string                 `json:"platform"`
	Env           []string               `json:"env"`
	Mounts        []container.MountPoint `json:"mounts"`
	NetworkMode   string                 `json:"network_mode"`
	RestartPolicy string                 `json:"restart_policy"`
	Networks      map[string]string      `json:"networks"`
	MemoryLimit   int64                  `json:"memory_limit"`
	NanoCPUs      int64                  `json:"nano_cpus"`
}

// ContainerStats mirrors the Server's ContainerStats for JSON compatibility.
type ContainerStats struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsage   uint64  `json:"memory_usage"`
	MemoryLimit   uint64  `json:"memory_limit"`
	MemoryPercent float64 `json:"memory_percent"`
	NetworkRx     uint64  `json:"network_rx"`
	NetworkTx     uint64  `json:"network_tx"`
}

// ContainerEditRequest represents changes to apply to a container.
type ContainerEditRequest struct {
	Name          *string          `json:"name,omitempty"`
	RestartPolicy *string          `json:"restart_policy,omitempty"`
	MemoryLimit   *int64           `json:"memory_limit,omitempty"`
	CPULimit      *float64         `json:"cpu_limit,omitempty"`
	Image         *string          `json:"image,omitempty"`
	Env           *[]string        `json:"env,omitempty"`
	Ports         *[]PortMapping   `json:"ports,omitempty"`
	Volumes       *[]VolumeMapping `json:"volumes,omitempty"`
}

// PortMapping represents a host:container port mapping.
type PortMapping struct {
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

// VolumeMapping represents a host:container volume mount.
type VolumeMapping struct {
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only"`
}

// ImageInfo mirrors the Server's ImageInfo for JSON compatibility.
type ImageInfo struct {
	ID      string `json:"id"`
	Repo    string `json:"repo"`
	Tag     string `json:"tag"`
	Size    string `json:"size"`
	Created string `json:"created"`
}

// FileInfo represents a file or directory inside a container.
type FileInfo struct {
	Name  string `json:"name"`
	Size  string `json:"size"`
	IsDir bool   `json:"is_dir"`
}

// DeployEvent represents a single log event during deployment.
type DeployEvent struct {
	Step    string `json:"step"`
	Message string `json:"message"`
	Status  string `json:"status"`
	Time    string `json:"time"`
}

// AuthHeaderFunc returns the registry auth header for a given image reference.
// Returns empty string if no credentials match (fallback to unauthenticated pull).
type AuthHeaderFunc func(imageRef string) (string, error)
