package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"time"
)

// Server is a minimal wrapper around net/http.Server with sane production
// timeouts and a graceful Shutdown method. The bridge runs a single Server
// instance behind NewRouter.
type Server struct {
	srv    *stdhttp.Server
	logger *slog.Logger
	addr   string
}

// NewServer constructs a Server bound to addr (host:port). The handler is
// usually the Gin engine returned by NewRouter. A nil logger falls back to
// slog.Default(); an empty addr panics.
func NewServer(addr string, router stdhttp.Handler, logger *slog.Logger) *Server {
	if addr == "" {
		panic("http: NewServer requires non-empty addr")
	}
	if router == nil {
		panic("http: NewServer requires non-nil router")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		srv: &stdhttp.Server{
			Addr:              addr,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		logger: logger,
		addr:   addr,
	}
}

// Start begins serving on the configured address. It blocks until the
// underlying server stops. A graceful Shutdown returns nil; any other error
// is returned to the caller wrapped with context.
func (s *Server) Start() error {
	s.logger.Info("http server starting", "addr", s.addr)
	if err := s.srv.ListenAndServe(); err != nil {
		if errors.Is(err, stdhttp.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Shutdown attempts a graceful shutdown bounded by ctx. After ctx expires the
// underlying server forces remaining connections closed and returns the
// context error.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("http server shutdown")
	if err := s.srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("http server shutdown: %w", err)
	}
	return nil
}
