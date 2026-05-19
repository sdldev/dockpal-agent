package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	apinetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// ListContainers returns all containers, optionally including stopped ones.
func (c *Client) ListContainers(ctx context.Context, all bool) ([]ContainerInfo, error) {
	result, err := c.cli.ContainerList(ctx, client.ContainerListOptions{All: all})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
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
		}
	}
	return containers, nil
}

// InspectContainer returns detailed information about a container.
func (c *Client) InspectContainer(ctx context.Context, id string) (*ContainerDetail, error) {
	result, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	ctr := result.Container
	name := ctr.Name
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}

	networks := make(map[string]string)
	if ctr.NetworkSettings != nil {
		for netName, ep := range ctr.NetworkSettings.Networks {
			networks[netName] = ep.IPAddress.String()
		}
	}

	status := ""
	if ctr.State != nil {
		status = string(ctr.State.Status)
	}

	networkMode := ""
	restartPolicy := ""
	var memoryLimit int64
	var nanoCPUs int64
	if ctr.HostConfig != nil {
		networkMode = string(ctr.HostConfig.NetworkMode)
		restartPolicy = string(ctr.HostConfig.RestartPolicy.Name)
		memoryLimit = ctr.HostConfig.Memory
		nanoCPUs = ctr.HostConfig.NanoCPUs
	}

	var created int64
	if ctr.Created != "" {
		if t, err := time.Parse(time.RFC3339Nano, ctr.Created); err == nil {
			created = t.Unix()
		}
	}

	info := &ContainerDetail{
		ContainerInfo: ContainerInfo{
			ID:      truncateID(ctr.ID, 12),
			Name:    name,
			Image:   ctr.Config.Image,
			Status:  status,
			State:   status,
			Ports:   []container.PortSummary{},
			Created: created,
		},
		Platform:      ctr.Platform,
		Env:           ctr.Config.Env,
		Mounts:        ctr.Mounts,
		NetworkMode:   networkMode,
		RestartPolicy: restartPolicy,
		Networks:      networks,
		MemoryLimit:   memoryLimit,
		NanoCPUs:      nanoCPUs,
	}

	return info, nil
}

// StartContainer starts a container.
func (c *Client) StartContainer(ctx context.Context, id string) error {
	_, err := c.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

// StopContainer gracefully stops a container with a 10-second timeout.
func (c *Client) StopContainer(ctx context.Context, id string) error {
	timeout := 10
	_, err := c.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	return err
}

// RestartContainer restarts a container with a 10-second timeout.
func (c *Client) RestartContainer(ctx context.Context, id string) error {
	timeout := 10
	_, err := c.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &timeout})
	return err
}

// RemoveContainer stops (if running) and removes a container.
// When force is true, it kills the container immediately.
func (c *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	if force {
		_, err := c.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
		return err
	}
	timeout := 10
	c.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	_, err := c.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{})
	return err
}

// ContainerLogs returns a reader for container log output.
func (c *Client) ContainerLogs(ctx context.Context, id string, tail string) (io.ReadCloser, error) {
	result, err := c.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tail,
		Follow:     true,
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ServerVersion returns the Docker daemon version string.
func (c *Client) ServerVersion(ctx context.Context) (string, error) {
	ver, err := c.cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get docker version: %w", err)
	}
	return ver.Version, nil
}

// GetContainerStats returns a snapshot of container resource usage.
func (c *Client) GetContainerStats(ctx context.Context, id string) (*ContainerStats, error) {
	result, err := c.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: false})
	if err != nil {
		return nil, fmt.Errorf("failed to get container stats: %w", err)
	}
	defer result.Body.Close()

	var v container.StatsResponse
	if err := json.NewDecoder(result.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("failed to decode stats: %w", err)
	}

	stats := &ContainerStats{}

	cpuDelta := v.CPUStats.CPUUsage.TotalUsage - v.PreCPUStats.CPUUsage.TotalUsage
	systemDelta := v.CPUStats.SystemUsage - v.PreCPUStats.SystemUsage
	if systemDelta > 0 && cpuDelta > 0 {
		numCPU := len(v.CPUStats.CPUUsage.PercpuUsage)
		if numCPU == 0 {
			numCPU = 1
		}
		stats.CPUPercent = (float64(cpuDelta) / float64(systemDelta)) * float64(numCPU) * 100.0
	}

	stats.MemoryUsage = v.MemoryStats.Usage
	stats.MemoryLimit = v.MemoryStats.Limit
	if stats.MemoryLimit > 0 {
		stats.MemoryPercent = float64(stats.MemoryUsage) / float64(stats.MemoryLimit) * 100.0
	}

	if v.Networks != nil {
		for _, net := range v.Networks {
			stats.NetworkRx += net.RxBytes
			stats.NetworkTx += net.TxBytes
		}
	}

	return stats, nil
}

