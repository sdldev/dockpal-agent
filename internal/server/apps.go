package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/sdldev/dockpal-agent/internal/docker"
)

// handleSetAppAutoUpdate toggles the dockpal.auto-update label on the
// compose file and redeploys so containers pick up the new label.
//
// PATCH /agent/docker/apps/{name}/auto-update
func (s *Server) handleSetAppAutoUpdate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing app name"})
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}

	composePath, composeYAML, err := s.resolveAppCompose(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}

	labelValue := "true"
	if !req.Enabled {
		labelValue = ""
	}

	newYAML, err := docker.SetServiceLabel(composeYAML, "dockpal.auto-update", labelValue)
	if err != nil {
		internalError(w, r, err)
		return
	}

	// Write updated compose back to disk so it persists across restarts.
	if composePath != "" {
		if err := os.WriteFile(composePath, []byte(newYAML), 0644); err != nil {
			log.Printf("[WARN] failed to write compose file %s: %v", composePath, err)
			// non-fatal — the deploy below will still use the in-memory YAML
		}
	}

	// Redeploy with the updated labels (no pull, just recreate).
	noAuth := func(string) (string, error) { return "", nil }
	if err := s.docker.DeployCompose(r.Context(), name, newYAML, noAuth); err != nil {
		internalError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTriggerAppUpdate runs a manual pull-and-redeploy for the named app.
//
// POST /agent/docker/apps/{name}/update
func (s *Server) handleTriggerAppUpdate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing app name"})
		return
	}

	_, composeYAML, err := s.resolveAppCompose(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}

	attemptID := generateAttemptID()

	// Pull latest images and redeploy in background.
	go func() {
		noAuth := func(string) (string, error) { return "", nil }
		if err := s.docker.DeployCompose(context.Background(), name, composeYAML, noAuth); err != nil {
			log.Printf("[ERROR] app update %s (attempt %s): %v", name, attemptID, err)
		} else {
			log.Printf("[INFO] app update %s (attempt %s): completed", name, attemptID)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"attempt_id": attemptID})
}

// resolveAppCompose finds the compose YAML for an app by reading the
// dockpal.compose label from its containers and loading the file.
func (s *Server) resolveAppCompose(ctx context.Context, appName string) (path string, yaml string, err error) {
	containers, err := s.docker.ListContainersWithLabel(ctx, "dockpal.project="+appName)
	if err != nil {
		return "", "", err
	}
	if len(containers) == 0 {
		return "", "", errAppNotFound(appName)
	}

	composePath := containers[0].Labels["dockpal.compose"]
	if composePath == "" {
		return "", "", errNoComposePath(appName)
	}

	content, err := os.ReadFile(composePath)
	if err != nil {
		return "", "", err
	}
	return composePath, string(content), nil
}

func generateAttemptID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "att-" + hex.EncodeToString(b)
}

type appError struct{ msg string }

func (e appError) Error() string { return e.msg }

func errAppNotFound(name string) error {
	return appError{msg: "no containers found for app " + name}
}

func errNoComposePath(name string) error {
	return appError{msg: "app " + name + " has no dockpal.compose label"}
}
