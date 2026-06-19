package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"gopkg.in/yaml.v3"
)

// composeBasePath is the directory for storing compose files on the agent.
// Different from the Server's path to avoid conflicts if both run on the same host.
const composeBasePath = "/opt/dockpal-agent/compose"

var projectNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// ValidateProjectName ensures a project name is safe for use in file paths and container names.
func ValidateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("project name is required")
	}
	if !projectNameRegex.MatchString(name) {
		return fmt.Errorf("invalid project name: must be alphanumeric with dashes/underscores, max 64 chars")
	}
	return nil
}

// ComposeFile represents a parsed docker-compose YAML file.
type ComposeFile struct {
	Version  string                    `yaml:"version,omitempty"`
	Services map[string]ComposeService `yaml:"services"`
}

// ComposeService represents a single service definition in docker-compose.
type ComposeService struct {
	Image       string   `yaml:"image"`
	Ports       []string `yaml:"ports,omitempty"`
	Volumes     []string `yaml:"volumes,omitempty"`
	Environment any      `yaml:"environment,omitempty"`
	Networks    []string `yaml:"networks,omitempty"`
	DependsOn   any      `yaml:"depends_on,omitempty"`
	Restart     string   `yaml:"restart,omitempty"`
	Labels      any      `yaml:"labels,omitempty"`
	Command     any      `yaml:"command,omitempty"`
	Entrypoint  any      `yaml:"entrypoint,omitempty"`
}

// PortBinding represents a parsed port mapping.
type PortBinding struct {
	HostPort      int
	ContainerPort int
	Protocol      string
}

// VolumeMount represents a parsed volume mount.
type VolumeMount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// ParseComposeFile parses a docker-compose YAML string into a ComposeFile struct.
func ParseComposeFile(yamlContent string) (*ComposeFile, error) {
	var cf ComposeFile
	if err := yaml.Unmarshal([]byte(yamlContent), &cf); err != nil {
		return nil, fmt.Errorf("invalid compose YAML: %w", err)
	}
	if len(cf.Services) == 0 {
		return nil, fmt.Errorf("no services defined in compose file")
	}
	return &cf, nil
}

// ParsePort parses a port specification string into a PortBinding.
func ParsePort(spec string) (PortBinding, error) {
	pb := PortBinding{Protocol: "tcp"}

	if idx := strings.LastIndex(spec, "/"); idx != -1 {
		pb.Protocol = spec[idx+1:]
		spec = spec[:idx]
	}

	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 1:
		port, err := strconv.Atoi(parts[0])
		if err != nil {
			return pb, fmt.Errorf("invalid port: %s", spec)
		}
		pb.ContainerPort = port
		pb.HostPort = port
	case 2:
		host, err := strconv.Atoi(parts[0])
		if err != nil {
			return pb, fmt.Errorf("invalid host port: %s", parts[0])
		}
		cport, err := strconv.Atoi(parts[1])
		if err != nil {
			return pb, fmt.Errorf("invalid container port: %s", parts[1])
		}
		pb.HostPort = host
		pb.ContainerPort = cport
	case 3:
		host, err := strconv.Atoi(parts[1])
		if err != nil {
			return pb, fmt.Errorf("invalid host port: %s", parts[1])
		}
		cport, err := strconv.Atoi(parts[2])
		if err != nil {
			return pb, fmt.Errorf("invalid container port: %s", parts[2])
		}
		pb.HostPort = host
		pb.ContainerPort = cport
	default:
		return pb, fmt.Errorf("invalid port format: %s", spec)
	}
	return pb, nil
}

// ParseVolume parses a volume specification string into a VolumeMount.
func ParseVolume(spec string) (VolumeMount, error) {
	vm := VolumeMount{}
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 1:
		vm.ContainerPath = parts[0]
	case 2:
		vm.HostPath = parts[0]
		vm.ContainerPath = parts[1]
	case 3:
		vm.HostPath = parts[0]
		vm.ContainerPath = parts[1]
		vm.ReadOnly = parts[2] == "ro"
	default:
		return vm, fmt.Errorf("invalid volume format: %s", spec)
	}
	return vm, nil
}

// ParseEnvironment converts environment variable definitions into a string slice.
func ParseEnvironment(env any) []string {
	switch v := env.(type) {
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case map[string]any:
		result := make([]string, 0, len(v))
		for key, val := range v {
			if val == nil {
				result = append(result, key)
			} else {
				result = append(result, fmt.Sprintf("%s=%v", key, val))
			}
		}
		return result
	default:
		return nil
	}
}

