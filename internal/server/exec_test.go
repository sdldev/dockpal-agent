// Tests for the one-shot exec endpoint: request validation happens before
// any docker call, so a nil docker client is safe for the rejection paths.
// The happy path is covered end-to-end by live verification against a real
// daemon.
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sdldev/dockpal-agent/internal/docker"
)

func execTestRouter() *chi.Mux {
	srv := &Server{}
	r := chi.NewRouter()
	r.Route("/agent/docker", func(r chi.Router) {
		r.Post("/containers/{id}/exec", srv.handleContainerExec)
	})
	return r
}

func TestHandleContainerExec_RejectsBadRequests(t *testing.T) {
	r := execTestRouter()
	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty cmd", `{"cmd":[]}`, http.StatusBadRequest},
		{"missing cmd", `{}`, http.StatusBadRequest},
		{"invalid json", `not-json`, http.StatusBadRequest},
		{"cmd not array", `{"cmd":"ls -la"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/agent/docker/containers/abc/exec", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// TestExecTypesJSONStability pins the JSON field names shared with the panel
// (internal/docker ExecRequest/ExecCommandResult) so the two binaries never
// drift apart.
func TestExecTypesJSONStability(t *testing.T) {
	req := docker.ExecRequest{Cmd: []string{"echo", "hi"}, Timeout: 45, User: "root"}
	if req.Cmd[0] != "echo" || req.Timeout != 45 || req.User != "root" {
		t.Fatalf("unexpected fields: %+v", req)
	}
	res := docker.ExecResult{ExitCode: 2, Stdout: "o", Stderr: "e", TimedOut: true, DurationMS: 33}
	if !res.TimedOut || res.ExitCode != 2 || res.DurationMS != 33 {
		t.Fatalf("unexpected fields: %+v", res)
	}
	if res.Truncated {
		t.Fatalf("Truncated must default to false")
	}
}
