package enroll

import (
	"encoding/json"
	"net/http"

	"github.com/sdldev/dockpal-agent/internal/config"
	"github.com/sdldev/dockpal-agent/internal/docker"
	"github.com/sdldev/dockpal-agent/internal/host"
)

// EnrollResponse is returned after successful enrollment.
type EnrollResponse struct {
	Status        string `json:"status"`
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	CPUCores      int    `json:"cpu_cores"`
	TotalMemory   uint64 `json:"total_memory"`
	DockerVersion string `json:"docker_version"`
	Version       string `json:"version"`
}

// HandleEnroll returns host info. Token is already validated by auth middleware.
func HandleEnroll(w http.ResponseWriter, r *http.Request, cfg *config.Config, dockerClient *docker.Client) {
	ver, _ := dockerClient.ServerVersion(r.Context())
	info := host.GetHostInfo(ver)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(EnrollResponse{
		Status:        "ok",
		Hostname:      info.Hostname,
		OS:            info.OS,
		CPUCores:      info.CPUCores,
		TotalMemory:   info.TotalMemory,
		DockerVersion: info.DockerVersion,
		Version:       config.Version,
	})
}