// ResolveStartOrder performs a topological sort on services based on depends_on.
func ResolveStartOrder(cf *ComposeFile) ([]string, error) {
	deps := make(map[string][]string)
	for name, svc := range cf.Services {
		deps[name] = parseDependsOn(svc.DependsOn)
	}
	return topologicalSort(deps)
}

func parseDependsOn(dep any) []string {
	switch v := dep.(type) {
	case []any:
		result := make([]string, 0, len(v))
		for _, d := range v {
			if s, ok := d.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case map[string]any:
		result := make([]string, 0, len(v))
		for name := range v {
			result = append(result, name)
		}
		return result
	default:
		return nil
	}
}

func topologicalSort(deps map[string][]string) ([]string, error) {
	visited := make(map[string]bool)
	inStack := make(map[string]bool)
	var order []string

	var visit func(string) error
	visit = func(node string) error {
		if inStack[node] {
			return fmt.Errorf("circular dependency detected at %s", node)
		}
		if visited[node] {
			return nil
		}
		inStack[node] = true
		for _, dep := range deps[node] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		inStack[node] = false
		visited[node] = true
		order = append(order, node)
		return nil
	}

	for name := range deps {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func parseLabels(labels any) map[string]string {
	result := make(map[string]string)
	switch v := labels.(type) {
	case map[string]any:
		for key, val := range v {
			result[key] = fmt.Sprintf("%v", val)
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				parts := strings.SplitN(s, "=", 2)
				if len(parts) == 2 {
					result[parts[0]] = parts[1]
				} else {
					result[parts[0]] = ""
				}
			}
		}
	}
	return result
}

func (c *Client) pullImageIfNeeded(ctx context.Context, image string, registryAuth string) error {
	_, err := c.cli.ImageInspect(ctx, image)
	if err == nil {
		return nil
	}
	if registryAuth != "" {
		return c.PullImageWithAuth(ctx, image, registryAuth)
	}
	return c.PullImage(ctx, image)
}

func writeComposeFile(projectName, composeYAML string) error {
	composeDir := filepath.Join(composeBasePath, projectName)
	if err := os.MkdirAll(composeDir, 0755); err != nil {
		return fmt.Errorf("failed to create compose directory: %w", err)
	}
	composeFilePath := filepath.Join(composeDir, "docker-compose.yml")
	if err := os.WriteFile(composeFilePath, []byte(composeYAML), 0644); err != nil {
		return fmt.Errorf("failed to write compose file: %w", err)
	}
	return nil
}

func (c *Client) createAndStartService(ctx context.Context, projectName, svcName string, svc ComposeService, cf *ComposeFile) error {
	composeFilePath := filepath.Join(composeBasePath, projectName, "docker-compose.yml")
	baseLabels := map[string]string{
		"dockpal.managed": "true",
		"dockpal.project": projectName,
		"dockpal.compose": composeFilePath,
	}

	svcLabels := make(map[string]string)
	for k, v := range baseLabels {
		svcLabels[k] = v
	}
	svcLabels["dockpal.service"] = svcName
	userLabels := parseLabels(svc.Labels)
	for k, v := range userLabels {
		svcLabels[k] = v
	}

	envVars := ParseEnvironment(svc.Environment)

	portBindings := make(network.PortMap)
	exposedPorts := make(network.PortSet)
	for _, portSpec := range svc.Ports {
		pb, err := ParsePort(portSpec)
		if err != nil {
			return fmt.Errorf("service %s: %w", svcName, err)
		}
		portKey := network.MustParsePort(fmt.Sprintf("%d/%s", pb.ContainerPort, pb.Protocol))
		exposedPorts[portKey] = struct{}{}
		portBindings[portKey] = append(portBindings[portKey], network.PortBinding{
			HostPort: strconv.Itoa(pb.HostPort),
		})
	}

	var binds []string
	for _, volSpec := range svc.Volumes {
		vm, err := ParseVolume(volSpec)
		if err != nil {
			return fmt.Errorf("service %s: %w", svcName, err)
		}
		bind := vm.HostPath + ":" + vm.ContainerPath
		if vm.ReadOnly {
			bind += ":ro"
		}
		if vm.HostPath != "" {
			binds = append(binds, bind)
		}
	}

	restartPolicy := "unless-stopped"
	if svc.Restart != "" {
		restartPolicy = svc.Restart
	}

	// Resolve command: supports both string (shell form) and []any (exec form)
	var cmd []string
	switch v := svc.Command.(type) {
	case string:
		cmd = []string{"/bin/sh", "-c", v}
	case []any:
		cmd = make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				cmd = append(cmd, s)
			}
		}
	}

	// Resolve entrypoint: supports both string and []any
	var entrypoint []string
	switch v := svc.Entrypoint.(type) {
	case string:
		entrypoint = []string{"/bin/sh", "-c", v}
	case []any:
		entrypoint = make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				entrypoint = append(entrypoint, s)
			}
		}
	}

	containerConfig := &container.Config{
		Labels:       svcLabels,
		Env:          envVars,
		ExposedPorts: exposedPorts,
		Image:        svc.Image,
	}
	if len(cmd) > 0 {
		containerConfig.Cmd = cmd
	}
	if len(entrypoint) > 0 {
		containerConfig.Entrypoint = entrypoint
	}

	hostConfig := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(restartPolicy)},
		PortBindings:  portBindings,
		Binds:         binds,
	}

	var networkConfig *network.NetworkingConfig
	if len(svc.Networks) > 0 {
		endpointsConfig := make(map[string]*network.EndpointSettings)
		for _, netName := range svc.Networks {
			endpointsConfig[netName] = &network.EndpointSettings{}
		}
		networkConfig = &network.NetworkingConfig{
			EndpointsConfig: endpointsConfig,
		}
	}

	containerName := fmt.Sprintf("%s_%s", projectName, svcName)

	createOpts := client.ContainerCreateOptions{
		Name:             containerName,
		Config:           containerConfig,
		HostConfig:       hostConfig,
		NetworkingConfig: networkConfig,
	}

	result, err := c.cli.ContainerCreate(ctx, createOpts)
	if err != nil {
		return fmt.Errorf("failed to create container for %s: %w", svcName, err)
	}

	if _, err := c.cli.ContainerStart(ctx, result.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("failed to start container %s: %w", svcName, err)
	}
	return nil
}

