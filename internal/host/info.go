package host

import (
	"os"
	"runtime"
)

// HostInfo contains static host information.
type HostInfo struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	CPUCores      int    `json:"cpu_cores"`
	TotalMemory   uint64 `json:"total_memory"`
	DockerVersion string `json:"docker_version"`
}

// GetHostInfo collects static host information.
func GetHostInfo(dockerVersion string) *HostInfo {
	hostname, _ := os.Hostname()
	totalRAM := getMemoryTotal()

	return &HostInfo{
		Hostname:      hostname,
		OS:            runtime.GOOS,
		CPUCores:      runtime.NumCPU(),
		TotalMemory:   totalRAM,
		DockerVersion: dockerVersion,
	}
}

// getMemoryTotal returns the total memory available on the host.
func getMemoryTotal() uint64 {
	total, _ := getMemoryInfo()
	return total
}
