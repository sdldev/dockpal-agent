package edge

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sdldev/dockpal-agent/internal/composecli"
	"github.com/sdldev/dockpal-agent/internal/docker"
)

// Dockge-style stack request dispatch for edge mode.
// Mirrors the direct-mode handlers in internal/server/stacks.go, but framed
// as AgentRequest/AgentResponse over the multiplexed WebSocket.

// edgeStackError maps stack errors to HTTP-ish status codes, preserving the
// message so the Server can apply its error taxonomy across the wire.
func edgeStackError(err error) (int, string) {
	msg := err.Error()
	switch {
	case composecli.IsBusy(err), strings.Contains(msg, "another operation is already running"):
		return 409, msg
	case strings.Contains(msg, "not found"):
		return 404, msg
	case strings.Contains(msg, "already exists"):
		return 409, msg
	default:
		return 400, msg
	}
}

// handleStackRequest dispatches /docker/stacks* requests. It always sends
// exactly one response (deploy sends a stream + end marker).
func (c *Client) handleStackRequest(ctx context.Context, msg AgentRequest) {
	resp := AgentResponse{RequestID: msg.RequestID}

	parts := strings.Split(strings.Trim(msg.Path, "/"), "/")
	// parts: [docker, stacks, ...]
	sub := parts[2:]

	switch {
	case len(sub) == 1 && sub[0] == "stacks" && msg.Method == "GET":
		stacks, err := docker.ListStacks(ctx)
		if err != nil {
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 500, Body: mustMarshal(map[string]string{"error": "internal error"})})
			return
		}
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 200, Body: mustMarshal(map[string]any{"stacks": stacks})})

	case len(sub) == 3 && sub[0] == "stacks" && sub[1] == "meta" && sub[2] == "networks" && msg.Method == "GET":
		names, err := docker.ListDockerNetworks(ctx)
		if err != nil {
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 500, Body: mustMarshal(map[string]string{"error": "internal error"})})
			return
		}
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 200, Body: mustMarshal(map[string]any{"networks": names})})

	case len(sub) == 3 && sub[0] == "stacks" && sub[1] == "meta" && sub[2] == "globalenv" && msg.Method == "GET":
		content, err := docker.GetGlobalEnv()
		if err != nil {
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 500, Body: mustMarshal(map[string]string{"error": "internal error"})})
			return
		}
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 200, Body: mustMarshal(map[string]any{"content": content})})

	case len(sub) == 3 && sub[0] == "stacks" && sub[1] == "meta" && sub[2] == "globalenv" && msg.Method == "PUT":
		var req struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(msg.Body, &req); err != nil {
			resp.Status = 400
			resp.Body = mustMarshal(map[string]string{"error": "invalid request body"})
			c.sendResponse(resp)
			return
		}
		if err := docker.SetGlobalEnv(req.Content); err != nil {
			status, text := edgeStackError(err)
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: status, Body: mustMarshal(map[string]string{"error": text})})
			return
		}
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 200, Body: mustMarshal(map[string]string{"message": "saved"})})

	case len(sub) == 2 && sub[0] == "stacks" && msg.Method == "GET":
		stack, err := docker.GetStackFull(ctx, sub[1])
		if err != nil {
			status, text := edgeStackError(err)
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: status, Body: mustMarshal(map[string]string{"error": text})})
			return
		}
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 200, Body: mustMarshal(stack)})

	case len(sub) == 1 && sub[0] == "stacks" && msg.Method == "POST":
		var req struct {
			Name    string `json:"name"`
			Compose string `json:"compose"`
			Env     string `json:"env"`
		}
		if err := json.Unmarshal(msg.Body, &req); err != nil {
			resp.Status = 400
			resp.Body = mustMarshal(map[string]string{"error": "invalid request body"})
			c.sendResponse(resp)
			return
		}
		if err := docker.SaveStack(req.Name, req.Compose, req.Env, true); err != nil {
			status, text := edgeStackError(err)
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: status, Body: mustMarshal(map[string]string{"error": text})})
			return
		}
		stack, err := docker.GetStack(req.Name)
		if err != nil {
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 500, Body: mustMarshal(map[string]string{"error": "internal error"})})
			return
		}
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 201, Body: mustMarshal(stack)})

	case len(sub) == 2 && sub[0] == "stacks" && msg.Method == "PUT":
		var req struct {
			Compose string `json:"compose"`
			Env     string `json:"env"`
		}
		if err := json.Unmarshal(msg.Body, &req); err != nil {
			resp.Status = 400
			resp.Body = mustMarshal(map[string]string{"error": "invalid request body"})
			c.sendResponse(resp)
			return
		}
		if err := docker.SaveStack(sub[1], req.Compose, req.Env, false); err != nil {
			status, text := edgeStackError(err)
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: status, Body: mustMarshal(map[string]string{"error": text})})
			return
		}
		stack, err := docker.GetStack(sub[1])
		if err != nil {
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 500, Body: mustMarshal(map[string]string{"error": "internal error"})})
			return
		}
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 200, Body: mustMarshal(stack)})

	case len(sub) == 2 && sub[0] == "stacks" && msg.Method == "DELETE":
		if err := docker.StackDelete(ctx, sub[1]); err != nil {
			status, text := edgeStackError(err)
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: status, Body: mustMarshal(map[string]string{"error": text})})
			return
		}
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 200, Body: mustMarshal(map[string]string{"message": "deleted"})})

	case len(sub) == 3 && sub[0] == "stacks" && sub[2] == "deploy" && msg.Method == "POST":
		c.handleEdgeStackDeploy(ctx, msg, sub[1])
		return

	case len(sub) == 3 && sub[0] == "stacks" && msg.Method == "POST":
		// /docker/stacks/{name}/{action}
		name, action := sub[1], sub[2]
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
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 400, Body: mustMarshal(map[string]string{"error": "unknown action"})})
			return
		}
		if err != nil {
			status, text := edgeStackError(err)
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: status, Body: mustMarshal(map[string]string{"error": text})})
			return
		}
		c.sendEdgeStackFull(ctx, msg, name, action)

	case len(sub) == 5 && sub[0] == "stacks" && sub[2] == "services" && msg.Method == "POST":
		// /docker/stacks/{name}/services/{svc}/{action}
		name, svc, action := sub[1], sub[3], sub[4]
		if svc == "" || strings.ContainsAny(svc, "/\\ ") {
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 400, Body: mustMarshal(map[string]string{"error": "invalid service name"})})
			return
		}
		var err error
		switch action {
		case "up":
			err = docker.StackServiceUp(ctx, name, svc)
		case "stop":
			err = docker.StackServiceStop(ctx, name, svc)
		case "restart":
			err = docker.StackServiceRestart(ctx, name, svc)
		default:
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 400, Body: mustMarshal(map[string]string{"error": "unknown action"})})
			return
		}
		if err != nil {
			status, text := edgeStackError(err)
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: status, Body: mustMarshal(map[string]string{"error": text})})
			return
		}
		c.sendEdgeStackFull(ctx, msg, name, action)

	default:
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 404, Body: mustMarshal(map[string]string{"error": "unknown stack path"})})
	}
}

