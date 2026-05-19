package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sdldev/dockpal-agent/internal/config"
	"github.com/sdldev/dockpal-agent/internal/docker"
	"github.com/sdldev/dockpal-agent/internal/host"
)

// Client is the edge mode WebSocket client that connects to the Server.
type Client struct {
	cfg       *config.Config
	docker    *docker.Client
	conn      *websocket.Conn
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	deployMgr *docker.DeployManager
}

// NewClient creates a new edge mode client.
func NewClient(cfg *config.Config, dockerClient *docker.Client) *Client {
	return &Client{
		cfg:       cfg,
		docker:    dockerClient,
		deployMgr: docker.NewDeployManager(),
	}
}

// Run starts the edge client with automatic reconnection.
// The provided context controls the client's lifecycle.
func (c *Client) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := c.connectAndServe(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("Edge connection error: %v, reconnecting in %s...", err, c.cfg.EdgeReconnect)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.cfg.EdgeReconnect):
		}
	}
}

func (c *Client) connectAndServe(parentCtx context.Context) error {
	wsURL := c.cfg.EdgeServerURL + "/api/agent/connect"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?token="+c.cfg.Token, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.ctx, c.cancel = context.WithCancel(parentCtx)
	connCtx := c.ctx
	c.mu.Unlock()

	defer func() {
		c.cancel()
		conn.Close()
	}()

	log.Printf("Edge: connected to %s", wsURL)

	enrollMsg := EnrollMessage{Token: c.cfg.Token}
	if err := conn.WriteJSON(enrollMsg); err != nil {
		return fmt.Errorf("enroll: %w", err)
	}

	go c.heartbeat(connCtx)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var msg AgentRequest
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Edge: invalid message: %v", err)
			continue
		}

		go c.handleRequest(connCtx, msg)
	}
}

func (c *Client) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.EdgeHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			err := c.conn.WriteJSON(HeartbeatMessage{
				Type:    "heartbeat",
				Version: config.Version,
			})
			c.mu.Unlock()
			if err != nil {
				log.Printf("Edge: heartbeat failed: %v", err)
				return
			}
		}
	}
}

