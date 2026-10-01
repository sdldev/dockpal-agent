package server

// Tests for the Dockge-style /agent/docker/stacks handlers. The compose CLI
// backend is swapped for a fake via docker.RegisterStackCLI and the stacks
// base directory is pointed at a temp dir, so no docker daemon or compose
// plugin is needed.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sdldev/dockpal-agent/internal/docker"
)

// fakeStackCLI implements docker's stackCLI seam (exported methods).
type fakeStackCLI struct {
	mu     sync.Mutex
	runs   [][]string
	lsOut  string
	locked map[string]bool
}

func (f *fakeStackCLI) Run(ctx context.Context, dir string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, append([]string{dir}, args...))
	return nil
}

func (f *fakeStackCLI) Output(ctx context.Context, dir string, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	if strings.HasPrefix(joined, "ls") {
		return f.lsOut, nil
	}
	return "", nil
}

func (f *fakeStackCLI) TryLock(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locked == nil {
		f.locked = map[string]bool{}
	}
	if f.locked[name] {
		return false
	}
	f.locked[name] = true
	return true
}

func (f *fakeStackCLI) Unlock(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.locked, name)
}

func stackTestEnv(t *testing.T) (*chi.Mux, *fakeStackCLI) {
	t.Helper()
	base := filepath.Join(t.TempDir(), "compose")
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(docker.SetComposeBasePathForTest(base))

	fake := &fakeStackCLI{}
	docker.RegisterStackCLI(fake)
	t.Cleanup(func() { docker.RegisterStackCLI(nil) })

	srv := &Server{deployMgr: docker.NewDeployManager()}
	r := chi.NewRouter()
	r.Route("/agent/docker", func(r chi.Router) {
		srv.registerStackRoutes(r)
	})
	return r, fake
}

func doStackJSON(t *testing.T, r *chi.Mux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestStackHandlersCRUD(t *testing.T) {
	r, _ := stackTestEnv(t)

	// Create
	w := doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks", map[string]string{
		"name":    "web",
		"compose": "services:\n  app:\n    image: nginx:latest\n",
		"env":     "TAG=latest\n",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}

	// Duplicate create → 409
	w = doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks", map[string]string{
		"name":    "web",
		"compose": "services:\n  app:\n    image: nginx:latest\n",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate create: %d %s", w.Code, w.Body.String())
	}

	// Get
	w = doStackJSON(t, r, http.MethodGet, "/agent/docker/stacks/web", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	var stack docker.Stack
	if err := json.Unmarshal(w.Body.Bytes(), &stack); err != nil {
		t.Fatal(err)
	}
	if !stack.Managed || !strings.Contains(stack.ComposeYAML, "nginx") || stack.ComposeENV != "TAG=latest\n" {
		t.Errorf("unexpected stack: %+v", stack)
	}

	// Update
	w = doStackJSON(t, r, http.MethodPut, "/agent/docker/stacks/web", map[string]string{
		"compose": "services:\n  app:\n    image: nginx:1.27\n",
		"env":     "",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "1.27") {
		t.Errorf("update not reflected: %s", w.Body.String())
	}

	// List
	w = doStackJSON(t, r, http.MethodGet, "/agent/docker/stacks", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "web") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}

	// Delete
	w = doStackJSON(t, r, http.MethodDelete, "/agent/docker/stacks/web", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}

	// Gone
	w = doStackJSON(t, r, http.MethodGet, "/agent/docker/stacks/web", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d %s", w.Code, w.Body.String())
	}
}

func TestStackHandlersValidation(t *testing.T) {
	r, _ := stackTestEnv(t)

	// Bad name → 400 with message preserved
	w := doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks", map[string]string{
		"name":    "Bad Name",
		"compose": "services: {}\n",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad name: %d %s", w.Code, w.Body.String())
	}

	// Bad YAML → 400
	w = doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks", map[string]string{
		"name":    "ok",
		"compose": "services: [nope]",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad yaml: %d %s", w.Code, w.Body.String())
	}

	// Update missing stack → 404
	w = doStackJSON(t, r, http.MethodPut, "/agent/docker/stacks/ghost", map[string]string{
		"compose": "services: {}\n",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("update ghost: %d %s", w.Code, w.Body.String())
	}
}

func TestStackHandlersActions(t *testing.T) {
	r, fake := stackTestEnv(t)

	w := doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks", map[string]string{
		"name":    "web",
		"compose": "services:\n  app:\n    image: nginx:latest\n",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}

	for _, action := range []string{"up", "start", "stop", "restart", "down", "update"} {
		w := doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks/web/"+action, nil)
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}

	fake.mu.Lock()
	runCount := len(fake.runs)
	fake.mu.Unlock()
	if runCount < 6 {
		t.Fatalf("expected >=6 runs, got %d", runCount)
	}

	// Service action
	w = doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks/web/services/app/restart", nil)
	if w.Code != http.StatusOK {
		t.Errorf("service restart: %d %s", w.Code, w.Body.String())
	}

	// Invalid service name rejected before hitting compose
	w = doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks/web/services/bad%20name/restart", nil)
	if w.Code == http.StatusOK {
		t.Errorf("expected invalid service name rejection, got 200")
	}
}

func TestStackHandlersBusyConflict(t *testing.T) {
	r, fake := stackTestEnv(t)

	w := doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks", map[string]string{
		"name":    "web",
		"compose": "services:\n  app:\n    image: nginx:latest\n",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}

	fake.locked = map[string]bool{"web": true}
	w = doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks/web/stop", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("busy stop: %d %s", w.Code, w.Body.String())
	}
}

func TestStackHandlersGlobalEnv(t *testing.T) {
	r, _ := stackTestEnv(t)

	// Absent → empty content
	w := doStackJSON(t, r, http.MethodGet, "/agent/docker/stacks/meta/globalenv", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get globalenv: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"content":""`) {
		t.Errorf("expected empty content: %s", w.Body.String())
	}

	// Set
	w = doStackJSON(t, r, http.MethodPut, "/agent/docker/stacks/meta/globalenv", map[string]string{
		"content": "GLOBAL=1\n",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("set globalenv: %d %s", w.Code, w.Body.String())
	}

	// Roundtrip
	w = doStackJSON(t, r, http.MethodGet, "/agent/docker/stacks/meta/globalenv", nil)
	if !strings.Contains(w.Body.String(), "GLOBAL=1") {
		t.Errorf("globalenv roundtrip: %s", w.Body.String())
	}

	// Invalid .env format → 400
	w = doStackJSON(t, r, http.MethodPut, "/agent/docker/stacks/meta/globalenv", map[string]string{
		"content": "BAD_LINE\n",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid globalenv: %d %s", w.Code, w.Body.String())
	}
}

func TestStackHandlersDeployReturnsSessionID(t *testing.T) {
	r, _ := stackTestEnv(t)

	w := doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks", map[string]string{
		"name":    "web",
		"compose": "services:\n  app:\n    image: nginx:latest\n",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}

	w = doStackJSON(t, r, http.MethodPost, "/agent/docker/stacks/web/deploy", map[string]any{
		"compose": "services:\n  app:\n    image: nginx:1.27\n",
		"env":     "",
		"is_add":  false,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		DeployID string `json:"deploy_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.DeployID, "deploy-") {
		t.Errorf("unexpected deploy_id: %q", res.DeployID)
	}

	// Compose was saved before deploy kicked off.
	stack, err := docker.GetStack("web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stack.ComposeYAML, "1.27") {
		t.Errorf("deploy should save compose first")
	}
}
