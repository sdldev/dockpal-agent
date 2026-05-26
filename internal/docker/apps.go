package docker

import (
	"context"
	"fmt"
	"sort"

	"github.com/moby/moby/client"
)

// AppSummary is the response shape used by GET /agent/docker/apps.
// It mirrors Dockpal server JSON so the controller can consume remote
// instance app summaries through the same instance-scoped API.
type AppSummary struct {
	Name       string              `json:"name"`
	InstanceID string              `json:"instance_id,omitempty"`
	Services   []AppServiceSummary `json:"services"`
	AutoUpdate bool                `json:"auto_update"`
	HasUpdate  bool                `json:"has_update"`
	LastUpdate any                 `json:"last_update,omitempty"`
}

// AppServiceSummary describes one service inside a compose project.
type AppServiceSummary struct {
	Name         string `json:"name"`
	Image        string `json:"image"`
	State        string `json:"state"`
	HasUpdate    bool   `json:"has_update"`
	LocalDigest  string `json:"local_digest,omitempty"`
	RemoteDigest string `json:"remote_digest,omitempty"`
}

// ListContainersWithLabel returns all containers, including stopped ones, that
// carry a label key. Docker label filters with just the key match any value.
func (c *Client) ListContainersWithLabel(ctx context.Context, label string) ([]ContainerInfo, error) {
	filters := make(client.Filters)
	filters = filters.Add("label", label)

	result, err := c.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers with label %q: %w", label, err)
	}

	containers := make([]ContainerInfo, len(result.Items))
	for i, ctr := range result.Items {
		name := ""
		if len(ctr.Names) > 0 {
			name = ctr.Names[0]
			if len(name) > 0 && name[0] == '/' {
				name = name[1:]
			}
		}
		containers[i] = ContainerInfo{
			ID:      truncateID(ctr.ID, 12),
			Name:    name,
			Image:   ctr.Image,
			Status:  ctr.Status,
			State:   string(ctr.State),
			Ports:   ctr.Ports,
			Created: ctr.Created,
			Labels:  ctr.Labels,
		}
	}
	return containers, nil
}

// ListApps groups containers carrying dockpal.project into one summary per
// compose project. The agent does not persist update attempts and does not own
// the controller's ImageUpdateMonitor, so update-related fields are zero-valued.
func (c *Client) ListApps(ctx context.Context) ([]AppSummary, error) {
	containers, err := c.ListContainersWithLabel(ctx, "dockpal.project")
	if err != nil {
		return nil, err
	}
	composeContainers, err := c.ListContainersWithLabel(ctx, "com.docker.compose.project")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(containers)+len(composeContainers))
	merged := make([]ContainerInfo, 0, len(containers)+len(composeContainers))
	for _, ctr := range containers {
		if ctr.ID == "" || seen[ctr.ID] {
			continue
		}
		seen[ctr.ID] = true
		merged = append(merged, ctr)
	}
	for _, ctr := range composeContainers {
		if ctr.ID == "" || seen[ctr.ID] {
			continue
		}
		seen[ctr.ID] = true
		merged = append(merged, ctr)
	}
	containers = merged

	type projectAccum struct {
		name       string
		autoUpdate bool
		services   map[string]AppServiceSummary
	}

	projects := make(map[string]*projectAccum)
	for _, ctr := range containers {
		project := ctr.Labels["dockpal.project"]
		if project == "" {
			project = ctr.Labels["com.docker.compose.project"]
		}
		if project == "" {
			continue
		}

		acc, ok := projects[project]
		if !ok {
			acc = &projectAccum{name: project, services: make(map[string]AppServiceSummary)}
			projects[project] = acc
		}
		if ctr.Labels["dockpal.auto-update"] == "true" {
			acc.autoUpdate = true
		}

		serviceName := ctr.Labels["dockpal.service"]
		if serviceName == "" {
			serviceName = ctr.Labels["com.docker.compose.service"]
		}
		if serviceName == "" {
			serviceName = ctr.Name
		}
		if _, exists := acc.services[serviceName]; exists {
			continue
		}
		acc.services[serviceName] = AppServiceSummary{
			Name:  serviceName,
			Image: ctr.Image,
			State: ctr.State,
		}
	}

	apps := make([]AppSummary, 0, len(projects))
	for _, acc := range projects {
		services := make([]AppServiceSummary, 0, len(acc.services))
		for _, svc := range acc.services {
			services = append(services, svc)
		}
		sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
		apps = append(apps, AppSummary{
			Name:       acc.name,
			Services:   services,
			AutoUpdate: acc.autoUpdate,
		})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}