func (c *Client) handleRequest(ctx context.Context, msg AgentRequest) {
	var resp AgentResponse
	resp.RequestID = msg.RequestID

	switch {
	// Containers
	case msg.Path == "/docker/containers" && msg.Method == "GET":
		all := msg.Query["all"] == "true"
		containers, err := c.docker.ListContainers(ctx, all)
		if err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(containers)
		}

	case strings.HasPrefix(msg.Path, "/docker/containers/") && msg.Method == "GET":
		id := extractPathParam(msg.Path, 3)
		if strings.HasSuffix(msg.Path, "/stats") && !strings.Contains(msg.Path, "/stats/ws") {
			stats, err := c.docker.GetContainerStats(ctx, id)
			if err != nil {
				resp.Status = 500
				resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
			} else {
				resp.Status = 200
				resp.Body, _ = json.Marshal(stats)
			}
		} else if strings.HasSuffix(msg.Path, "/logs") {
			c.handleContainerLogs(ctx, msg, id)
			return
		} else if strings.HasSuffix(msg.Path, "/stats/ws") {
			c.handleStatsStream(ctx, msg, id)
			return
		} else {
			detail, err := c.docker.InspectContainer(ctx, id)
			if err != nil {
				resp.Status = 500
				resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
			} else {
				resp.Status = 200
				resp.Body, _ = json.Marshal(detail)
			}
		}

	case strings.HasPrefix(msg.Path, "/docker/containers/") && msg.Method == "POST":
		id := extractPathParam(msg.Path, 3)
		action := extractPathParam(msg.Path, 4)
		var err error
		switch action {
		case "start":
			err = c.docker.StartContainer(ctx, id)
		case "stop":
			err = c.docker.StopContainer(ctx, id)
		case "restart":
			err = c.docker.RestartContainer(ctx, id)
		default:
			resp.Status = 400
			resp.Body, _ = json.Marshal(map[string]string{"error": "unknown action"})
		}
		if err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
		} else if resp.Status == 0 {
			resp.Status = 200
			resp.Body, _ = json.Marshal(map[string]string{"status": action + "ed"})
		}

	case strings.HasPrefix(msg.Path, "/docker/containers/") && msg.Method == "DELETE":
		id := extractPathParam(msg.Path, 3)
		force := msg.Query["force"] == "true"
		if err := c.docker.RemoveContainer(ctx, id, force); err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(map[string]string{"status": "removed"})
		}

	case strings.HasPrefix(msg.Path, "/docker/containers/") && msg.Method == "PUT":
		id := extractPathParam(msg.Path, 3)
		var req docker.ContainerEditRequest
		if err := json.Unmarshal(msg.Body, &req); err != nil {
			resp.Status = 400
			resp.Body, _ = json.Marshal(map[string]string{"error": "invalid request body"})
		} else {
			detail, err := c.docker.EditContainer(ctx, id, req)
			if err != nil {
				resp.Status = 500
				resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
			} else {
				resp.Status = 200
				resp.Body, _ = json.Marshal(detail)
			}
		}

	// Deploy
	case msg.Path == "/docker/deploy/compose" && msg.Method == "POST":
		var req struct {
			Name          string            `json:"name"`
			Compose       string            `json:"compose"`
			RegistryAuths map[string]string `json:"registry_auths,omitempty"`
		}
		if err := json.Unmarshal(msg.Body, &req); err != nil {
			resp.Status = 400
			resp.Body, _ = json.Marshal(map[string]string{"error": "invalid request body"})
		} else {
			getAuthHeader := buildAuthHeaderFunc(req.RegistryAuths)
			if err := c.docker.DeployCompose(ctx, req.Name, req.Compose, getAuthHeader); err != nil {
				resp.Status = 500
				resp.Body, _ = json.Marshal(map[string]string{"error": err.Error()})
			} else {
				resp.Status = 200
				resp.Body, _ = json.Marshal(map[string]string{"status": "deployed"})
			}
		}

	case msg.Path == "/docker/deploy/stream" && msg.Method == "POST":
		c.handleDeployStream(ctx, msg)
		return

	// Compose stop/remove
	case msg.Path == "/compose/stop" && msg.Method == "POST":
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(msg.Body, &req); err != nil {
			resp.Status = 400
			resp.Body, _ = json.Marshal(map[string]string{"error": "invalid request body"})
		} else if err := c.docker.StopCompose(ctx, req.Name); err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(map[string]string{"status": "stopped"})
		}

	case msg.Path == "/compose/remove" && msg.Method == "POST":
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(msg.Body, &req); err != nil {
			resp.Status = 400
			resp.Body, _ = json.Marshal(map[string]string{"error": "invalid request body"})
		} else if err := c.docker.RemoveCompose(ctx, req.Name); err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(map[string]string{"status": "removed"})
		}

	// Images
	case msg.Path == "/docker/images" && msg.Method == "GET":
		images, err := c.docker.ListImages(ctx)
		if err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(images)
		}

	case msg.Path == "/docker/images/pull" && msg.Method == "POST":
		var req struct {
			Image        string `json:"image"`
			RegistryAuth string `json:"registry_auth,omitempty"`
		}
		if err := json.Unmarshal(msg.Body, &req); err != nil {
			resp.Status = 400
			resp.Body, _ = json.Marshal(map[string]string{"error": "invalid request body"})
		} else {
			var err error
			if req.RegistryAuth != "" {
				err = c.docker.PullImageWithAuth(ctx, req.Image, req.RegistryAuth)
			} else {
				err = c.docker.PullImage(ctx, req.Image)
			}
			if err != nil {
				resp.Status = 500
				resp.Body, _ = json.Marshal(map[string]string{"error": err.Error()})
			} else {
				resp.Status = 200
				resp.Body, _ = json.Marshal(map[string]string{"status": "pulled"})
			}
		}

	case strings.HasPrefix(msg.Path, "/docker/images/") && msg.Method == "DELETE":
		id := extractPathParam(msg.Path, 3)
		if err := c.docker.RemoveImage(ctx, id); err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": "internal error"})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(map[string]string{"status": "removed"})
		}

	// Files
	case msg.Path == "/docker/files" && msg.Method == "GET":
		containerID := msg.Query["container"]
		path := msg.Query["path"]
		files, err := c.docker.ListFiles(ctx, containerID, path)
		if err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": err.Error()})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(files)
		}

	case msg.Path == "/docker/files/read" && msg.Method == "GET":
		containerID := msg.Query["container"]
		path := msg.Query["path"]
		content, err := c.docker.ReadFile(ctx, containerID, path)
		if err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": err.Error()})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(map[string]string{"content": content})
		}

	case msg.Path == "/docker/files/write" && msg.Method == "POST":
		var req struct {
			Container string `json:"container"`
			Path      string `json:"path"`
			Content   string `json:"content"`
		}
		if err := json.Unmarshal(msg.Body, &req); err != nil {
			resp.Status = 400
			resp.Body, _ = json.Marshal(map[string]string{"error": "invalid request body"})
		} else if err := c.docker.WriteFile(ctx, req.Container, req.Path, req.Content); err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": err.Error()})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(map[string]string{"status": "written"})
		}

	case msg.Path == "/docker/files" && msg.Method == "DELETE":
		containerID := msg.Query["container"]
		path := msg.Query["path"]
		if err := c.docker.DeleteFile(ctx, containerID, path); err != nil {
			resp.Status = 500
			resp.Body, _ = json.Marshal(map[string]string{"error": err.Error()})
		} else {
			resp.Status = 200
			resp.Body, _ = json.Marshal(map[string]string{"status": "deleted"})
		}

	// Host
	case msg.Path == "/host/info" && msg.Method == "GET":
		ver, _ := c.docker.ServerVersion(ctx)
		info := host.GetHostInfo(ver)
		resp.Status = 200
		resp.Body, _ = json.Marshal(info)

	case msg.Path == "/host/stats" && msg.Method == "GET":
		stats := host.GetHostStats()
		resp.Status = 200
		resp.Body, _ = json.Marshal(stats)

	default:
		resp.Status = 404
		resp.Body, _ = json.Marshal(map[string]string{"error": "not found"})
	}

	c.sendResponse(resp)
}

