// Package server is the HTTP face of the service: routing, middleware and the health endpoint.
// Feature packages (analytics, admin, leads…) register their own routes on the mux.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/DenisHumen/krokosha-site/api/internal/cache"
	"github.com/DenisHumen/krokosha-site/api/internal/config"
)

// Deps are the long-lived objects handlers work with.
type Deps struct {
	Env     *config.Env
	DB      *sql.DB
	Cache   *cache.Cache
	Log     *slog.Logger
	Version string
	Started time.Time
}

// Server wraps http.Server with the project's defaults.
type Server struct {
	deps Deps
	mux  *http.ServeMux
	http *http.Server
}

// New builds the server with the routes every installation has.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})

	handler := chain(s.mux,
		recoverPanics(deps.Log),
		requestContext,
		securityHeaders,
		accessLog(deps.Log),
	)
	s.http = &http.Server{
		Addr:              deps.Env.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// No WriteTimeout: the admin's live feed is a long-lived response (SSE).
		// Slow readers are nginx's problem, and it has its own timeouts.
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 16 << 10,
		ErrorLog:       slog.NewLogLogger(deps.Log.Handler(), slog.LevelWarn),
	}
	return s
}

// Mux lets feature packages add their routes before the server starts.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Handler is the full middleware stack (tests drive it with httptest).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.deps.Env.Listen)
	if err != nil {
		return err
	}
	s.deps.Log.Info("listening", "addr", listener.Addr().String(), "version", s.deps.Version)

	failed := make(chan error, 1)
	go func() {
		if err := s.http.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	s.deps.Log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.http.Shutdown(shutdownCtx)
}

type health struct {
	Status  string            `json:"status"`
	Version string            `json:"version,omitempty"`
	Uptime  int64             `json:"uptime_s,omitempty"`
	Checks  map[string]string `json:"checks,omitempty"`
}

// handleHealth answers "ok" with 200 while the database is reachable. Redis is optional, so its
// outage only degrades. Details are for the server itself (install.sh, monitoring on localhost);
// visitors coming through nginx get the bare status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{"mysql": "ok", "redis": "ok"}
	status, code := "ok", http.StatusOK
	if err := s.deps.DB.PingContext(ctx); err != nil {
		checks["mysql"] = "down"
		status, code = "down", http.StatusServiceUnavailable
	}
	switch err := s.deps.Cache.Ping(ctx); {
	case errors.Is(err, cache.ErrDisabled):
		checks["redis"] = "disabled"
	case err != nil:
		checks["redis"] = "down"
		if status == "ok" {
			status = "degraded"
		}
	}

	answer := health{Status: status}
	if !ThroughProxy(r) {
		answer.Version = s.deps.Version
		answer.Uptime = int64(time.Since(s.deps.Started).Seconds())
		answer.Checks = checks
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, code, answer)
}

// WriteJSON sends v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
