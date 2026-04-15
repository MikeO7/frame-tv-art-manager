// Package health provides a lightweight HTTP health check server
// for monitoring the sync service from Docker healthchecks,
// Uptime Kuma, Home Assistant, or similar systems.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Status holds the current service health state.
type Status struct {
	mu sync.RWMutex

	StartedAt  time.Time
	LastSyncAt time.Time
	LastSyncOK bool
	SyncCount  int
	TVStatuses map[string]TVStatus
}

// TVStatus tracks per-TV health information.
type TVStatus struct {
	IP         string `json:"ip"`
	LastSeen   string `json:"last_seen"`
	ImageCount int    `json:"image_count"`
	ArtMode    bool   `json:"art_mode"`
	Status     string `json:"status"` // "ok", "unreachable", "backoff"
}

// NewStatus creates a new health status tracker.
func NewStatus() *Status {
	return &Status{
		StartedAt:  time.Now(),
		TVStatuses: make(map[string]TVStatus),
	}
}

// RecordSync records the result of a sync cycle.
func (s *Status) RecordSync(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastSyncAt = time.Now()
	s.LastSyncOK = ok
	s.SyncCount++
}

// SetTVStatus updates the status for a specific TV.
func (s *Status) SetTVStatus(ip string, status TVStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TVStatuses[ip] = status
}

// Server runs a lightweight HTTP health check endpoint.
type Server struct {
	status *Status
	port   int
	logger *slog.Logger
	server *http.Server
}

// NewServer creates a health check server. If port is 0, the server
// is effectively disabled (Start will be a no-op).
func NewServer(port int, status *Status, logger *slog.Logger) *Server {
	return &Server{
		status: status,
		port:   port,
		logger: logger,
	}
}

// Start begins serving health check endpoints in a goroutine.
// Returns immediately. Call Shutdown to stop.
func (s *Server) Start() {
	if s.port == 0 {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}

	go func() {
		s.logger.Info("health server started", "port", s.port)
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

// handleHealth returns a simple 200 OK with basic status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.status.mu.RLock()
	defer s.status.mu.RUnlock()

	resp := map[string]any{
		"status":       "ok",
		"uptime":       time.Since(s.status.StartedAt).Round(time.Second).String(),
		"last_sync":    s.status.LastSyncAt.Format(time.RFC3339),
		"last_sync_ok": s.status.LastSyncOK,
		"sync_count":   s.status.SyncCount,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleStatus returns detailed per-TV status information.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.status.mu.RLock()
	defer s.status.mu.RUnlock()

	resp := map[string]any{
		"status":       "ok",
		"uptime":       time.Since(s.status.StartedAt).Round(time.Second).String(),
		"started_at":   s.status.StartedAt.Format(time.RFC3339),
		"last_sync":    s.status.LastSyncAt.Format(time.RFC3339),
		"last_sync_ok": s.status.LastSyncOK,
		"sync_count":   s.status.SyncCount,
		"tvs":          s.status.TVStatuses,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
