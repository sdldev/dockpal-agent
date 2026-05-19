package docker

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"
)

// Client wraps the Docker SDK client for agent operations.
type Client struct {
	cli *client.Client
}

// NewClient creates a new Docker client. If socketPath is non-empty and differs
// from the default, it uses the socket directly instead of modifying global env.
func NewClient(socketPath string) (*Client, error) {
	var opts []client.Opt
	opts = append(opts, client.FromEnv)
	if socketPath != "" && socketPath != "/var/run/docker.sock" {
		opts = append(opts, client.WithHost("unix://"+socketPath))
	}

	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &Client{cli: cli}, nil
}

// Close releases the Docker client resources.
func (c *Client) Close() error {
	return c.cli.Close()
}

// Ping checks if the Docker daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.cli.Ping(ctx, client.PingOptions{})
	return err
}
