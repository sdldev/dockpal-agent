package docker

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/client"
)

// ListImages returns all images on the host.
func (c *Client) ListImages(ctx context.Context) ([]ImageInfo, error) {
	result, err := c.cli.ImageList(ctx, client.ImageListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	images := make([]ImageInfo, 0, len(result.Items))
	for _, img := range result.Items {
		repo := "<none>"
		tag := "<none>"
		if len(img.RepoTags) > 0 && img.RepoTags[0] != "<none>:<none>" {
			parts := strings.SplitN(img.RepoTags[0], ":", 2)
			repo = parts[0]
			if len(parts) > 1 {
				tag = parts[1]
			}
		}
		images = append(images, ImageInfo{
			ID:      truncateID(img.ID, 16),
			Repo:    repo,
			Tag:     tag,
			Size:    formatSize(img.Size),
			Created: fmt.Sprintf("%d", img.Created),
		})
	}
	return images, nil
}

// PullImage pulls an image without authentication.
func (c *Client) PullImage(ctx context.Context, image string) error {
	reader, err := c.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer reader.Close()
	io.Copy(io.Discard, reader)
	return nil
}

// PullImageWithAuth pulls an image with optional registry authentication.
func (c *Client) PullImageWithAuth(ctx context.Context, image string, registryAuth string) error {
	opts := client.ImagePullOptions{}
	if registryAuth != "" {
		opts.RegistryAuth = registryAuth
	}
	reader, err := c.cli.ImagePull(ctx, image, opts)
	if err != nil {
		errMsg := err.Error()
		if registryAuth != "" && (strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403") ||
			strings.Contains(errMsg, "unauthorized") || strings.Contains(errMsg, "denied")) {
			domain := ExtractImageDomain(image)
			return fmt.Errorf("authentication failed for %s — credentials may be expired: %w", domain, err)
		}
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer reader.Close()
	io.Copy(io.Discard, reader)
	return nil
}

// ExtractImageDomain extracts the registry domain from an image reference.
// Returns "registry" as fallback for Docker Hub images (no dot in first segment).
func ExtractImageDomain(image string) string {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) >= 2 && strings.Contains(parts[0], ".") {
		return parts[0]
	}
	return "registry"
}

// RemoveImage removes an image by ID.
func (c *Client) RemoveImage(ctx context.Context, id string) error {
	_, err := c.cli.ImageRemove(ctx, id, client.ImageRemoveOptions{Force: true})
	return err
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}