// DeployCompose deploys services defined in a docker-compose YAML string.
func (c *Client) DeployCompose(ctx context.Context, projectName, composeYAML string, getAuthHeader AuthHeaderFunc) error {
	if err := ValidateProjectName(projectName); err != nil {
		return err
	}
	if err := writeComposeFile(projectName, composeYAML); err != nil {
		return err
	}

	cf, err := ParseComposeFile(composeYAML)
	if err != nil {
		return fmt.Errorf("failed to parse compose: %w", err)
	}

	startOrder, err := ResolveStartOrder(cf)
	if err != nil {
		return fmt.Errorf("failed to resolve service start order: %w", err)
	}

	for _, svcName := range startOrder {
		svc := cf.Services[svcName]
		registryAuth := ""
		if getAuthHeader != nil {
			auth, err := getAuthHeader(svc.Image)
			if err == nil {
				registryAuth = auth
			}
		}
		if err := c.pullImageIfNeeded(ctx, svc.Image, registryAuth); err != nil {
			return fmt.Errorf("failed to pull image for %s: %w", svcName, err)
		}
		if err := c.createAndStartService(ctx, projectName, svcName, svc, cf); err != nil {
			return err
		}
	}

	return nil
}

// StopCompose stops all containers belonging to a compose project.
func (c *Client) StopCompose(ctx context.Context, projectName string) error {
	if err := ValidateProjectName(projectName); err != nil {
		return err
	}
	f := make(client.Filters)
	f = f.Add("label", fmt.Sprintf("dockpal.project=%s", projectName))
	result, err := c.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return err
	}

	timeout := 10
	for _, ctr := range result.Items {
		c.cli.ContainerStop(ctx, ctr.ID, client.ContainerStopOptions{Timeout: &timeout})
	}

	return nil
}

// RemoveCompose removes all containers and files belonging to a compose project.
func (c *Client) RemoveCompose(ctx context.Context, projectName string) error {
	if err := ValidateProjectName(projectName); err != nil {
		return err
	}
	f := make(client.Filters)
	f = f.Add("label", fmt.Sprintf("dockpal.project=%s", projectName))
	result, err := c.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return err
	}

	for _, ctr := range result.Items {
		c.cli.ContainerRemove(ctx, ctr.ID, client.ContainerRemoveOptions{Force: true})
	}

	composeDir := filepath.Join(composeBasePath, projectName)
	os.RemoveAll(composeDir)

	return nil
}
