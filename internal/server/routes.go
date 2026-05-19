package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/sdldev/dockpal-agent/internal/auth"
	"github.com/sdldev/dockpal-agent/internal/docker"
	"github.com/sdldev/dockpal-agent/internal/enroll"
	"github.com/sdldev/dockpal-agent/internal/host"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) registerRoutes() {
	s.router.Route("/agent", func(r chi.Router) {
		// Unauthenticated health check (for Docker HEALTHCHECK)
		r.Get("/ping", s.handlePing)

		// Authenticated agent routes
		r.Group(func(r chi.Router) {
			r.Use(auth.TokenMiddleware(s.cfg.Token))

			// Enrollment
			r.Post("/enroll", func(w http.ResponseWriter, r *http.Request) {
				enroll.HandleEnroll(w, r, s.cfg, s.docker)
			})

			// Docker proxy
			r.Route("/docker", func(r chi.Router) {
				// Containers
				r.Get("/containers", s.handleListContainers)
				r.Get("/containers/{id}", s.handleInspectContainer)
				r.Post("/containers/{id}/start", s.handleStartContainer)
				r.Post("/containers/{id}/stop", s.handleStopContainer)
				r.Post("/containers/{id}/restart", s.handleRestartContainer)
				r.Delete("/containers/{id}", s.handleRemoveContainer)
				r.Put("/containers/{id}", s.handleEditContainer)
				r.Get("/containers/{id}/stats", s.handleGetContainerStats)
				r.Get("/containers/{id}/logs", s.handleContainerLogs)
				r.Get("/containers/{id}/stats/ws", s.handleStatsStream)

				// Deploy
				r.Post("/deploy/compose", s.handleDeployCompose)
				r.Post("/deploy/stream", s.handleDeployStream)
				r.Post("/compose/stop", s.handleStopCompose)
				r.Post("/compose/remove", s.handleRemoveCompose)

				// Images
				r.Get("/images", s.handleListImages)
				r.Post("/images/pull", s.handlePullImage)
				r.Delete("/images/{id}", s.handleRemoveImage)

				// Files
				r.Get("/files", s.handleListFiles)
				r.Get("/files/read", s.handleReadFile)
				r.Post("/files/write", s.handleWriteFile)
				r.Post("/files/upload", s.handleUploadFile)
				r.Get("/files/download", s.handleDownloadFile)
				r.Delete("/files", s.handleDeleteFile)
				r.Post("/containers/{id}/files/write", s.handleContainerFileWrite)
			})

			// Host
			r.Get("/host/info", s.handleHostInfo)
			r.Get("/host/stats", s.handleHostStats)
		})
	})
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "error": "docker unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// --- Container handlers ---

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	containers, err := s.docker.ListContainers(r.Context(), all)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, containers)
}

func (s *Server) handleInspectContainer(w http.ResponseWriter, r *http.Request) {
	detail, err := s.docker.InspectContainer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleStartContainer(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.StartContainer(r.Context(), chi.URLParam(r, "id")); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "started"})
}

func (s *Server) handleStopContainer(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.StopContainer(r.Context(), chi.URLParam(r, "id")); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "stopped"})
}

func (s *Server) handleRestartContainer(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.RestartContainer(r.Context(), chi.URLParam(r, "id")); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "restarted"})
}

func (s *Server) handleRemoveContainer(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	if err := s.docker.RemoveContainer(r.Context(), chi.URLParam(r, "id"), force); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
}

func (s *Server) handleEditContainer(w http.ResponseWriter, r *http.Request) {
	var req docker.ContainerEditRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	detail, err := s.docker.EditContainer(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "container": detail})
}

func (s *Server) handleGetContainerStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.docker.GetContainerStats(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	reader, err := s.docker.ContainerLogs(r.Context(), chi.URLParam(r, "id"), "100")
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Error: failed to retrieve logs"))
		return
	}
	defer reader.Close()

	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			conn.WriteMessage(websocket.TextMessage, buf[:n])
		}
		if err != nil {
			break
		}
	}
}

func (s *Server) handleStatsStream(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	containerID := chi.URLParam(r, "id")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	detail, err := s.docker.InspectContainer(ctx, containerID)
	if err != nil {
		conn.WriteJSON(map[string]any{"error": "container not found"})
		return
	}
	if detail.State != "running" {
		conn.WriteJSON(map[string]any{"error": "container is not running"})
		return
	}

	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	stats, err := s.docker.GetContainerStats(ctx, containerID)
	if err != nil {
		conn.WriteJSON(map[string]any{"error": "failed to get container stats"})
		return
	}
	if err := conn.WriteJSON(stats); err != nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats, err := s.docker.GetContainerStats(ctx, containerID)
			if err != nil {
				return
			}
			if err := conn.WriteJSON(stats); err != nil {
				return
			}
		}
	}
}