func (c *Client) handleContainerLogs(ctx context.Context, msg AgentRequest, containerID string) {
	reader, err := c.docker.ContainerLogs(ctx, containerID, "100")
	if err != nil {
		c.sendResponse(AgentResponse{
			RequestID: msg.RequestID,
			Status:    500,
			Body:      mustMarshal(map[string]string{"error": "internal error"}),
		})
		return
	}
	defer reader.Close()

	buf := make([]byte, 4096)
	chunk := 0
	for {
		select {
		case <-ctx.Done():
			c.sendStreamEnd(msg.RequestID, 200)
			return
		default:
		}
		n, err := reader.Read(buf)
		if n > 0 {
			c.sendStreamChunk(msg.RequestID, chunk, buf[:n])
			chunk++
		}
		if err != nil {
			break
		}
	}
	c.sendStreamEnd(msg.RequestID, 200)
}

func (c *Client) handleStatsStream(ctx context.Context, msg AgentRequest, containerID string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Send initial stats
	stats, err := c.docker.GetContainerStats(ctx, containerID)
	if err != nil {
		c.sendStreamEnd(msg.RequestID, 500)
		return
	}
	data, _ := json.Marshal(stats)
	c.sendStreamChunk(msg.RequestID, 0, data)

	chunk := 1
	for {
		select {
		case <-ctx.Done():
			c.sendStreamEnd(msg.RequestID, 200)
			return
		case <-ticker.C:
			stats, err := c.docker.GetContainerStats(ctx, containerID)
			if err != nil {
				c.sendStreamEnd(msg.RequestID, 200)
				return
			}
			data, _ := json.Marshal(stats)
			c.sendStreamChunk(msg.RequestID, chunk, data)
			chunk++
		}
	}
}

func (c *Client) handleDeployStream(ctx context.Context, msg AgentRequest) {
	var req struct {
		Name          string            `json:"name"`
		Compose       string            `json:"compose"`
		RegistryAuths map[string]string `json:"registry_auths,omitempty"`
	}
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		c.sendResponse(AgentResponse{
			RequestID: msg.RequestID,
			Status:    400,
			Body:      mustMarshal(map[string]string{"error": "invalid request body"}),
		})
		return
	}

	session := c.deployMgr.CreateSession()
	getAuthHeader := buildAuthHeaderFunc(req.RegistryAuths)

	// Send deploy_id back
	c.sendResponse(AgentResponse{
		RequestID: msg.RequestID,
		Status:    200,
		Body:      mustMarshal(map[string]string{"deploy_id": session.ID}),
	})

	// Run deploy in background, stream events
	go func() {
		_ = c.docker.DeployComposeStreamed(ctx, req.Name, req.Compose, session, getAuthHeader)
	}()

	// Stream events — drain Events channel fully before checking Done
	chunk := 0
	for {
		select {
		case event, ok := <-session.Events:
			if !ok {
				c.sendStreamEnd(msg.RequestID, 200)
				c.deployMgr.RemoveSession(session.ID)
				return
			}
			data, _ := json.Marshal(event)
			c.sendStreamChunk(msg.RequestID, chunk, data)
			chunk++
		case <-session.Done:
			// Drain remaining events
			for event := range session.Events {
				data, _ := json.Marshal(event)
				c.sendStreamChunk(msg.RequestID, chunk, data)
				chunk++
			}
			c.sendStreamEnd(msg.RequestID, 200)
			c.deployMgr.RemoveSession(session.ID)
			return
		case <-ctx.Done():
			c.sendStreamEnd(msg.RequestID, 200)
			c.deployMgr.RemoveSession(session.ID)
			return
		}
	}
}

func (c *Client) sendResponse(resp AgentResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn.WriteJSON(resp)
}

func (c *Client) sendStreamChunk(requestID string, chunk int, data []byte) {
	resp := AgentResponse{
		RequestID: requestID,
		Status:    200,
		Stream:    true,
		Chunk:     chunk,
		Body:      json.RawMessage(data),
	}
	c.sendResponse(resp)
}

func (c *Client) sendStreamEnd(requestID string, status int) {
	resp := AgentResponse{
		RequestID: requestID,
		Status:    status,
		Done:      true,
	}
	c.sendResponse(resp)
}

func extractPathParam(path string, index int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if index < len(parts) {
		return parts[index]
	}
	return ""
}

func buildAuthHeaderFunc(registryAuths map[string]string) docker.AuthHeaderFunc {
	return func(imageRef string) (string, error) {
		domain := docker.ExtractImageDomain(imageRef)
		if auth, ok := registryAuths[strings.ToLower(domain)]; ok {
			return auth, nil
		}
		return "", nil
	}
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