// truncateID safely truncates an ID to the given length.
func truncateID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// RenameContainer renames a container.
func (c *Client) RenameContainer(ctx context.Context, id, newName string) error {
	_, err := c.cli.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: newName})
	return err
}

// UpdateContainer applies in-place updates (restart policy, resource limits).
func (c *Client) UpdateContainer(ctx context.Context, id string, opts client.ContainerUpdateOptions) error {
	_, err := c.cli.ContainerUpdate(ctx, id, opts)
	return err
}

// EditContainer applies edits to a container. In-place fields (name, restart_policy,
// memory_limit, cpu_limit) are applied without recreation. Recreate fields (image,
// env, ports, volumes) require stopping and recreating the container.
func (c *Client) EditContainer(ctx context.Context, id string, req ContainerEditRequest) (*ContainerDetail, error) {
	preInspect, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}
	fullID := preInspect.Container.ID
	containerName := preInspect.Container.Name
	if len(containerName) > 0 && containerName[0] == '/' {
		containerName = containerName[1:]
	}

	needsRecreate := req.Image != nil || req.Env != nil || req.Ports != nil || req.Volumes != nil

	// Apply in-place updates first
	if req.Name != nil {
		if err := c.RenameContainer(ctx, fullID, *req.Name); err != nil {
			return nil, fmt.Errorf("failed to rename container: %w", err)
		}
		containerName = *req.Name
	}

	if req.RestartPolicy != nil || req.MemoryLimit != nil || req.CPULimit != nil {
		updateOpts := client.ContainerUpdateOptions{}
		if req.RestartPolicy != nil {
			updateOpts.RestartPolicy = &container.RestartPolicy{Name: container.RestartPolicyMode(*req.RestartPolicy)}
		}
		if req.MemoryLimit != nil || req.CPULimit != nil {
			resources := container.Resources{}
			if req.MemoryLimit != nil {
				resources.Memory = *req.MemoryLimit
				if *req.MemoryLimit > 0 {
					resources.MemorySwap = -1
				} else {
					resources.MemorySwap = 0
				}
			}
			if req.CPULimit != nil {
				resources.NanoCPUs = int64(*req.CPULimit * 1e9)
			}
			updateOpts.Resources = &resources
		}
		if err := c.UpdateContainer(ctx, fullID, updateOpts); err != nil {
			return nil, fmt.Errorf("failed to update container: %w", err)
		}
	}

	if needsRecreate {
		if err := c.recreateContainer(ctx, fullID, req); err != nil {
			return nil, fmt.Errorf("failed to recreate container: %w", err)
		}
	}

	return c.InspectContainer(ctx, containerName)
}

