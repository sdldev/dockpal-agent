package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sdldev/dockpal-agent/internal/config"
	"github.com/sdldev/dockpal-agent/internal/docker"
)

// Server is the direct mode HTTP server.
type Server struct {
	cfg       *config.Config
	docker    *docker.Client
	router    *chi.Mux
	httpSrv   *http.Server
	deployMgr *docker.DeployManager
}

// New creates a new direct mode server.
func New(cfg *config.Config, dockerClient *docker.Client) *Server {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)

	srv := &Server{
		cfg:       cfg,
		docker:    dockerClient,
		router:    router,
		deployMgr: docker.NewDeployManager(),
	}

	srv.registerRoutes()
	return srv
}

// Run starts the HTTP server (with optional TLS).
func (s *Server) Run() error {
	var listener net.Listener
	var err error

	if s.cfg.DirectTLS {
		cert, err := ensureCerts(s.cfg.TLSCertDir)
		if err != nil {
			return fmt.Errorf("tls cert error: %w", err)
		}
		listener, err = tls.Listen("tcp", s.cfg.DirectListen, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
		if err != nil {
			return fmt.Errorf("tls listen error: %w", err)
		}
	} else {
		listener, err = net.Listen("tcp", s.cfg.DirectListen)
		if err != nil {
			return fmt.Errorf("listen error: %w", err)
		}
	}

	s.httpSrv = &http.Server{
		Handler:      s.router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("Agent listening on %s (TLS: %v)", s.cfg.DirectListen, s.cfg.DirectTLS)
	return s.httpSrv.Serve(listener)
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv != nil {
		return s.httpSrv.Shutdown(ctx)
	}
	return nil
}

// ensureCerts loads or auto-generates a self-signed TLS certificate.
func ensureCerts(certDir string) (tls.Certificate, error) {
	if certDir == "" {
		certDir = "/etc/dockpal/agent/certs"
	}

	certFile := filepath.Join(certDir, "agent.crt")
	keyFile := filepath.Join(certDir, "agent.key")

	if _, err := os.Stat(certFile); err == nil {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}

	if err := os.MkdirAll(certDir, 0700); err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to create cert dir: %w", err)
	}

	hostname, _ := os.Hostname()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to generate key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: hostname,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{hostname},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to create certificate: %w", err)
	}

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to marshal key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to write cert: %w", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to write key: %w", err)
	}

	log.Printf("Auto-generated self-signed TLS certificate in %s", certDir)
	return tls.X509KeyPair(certPEM, keyPEM)
}
