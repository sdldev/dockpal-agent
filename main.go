package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sdldev/dockpal-agent/internal/config"
	"github.com/sdldev/dockpal-agent/internal/docker"
	"github.com/sdldev/dockpal-agent/internal/edge"
	"github.com/sdldev/dockpal-agent/internal/server"
)

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("dockpal-agent v%s\n", config.Version)
			return
		case "healthcheck":
			os.Exit(runHealthcheck())
		case "help", "--help", "-h":
			fmt.Println("DockPal Agent — Lightweight Docker proxy for remote management")
			fmt.Println()
			fmt.Println("Configuration via environment variables. See README.")
			fmt.Println()
			fmt.Println("Commands:")
			fmt.Println("  version       Print agent version")
			fmt.Println("  healthcheck   Probe local /agent/ping (used by Docker HEALTHCHECK)")
			fmt.Println("  help          Show this help message")
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	dockerClient, err := docker.NewClient(cfg.DockerSocket)
	if err != nil {
		log.Fatalf("Docker client error: %v", err)
	}
	defer dockerClient.Close()

	if err := dockerClient.Ping(context.Background()); err != nil {
		log.Fatalf("Docker daemon unreachable: %v", err)
	}

	log.Printf("DockPal Agent v%s starting in %s mode", config.Version, cfg.Mode)

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()

	switch cfg.Mode {
	case "direct":
		runDirect(ctx, cfg, dockerClient)
	case "edge":
		runEdge(ctx, cfg, dockerClient)
	}
}

func runDirect(ctx context.Context, cfg *config.Config, dockerClient *docker.Client) {
	srv := server.New(cfg, dockerClient)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Run(); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		log.Fatalf("Server error: %v", err)
	}
}

// runHealthcheck probes the local /agent/ping endpoint, auto-detecting
// scheme (http vs https) from DOCKPAL_DIRECT_TLS and the listen address
// from DOCKPAL_DIRECT_LISTEN. Returns 0 on success, 1 otherwise.
func runHealthcheck() int {
	listen := os.Getenv("DOCKPAL_DIRECT_LISTEN")
	if listen == "" {
		listen = ":9273"
	}
	// Replace ":port" or "0.0.0.0:port" with localhost.
	host := "127.0.0.1"
	port := strings.TrimPrefix(listen, ":")
	if idx := strings.LastIndex(listen, ":"); idx >= 0 && idx != 0 {
		port = listen[idx+1:]
	}

	scheme := "https"
	if v := strings.ToLower(os.Getenv("DOCKPAL_DIRECT_TLS")); v == "false" || v == "0" || v == "no" {
		scheme = "http"
	}

	url := fmt.Sprintf("%s://%s:%s/agent/ping", scheme, host, port)

	client := &http.Client{
		Timeout: 4 * time.Second,
		Transport: &http.Transport{
			// Self-signed cert is expected in direct mode.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

func runEdge(ctx context.Context, cfg *config.Config, dockerClient *docker.Client) {
	client := edge.NewClient(cfg, dockerClient)

	errCh := make(chan error, 1)
	go func() {
		if err := client.Run(ctx); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("Edge client shutting down...")
	case err := <-errCh:
		log.Fatalf("Edge client error: %v", err)
	}
}