// --- Deploy handlers ---

func (s *Server) handleDeployCompose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string            `json:"name"`
		Compose       string            `json:"compose"`
		RegistryAuths map[string]string `json:"registry_auths,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}

	getAuthHeader := buildAuthHeaderFunc(req.RegistryAuths)
	if err := s.docker.DeployCompose(r.Context(), req.Name, req.Compose, getAuthHeader); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deployed"})
}

func (s *Server) handleDeployStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string            `json:"name"`
		Compose       string            `json:"compose"`
		RegistryAuths map[string]string `json:"registry_auths,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}

	session := s.deployMgr.CreateSession()
	getAuthHeader := buildAuthHeaderFunc(req.RegistryAuths)

	// Use background context — request context is cancelled when handler returns
	go func() {
		_ = s.docker.DeployComposeStreamed(context.Background(), req.Name, req.Compose, session, getAuthHeader)
		go func() {
			time.Sleep(30 * time.Second)
			s.deployMgr.RemoveSession(session.ID)
		}()
	}()

	writeJSON(w, http.StatusOK, map[string]any{"deploy_id": session.ID})
}

func (s *Server) handleStopCompose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	if err := s.docker.StopCompose(r.Context(), req.Name); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "stopped"})
}

func (s *Server) handleRemoveCompose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	if err := s.docker.RemoveCompose(r.Context(), req.Name); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
}

// --- Image handlers ---

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.docker.ListImages(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, images)
}

func (s *Server) handlePullImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image        string `json:"image"`
		RegistryAuth string `json:"registry_auth,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}

	var err error
	if req.RegistryAuth != "" {
		err = s.docker.PullImageWithAuth(r.Context(), req.Image, req.RegistryAuth)
	} else {
		err = s.docker.PullImage(r.Context(), req.Image)
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "pulled"})
}

func (s *Server) handleRemoveImage(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.RemoveImage(r.Context(), chi.URLParam(r, "id")); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "removed"})
}

// --- File handlers ---

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("container")
	path := r.URL.Query().Get("path")
	files, err := s.docker.ListFiles(r.Context(), containerID, path)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("container")
	path := r.URL.Query().Get("path")
	content, err := s.docker.ReadFile(r.Context(), containerID, path)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": content})
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Container string `json:"container"`
		Path      string `json:"path"`
		Content   string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	if err := s.docker.WriteFile(r.Context(), req.Container, req.Path, req.Content); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "written"})
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	// Limit upload size to 50MB
	const maxUploadSize = 50 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "file is required (max 50MB)"})
		return
	}
	defer file.Close()

	containerID := r.FormValue("container")
	path := r.FormValue("path")

	content, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
	if err != nil {
		internalError(w, r, err)
		return
	}
	if int64(len(content)) > maxUploadSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "file too large (max 50MB)"})
		return
	}

	if err := s.docker.WriteFile(r.Context(), containerID, path, string(content)); err != nil {
		internalError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "uploaded"})
}

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("container")
	path := r.URL.Query().Get("path")

	content, err := s.docker.ReadFile(r.Context(), containerID, path)
	if err != nil {
		internalError(w, r, err)
		return
	}

	filename := sanitizeFilename(path)
	if idx := strings.LastIndex(filename, "/"); idx >= 0 {
		filename = filename[idx+1:]
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write([]byte(content))
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	containerID := r.URL.Query().Get("container")
	path := r.URL.Query().Get("path")
	if err := s.docker.DeleteFile(r.Context(), containerID, path); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (s *Server) handleContainerFileWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	containerID := chi.URLParam(r, "id")
	if err := s.docker.WriteFile(r.Context(), containerID, req.Path, req.Content); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "written"})
}

// --- Host handlers ---

func (s *Server) handleHostInfo(w http.ResponseWriter, r *http.Request) {
	ver, _ := s.docker.ServerVersion(r.Context())
	info := host.GetHostInfo(ver)
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleHostStats(w http.ResponseWriter, r *http.Request) {
	stats := host.GetHostStats()
	writeJSON(w, http.StatusOK, stats)
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("[ERROR] %s %s: %v", r.Method, r.URL.Path, err)
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal server error"})
}

func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, `"`, "'")
	return name
}

func buildAuthHeaderFunc(registryAuths map[string]string) docker.AuthHeaderFunc {
	return func(imageRef string) (string, error) {
		domain := docker.ExtractImageDomain(imageRef)
		if a, ok := registryAuths[strings.ToLower(domain)]; ok {
			return a, nil
		}
		return "", nil
	}
}
