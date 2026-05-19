package edge

import "encoding/json"

// AgentRequest is a request sent from Server to Agent over the WebSocket.
type AgentRequest struct {
	RequestID string            `json:"request_id"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Query     map[string]string `json:"query,omitempty"`
	Body      json.RawMessage   `json:"body,omitempty"`
}

// AgentResponse is a message sent from Agent to Server over the WebSocket.
type AgentResponse struct {
	RequestID string          `json:"request_id"`
	Status    int             `json:"status"`
	Body      json.RawMessage `json:"body,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
	Chunk     int             `json:"chunk,omitempty"`
	Done      bool            `json:"done,omitempty"`
}

// EnrollMessage is the first message sent by the agent after connecting.
type EnrollMessage struct {
	Token string `json:"token"`
}

// HeartbeatMessage is sent periodically by the agent.
type HeartbeatMessage struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}