// sendEdgeStackFull responds with the stack's refreshed status, falling back
// to a plain message when the status lookup fails.
func (c *Client) sendEdgeStackFull(ctx context.Context, msg AgentRequest, name, action string) {
	stack, err := docker.GetStackFull(ctx, name)
	if err != nil {
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 200, Body: mustMarshal(map[string]string{"message": action + " ok"})})
		return
	}
	c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: 200, Body: mustMarshal(stack)})
}

// handleEdgeStackDeploy saves the stack then streams `docker compose up` output
// as chunked AgentResponse frames (Server-side EdgeClient polls these).
func (c *Client) handleEdgeStackDeploy(ctx context.Context, msg AgentRequest, name string) {
	var req struct {
		Compose string `json:"compose"`
		Env     string `json:"env"`
		IsAdd   bool   `json:"is_add"`
	}
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		req.Compose = ""
		req.Env = ""
		req.IsAdd = false
	}

	if req.Compose != "" {
		if err := docker.SaveStack(name, req.Compose, req.Env, req.IsAdd); err != nil {
			status, text := edgeStackError(err)
			c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: status, Body: mustMarshal(map[string]string{"error": text})})
			return
		}
	} else if _, err := docker.GetStack(name); err != nil {
		status, text := edgeStackError(err)
		c.sendResponse(AgentResponse{RequestID: msg.RequestID, Status: status, Body: mustMarshal(map[string]string{"error": text})})
		return
	}

	session := c.deployMgr.CreateSession()

	// First frame: the deploy_id, mirroring direct mode.
	c.sendResponse(AgentResponse{
		RequestID: msg.RequestID,
		Status:    200,
		Body:      mustMarshal(map[string]string{"deploy_id": session.ID}),
	})

	go func() {
		if err := composecli.StackUpStreamed(context.Background(), name, session); err == nil {
			session.Emit("done", "Deployed", "done")
		}
		time.AfterFunc(30*time.Second, func() {
			c.deployMgr.RemoveSession(session.ID)
		})
	}()

	chunk := 0
	for {
		select {
		case event, ok := <-session.Events:
			if !ok {
				c.sendStreamEnd(msg.RequestID, 200)
				return
			}
			data, _ := json.Marshal(event)
			c.sendStreamChunk(msg.RequestID, chunk, data)
			chunk++
		case <-session.Done:
			for event := range session.Events {
				data, _ := json.Marshal(event)
				c.sendStreamChunk(msg.RequestID, chunk, data)
				chunk++
			}
			c.sendStreamEnd(msg.RequestID, 200)
			return
		case <-ctx.Done():
			c.sendStreamEnd(msg.RequestID, 200)
			return
		}
	}
}