// recreateContainer stops, removes, and recreates a container with merged config.
func (c *Client) recreateContainer(ctx context.Context, id string, req ContainerEditRequest) error {
	result, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("failed to inspect container for recreation: %w", err)
	}

	ctr := result.Container
	if ctr.Config == nil || ctr.HostConfig == nil {
		return fmt.Errorf("container inspect returned incomplete data")
	}

	name := ctr.Name
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	if req.Name != nil {
		name = *req.Name
	}

	env := ctr.Config.Env
	if req.Env != nil {
		env = *req.Env
	}

	image := ctr.Config.Image
	if req.Image != nil {
		image = *req.Image
	}

	portBindings := ctr.HostConfig.PortBindings
	exposedPorts := ctr.Config.ExposedPorts
	if req.Ports != nil {
		portBindings = apinetwork.PortMap{}
		exposedPorts = apinetwork.PortSet{}
		for _, pm := range *req.Ports {
			proto := pm.Protocol
			if proto == "" {
				proto = "tcp"
			}
			portKey, _ := apinetwork.ParsePort(fmt.Sprintf("%d/%s", pm.ContainerPort, proto))
			exposedPorts[portKey] = struct{}{}
			portBindings[portKey] = append(portBindings[portKey], apinetwork.PortBinding{
				HostIP:   netip.IPv4Unspecified(),
				HostPort: fmt.Sprintf("%d", pm.HostPort),
			})
		}
	}

	binds := ctr.HostConfig.Binds
	if req.Volumes != nil {
		binds = make([]string, 0, len(*req.Volumes))
		for _, vm := range *req.Volumes {
			bind := vm.HostPath + ":" + vm.ContainerPath
			if vm.ReadOnly {
				bind += ":ro"
			}
			binds = append(binds, bind)
		}
	}

	newConfig := &container.Config{
		Image:        image,
		Env:          env,
		ExposedPorts: exposedPorts,
		Labels:       ctr.Config.Labels,
		Cmd:          ctr.Config.Cmd,
		Entrypoint:   ctr.Config.Entrypoint,
		WorkingDir:   ctr.Config.WorkingDir,
		User:         ctr.Config.User,
		Tty:          ctr.Config.Tty,
		OpenStdin:    ctr.Config.OpenStdin,
		AttachStdin:  ctr.Config.AttachStdin,
		AttachStdout: ctr.Config.AttachStdout,
		AttachStderr: ctr.Config.AttachStderr,
	}

	restartPolicy := ctr.HostConfig.RestartPolicy
	if req.RestartPolicy != nil {
		restartPolicy = container.RestartPolicy{Name: container.RestartPolicyMode(*req.RestartPolicy)}
	}

	newHostConfig := &container.HostConfig{
		RestartPolicy: restartPolicy,
		PortBindings:  portBindings,
		Binds:         binds,
		NetworkMode:   ctr.HostConfig.NetworkMode,
		Privileged:    ctr.HostConfig.Privileged,
		CapAdd:        ctr.HostConfig.CapAdd,
		CapDrop:       ctr.HostConfig.CapDrop,
		ExtraHosts:    ctr.HostConfig.ExtraHosts,
	}

	if req.MemoryLimit != nil {
		newHostConfig.Memory = *req.MemoryLimit
		newHostConfig.MemorySwap = -1
	} else if ctr.HostConfig.Memory > 0 {
		newHostConfig.Memory = ctr.HostConfig.Memory
		if ctr.HostConfig.MemorySwap != 0 {
			newHostConfig.MemorySwap = ctr.HostConfig.MemorySwap
		} else {
			newHostConfig.MemorySwap = -1
		}
	}
	if req.CPULimit != nil {
		newHostConfig.NanoCPUs = int64(*req.CPULimit * 1e9)
	} else if ctr.HostConfig.NanoCPUs != 0 {
		newHostConfig.NanoCPUs = ctr.HostConfig.NanoCPUs
	}

	var networkConfig *apinetwork.NetworkingConfig
	if ctr.NetworkSettings != nil && len(ctr.NetworkSettings.Networks) > 0 {
		endpointsConfig := make(map[string]*apinetwork.EndpointSettings)
		for netName := range ctr.NetworkSettings.Networks {
			endpointsConfig[netName] = &apinetwork.EndpointSettings{}
		}
		networkConfig = &apinetwork.NetworkingConfig{
			EndpointsConfig: endpointsConfig,
		}
	}

	wasRunning := ctr.State != nil && strings.ToLower(string(ctr.State.Status)) == "running"

	if wasRunning {
		timeout := 10
		c.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	}
	removeOpts := client.ContainerRemoveOptions{
		RemoveVolumes: false,
		Force:         true,
	}
	if _, err := c.cli.ContainerRemove(ctx, id, removeOpts); err != nil {
		return fmt.Errorf("failed to remove old container: %w", err)
	}

	createOpts := client.ContainerCreateOptions{
		Name:             name,
		Config:           newConfig,
		HostConfig:       newHostConfig,
		NetworkingConfig: networkConfig,
	}
	createResult, err := c.cli.ContainerCreate(ctx, createOpts)
	if err != nil {
		return fmt.Errorf("failed to create new container: %w", err)
	}

	if wasRunning {
		if _, err := c.cli.ContainerStart(ctx, createResult.ID, client.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("container created but failed to start: %w", err)
		}
	}

	return nil
}
