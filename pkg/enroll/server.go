package enroll

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// ServerConfig configures the enrollment HTTP server.
type ServerConfig struct {
	// ListenAddr is the address to listen on (e.g., ":8443").
	ListenAddr string

	// TLSCert is the path to the server TLS certificate.
	TLSCert string

	// TLSKey is the path to the server TLS private key.
	TLSKey string

	// GetCertificate, when set, supplies the server certificate dynamically
	// on every TLS handshake (embedded-CA mode: the master self-issues the
	// enrollment leaf and renews it in-process, swapping the returned
	// certificate without a restart). When set it takes precedence over
	// TLSCert/TLSKey, so those may be empty.
	GetCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	// Handler is the enrollment HTTP handler.
	Handler *Handler

	// ExtraRoutes allows callers to register additional routes on the same
	// HTTPS listener after enrollment routes are mounted.
	ExtraRoutes func(*http.ServeMux)

	// MaxStreamDuration is the maximum time an SSE stream can stay open.
	// Defaults to 30 minutes.
	MaxStreamDuration time.Duration

	// Logger is the structured logger.
	Logger *slog.Logger
}

// Server runs the enrollment HTTP API.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// NewServer creates an enrollment HTTP server.
// TLS is mandatory — the enrollment API must never be served over plaintext
// because it handles credential delivery to unauthenticated peels.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.GetCertificate == nil && (cfg.TLSCert == "" || cfg.TLSKey == "") {
		return nil, fmt.Errorf("enroll: TLS certificate and key (or GetCertificate) are required; the enrollment API must not run without TLS")
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8443"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxStreamDuration == 0 {
		cfg.MaxStreamDuration = 30 * time.Minute
	}

	cfg.Handler.maxStreamDuration = cfg.MaxStreamDuration

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if cfg.GetCertificate != nil {
		tlsConfig.GetCertificate = cfg.GetCertificate
	} else {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("enroll: load TLS cert: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	mux := http.NewServeMux()
	cfg.Handler.RegisterRoutes(mux)
	if cfg.ExtraRoutes != nil {
		cfg.ExtraRoutes(mux)
	}

	// Wrap with middleware. The strict enrollment rate limit (burst 10,
	// 1 req/10s) protects the unauthenticated enrollment endpoints; routes
	// registered via ExtraRoutes (the token-authenticated REST API) get a
	// far higher budget so dispatch-and-poll clients aren't locked out.
	strictLimited := RateLimitMiddleware(mux, cfg.Logger)
	apiLimited := RateLimitMiddlewareWithConfig(mux, cfg.Logger, RateLimitConfig{
		Capacity:     120,
		RefillPerSec: 20,
	})
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isEnrollPath(r.URL.Path) {
			strictLimited.ServeHTTP(w, r)
			return
		}
		apiLimited.ServeHTTP(w, r)
	})
	handler = securityHeaders(handler)

	httpServer := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
		TLSConfig:    tlsConfig,
	}

	return &Server{
		httpServer: httpServer,
		logger:     cfg.Logger,
	}, nil
}

// isEnrollPath reports whether the request path belongs to the strictly
// rate-limited enrollment API. Note /api/v1/enrollments (the REST admin
// route) is NOT an enrollment path — only /api/v1/enroll and its subpaths
// are. The bootstrap route /api/v1/enroll/ca is EXCLUDED: it serves only
// public, cacheable material (CA bundle + NATS endpoints) and is fetched by
// every peel at boot — often many behind one NAT during provisioning — so
// it uses the relaxed bucket to avoid throttling fleet bring-up.
func isEnrollPath(path string) bool {
	if path == "/api/v1/enroll/ca" {
		return false
	}
	return path == "/api/v1/enroll" || strings.HasPrefix(path, "/api/v1/enroll/")
}

// Start begins listening for enrollment requests over TLS. It blocks
// until the server is shut down or an error occurs.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("enroll: listen %s: %w", s.httpServer.Addr, err)
	}

	tlsLn := tls.NewListener(ln, s.httpServer.TLSConfig)
	s.logger.Info("enrollment HTTPS server started", "addr", s.httpServer.Addr)
	return s.httpServer.Serve(tlsLn)
}

// Shutdown gracefully stops the enrollment HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down enrollment HTTP server")
	return s.httpServer.Shutdown(ctx)
}
