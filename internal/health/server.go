// Package health provides a lightweight HTTP health check server
// for monitoring the sync service from Docker healthchecks,
// Uptime Kuma, Home Assistant, or similar systems.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// JSON field names and status values shared across the health endpoints.
// Centralizing them keeps the wire contract consistent in one place.
const (
	fieldStatus = "status"
	fieldError  = "error"
	statusOK    = "ok"
	statusError = "error"
)

// Status holds the current service health state.
type Status struct {
	mu sync.RWMutex

	StartedAt        time.Time
	LastSyncAt       time.Time
	LastSyncOK       bool
	LastErrorMessage string
	SyncCount        int
	CurrentStage     string
	TVStatuses       map[string]TVStatus
}

// TVStatus tracks per-TV health information.
type TVStatus struct {
	IP               string `json:"ip"`
	LastSeen         string `json:"last_seen"`
	ImageCount       int    `json:"image_count"`
	ArtMode          bool   `json:"art_mode"`
	Status           string `json:"status"` // "ok", "unreachable", "backoff"
	LastErrorMessage string `json:"last_error,omitempty"`
}

// NewStatus creates a new thread-safe health status tracker.
func NewStatus() *Status {
	return &Status{
		StartedAt:  time.Now(),
		TVStatuses: make(map[string]TVStatus),
	}
}

// RecordSync records the result of a sync cycle.
func (s *Status) RecordSync(ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastSyncAt = time.Now()
	s.LastSyncOK = ok
	s.SyncCount++
	if err != nil {
		s.LastErrorMessage = err.Error()
	} else {
		s.LastErrorMessage = ""
	}
}

// SetStage updates the current operation stage.
func (s *Status) SetStage(stage string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CurrentStage = stage
}

// SetTVStatus updates the status for a specific TV.
func (s *Status) SetTVStatus(ip string, status TVStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TVStatuses[ip] = status
}

// Server runs a lightweight HTTP health check endpoint.
type Server struct {
	cfg    *config.Config
	status *Status
	logger *slog.Logger
	server *http.Server
}

// NewServer creates a health check server. If cfg is nil or HealthPort is 0, the
// server is effectively disabled (Start will be a no-op).
func NewServer(cfg *config.Config, status *Status, logger *slog.Logger) *Server {
	return &Server{
		cfg:    cfg,
		status: status,
		logger: logger,
	}
}

// Start begins serving health check endpoints in a goroutine.
// Returns immediately. Call Shutdown to stop.
func (s *Server) Start() {
	if s.cfg == nil || s.cfg.HealthPort == 0 {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/upload", s.HandleUpload)

	s.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", s.cfg.HealthPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		s.logger.Info("health server started", "port", s.cfg.HealthPort)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("health server error", "error", err)
		}
	}()
}

// Shutdown gracefully stops the health server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}
