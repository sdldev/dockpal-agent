package server

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sdldev/dockpal-agent/internal/composecli"
	"github.com/sdldev/dockpal-agent/internal/docker"
)

// Dockge-style compose stack handlers for direct mode.
// Mirrors the Server's internal/server/routes_stacks.go (copy-port).

// registerStackRoutes mounts /stacks under the authenticated /agent/docker group.
func (s *Server) registerStackRoutes(r chi.Router) {
	r.Get("/stacks", s.handleListStacks)
	r.Get("/stacks/meta/networks", s.handleListDockerNetworks)
	r.Get("/stacks/meta/globalenv", s.handleGetGlobalEnv)
	r.Put("/stacks/meta/globalenv", s.handleSetGlobalEnv)
	r.Get("/stacks/{name}", s.handleGetStack)

	r.Post("/stacks", s.handleCreateStack)
	r.Put("/stacks/{name}", s.handleUpdateStack)
	r.Delete("/stacks/{name}", s.handleDeleteStack)

	r.Post("/stacks/{name}/deploy", s.handleDeployStack)
	r.Get("/stacks/deploy/stream/{deploy_id}", s.handleStackDeployStreamWS)

	for _, action := range []string{"up", "start", "stop", "restart", "down", "update"} {
		action := action
		r.Post("/stacks/{name}/"+action, func(w http.ResponseWriter, req *http.Request) {
			s.handleStackAction(w, req, action)
		})
	}

	r.Post("/stacks/{name}/services/{service}/up", func(w http.ResponseWriter, req *http.Request) {
		s.handleStackServiceAction(w, req, "up")
	})
	r.Post("/stacks/{name}/services/{service}/stop", func(w http.ResponseWriter, req *http.Request) {
		s.handleStackServiceAction(w, req, "stop")
	})
	r.Post("/stacks/{name}/services/{service}/restart", func(w http.ResponseWriter, req *http.Request) {
		s.handleStackServiceAction(w, req, "restart")
	})
}

// stackCLIUnavailable responds 501 when the docker compose plugin is missing.
func (s *Server) stackCLIUnavailable(w http.ResponseWriter) bool {
	if !composecli.Available() {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "docker compose CLI not available on this host"})
		return true
	}
	return false
}

// stackError preserves the error message so the Server can map the taxonomy
// (409 busy/exists, 404 not-found, 400 invalid) across the wire.
func (s *Server) stackError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case composecli.IsBusy(err), strings.Contains(msg, "another operation is already running"):
		writeJSON(w, http.StatusConflict, map[string]any{"error": msg})
	case strings.Contains(msg, "not found"):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": msg})
	case strings.Contains(msg, "already exists"):
		writeJSON(w, http.StatusConflict, map[string]any{"error": msg})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg})
	}
}

func (s *Server) handleListStacks(w http.ResponseWriter, r *http.Request) {
	if s.stackCLIUnavailable(w) {
		return
	}
	stacks, err := docker.ListStacks(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stacks": stacks})
}

func (s *Server) handleListDockerNetworks(w http.ResponseWriter, r *http.Request) {
	if s.stackCLIUnavailable(w) {
		return
	}
	names, err := docker.ListDockerNetworks(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"networks": names})
}

func (s *Server) handleGetGlobalEnv(w http.ResponseWriter, r *http.Request) {
	content, err := docker.GetGlobalEnv()
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": content})
}

func (s *Server) handleSetGlobalEnv(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	if err := docker.SetGlobalEnv(req.Content); err != nil {
		s.stackError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "saved"})
}

func (s *Server) handleGetStack(w http.ResponseWriter, r *http.Request) {
	if s.stackCLIUnavailable(w) {
		return
	}
	stack, err := docker.GetStackFull(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		s.stackError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stack)
}

func (s *Server) handleCreateStack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Compose string `json:"compose"`
		Env     string `json:"env"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	if err := docker.SaveStack(req.Name, req.Compose, req.Env, true); err != nil {
		s.stackError(w, err)
		return
	}
	stack, err := docker.GetStack(req.Name)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, stack)
}

func (s *Server) handleUpdateStack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Compose string `json:"compose"`
		Env     string `json:"env"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	name := chi.URLParam(r, "name")
	if err := docker.SaveStack(name, req.Compose, req.Env, false); err != nil {
		s.stackError(w, err)
		return
	}
	stack, err := docker.GetStack(name)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stack)
}

func (s *Server) handleDeleteStack(w http.ResponseWriter, r *http.Request) {
	if s.stackCLIUnavailable(w) {
		return
	}
	name := chi.URLParam(r, "name")
	if err := docker.StackDelete(r.Context(), name); err != nil {
		s.stackError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

// handleDeployStack saves the stack (create-or-update) then runs
// `docker compose up -d --remove-orphans` in the background; progress is
// streamed over the stack deploy WS.
func (s *Server) handleDeployStack(w http.ResponseWriter, r *http.Request) {
	if s.stackCLIUnavailable(w) {
		return
	}
	name := chi.URLParam(r, "name")
	var req struct {
		Compose string `json:"compose"`
		Env     string `json:"env"`
		IsAdd   bool   `json:"is_add"`
	}
	if err := readJSON(r, &req); err != nil {
		// empty body is fine — deploy whatever is on disk
		req.Compose = ""
		req.Env = ""
		req.IsAdd = false
	}

	if req.Compose != "" {
		if err := docker.SaveStack(name, req.Compose, req.Env, req.IsAdd); err != nil {
			s.stackError(w, err)
			return
		}
	} else if _, err := docker.GetStack(name); err != nil {
		s.stackError(w, err)
		return
	}

	session := s.deployMgr.CreateSession()
	go func() {
		err := composecli.StackUpStreamed(context.Background(), name, session)
		if err == nil {
			session.Emit("done", "Deployed", "done")
		}
		time.AfterFunc(30*time.Second, func() {
			s.deployMgr.RemoveSession(session.ID)
		})
	}()

	writeJSON(w, http.StatusOK, map[string]any{"deploy_id": session.ID})
}

// handleStackDeployStreamWS relays DeployEvents to the caller (direct mode).
func (s *Server) handleStackDeployStreamWS(w http.ResponseWriter, r *http.Request) {
	deployID := chi.URLParam(r, "deploy_id")
	session := s.deployMgr.GetSession(deployID)
	if session == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ERROR] stack deploy WS upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	for {
		select {
		case event, ok := <-session.Events:
			if !ok {
				return
			}
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		case <-session.Done:
			for {
				select {
				case event, ok := <-session.Events:
					if !ok {
						return
					}
					conn.WriteJSON(event)
				default:
					return
				}
			}
		}
	}
}

func (s *Server) handleStackAction(w http.ResponseWriter, r *http.Request, action string) {
	if s.stackCLIUnavailable(w) {
		return
	}
	name := chi.URLParam(r, "name")
	ctx := r.Context()

	var err error
	switch action {
	case "up", "start":
		err = docker.StackUp(ctx, name)
	case "stop":
		err = docker.StackStop(ctx, name)
	case "restart":
		err = docker.StackRestart(ctx, name)
	case "down":
		err = docker.StackDown(ctx, name)
	case "update":
		err = docker.StackUpdate(ctx, name)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
		return
	}
	if err != nil {
		s.stackError(w, err)
		return
	}

	stack, getErr := docker.GetStackFull(ctx, name)
	if getErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"message": action + " ok"})
		return
	}
	writeJSON(w, http.StatusOK, stack)
}

func (s *Server) handleStackServiceAction(w http.ResponseWriter, r *http.Request, action string) {
	if s.stackCLIUnavailable(w) {
		return
	}
	name := chi.URLParam(r, "name")
	service := chi.URLParam(r, "service")
	if service == "" || strings.ContainsAny(service, "/\\ ") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid service name"})
		return
	}
	ctx := r.Context()

	var err error
	switch action {
	case "up":
		err = docker.StackServiceUp(ctx, name, service)
	case "stop":
		err = docker.StackServiceStop(ctx, name, service)
	case "restart":
		err = docker.StackServiceRestart(ctx, name, service)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
		return
	}
	if err != nil {
		s.stackError(w, err)
		return
	}
	stack, getErr := docker.GetStackFull(ctx, name)
	if getErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"message": action + " ok"})
		return
	}
	writeJSON(w, http.StatusOK, stack)
}