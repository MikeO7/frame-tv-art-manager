// Package health provides a lightweight HTTP health check server
// for monitoring the sync service from Docker healthchecks,
// Uptime Kuma, Home Assistant, or similar systems.
package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// JSON field names and status values shared across the health endpoints.
// Centralizing them keeps the wire contract consistent in one place.
const (
	fieldStatus       = "status"
	fieldError        = "error"
	fieldUptime       = "uptime"
	fieldLastSync     = "last_sync"
	fieldLastSyncOK   = "last_sync_ok"
	fieldLastError    = "last_error"
	fieldCurrentStage = "current_stage"
	fieldSyncCount    = "sync_count"
	fieldLifecycle    = "lifecycle"
	statusOK          = "ok"
	statusError       = "error"
	lifecycleReady    = "ready"
	lifecycleFailed   = "failed"
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
	Lifecycle        string
	TVStatuses       map[string]TVStatus
}

// TVStatus tracks per-TV health information.
type TVStatus struct {
	IP               string   `json:"ip"`
	LastSeen         string   `json:"last_seen"`
	ImageCount       int      `json:"image_count"`
	ArtMode          bool     `json:"art_mode"`
	Status           string   `json:"status"` // "ok", "unreachable", "backoff"
	LastErrorMessage string   `json:"last_error,omitempty"`
	FreeSpaceBytes   *int64   `json:"free_space_bytes,omitempty"`
	FreeSpacePercent *float64 `json:"free_space_percent,omitempty"`
}

type statusSnapshot struct {
	StartedAt        time.Time
	LastSyncAt       time.Time
	LastSyncOK       bool
	LastErrorMessage string
	SyncCount        int
	CurrentStage     string
	Lifecycle        string
	TVStatuses       map[string]TVStatus
}

// NewStatus creates a new thread-safe health status tracker.
func NewStatus() *Status {
	return &Status{
		StartedAt:  time.Now(),
		Lifecycle:  "starting",
		TVStatuses: make(map[string]TVStatus),
	}
}

// SetLifecycle publishes the process supervisor state. Readiness is granted
// only while the supervisor is ready and at least one Sync Cycle has completed
// successfully.
func (s *Status) SetLifecycle(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Lifecycle = state
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
	if status.LastSeen == "" {
		status.LastSeen = s.TVStatuses[ip].LastSeen
	}
	s.TVStatuses[ip] = cloneTVStatus(status)
}

func (s *Status) snapshot() statusSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tvs := make(map[string]TVStatus, len(s.TVStatuses))
	for ip, tvStatus := range s.TVStatuses {
		tvs[ip] = cloneTVStatus(tvStatus)
	}
	return statusSnapshot{
		StartedAt:        s.StartedAt,
		LastSyncAt:       s.LastSyncAt,
		LastSyncOK:       s.LastSyncOK,
		LastErrorMessage: s.LastErrorMessage,
		SyncCount:        s.SyncCount,
		CurrentStage:     s.CurrentStage,
		Lifecycle:        s.Lifecycle,
		TVStatuses:       tvs,
	}
}

func cloneTVStatus(status TVStatus) TVStatus {
	clone := status
	if status.FreeSpaceBytes != nil {
		value := *status.FreeSpaceBytes
		clone.FreeSpaceBytes = &value
	}
	if status.FreeSpacePercent != nil {
		value := *status.FreeSpacePercent
		clone.FreeSpacePercent = &value
	}
	return clone
}

// Server runs a lightweight HTTP health check endpoint.
type Server struct {
	cfg      *config.Config
	status   *Status
	logger   *slog.Logger
	server   *http.Server
	listener net.Listener
	mu       sync.Mutex
	now      func() time.Time
	importer ArtworkImporter
	imports  chan struct{}
}

// ArtworkImporter is the only collection authority required by HTTP uploads.
type ArtworkImporter interface {
	Import(context.Context, collection.ImportRequest) (collection.Snapshot, error)
}

// NewServer constructs the supervised HTTP module. Uploads are available only
// when importer is the authoritative transactional Artwork Collection.
func NewServer(
	cfg *config.Config,
	status *Status,
	logger *slog.Logger,
	importer ArtworkImporter,
) *Server {
	return &Server{
		cfg: cfg, status: status, logger: logger, now: time.Now,
		importer: importer, imports: make(chan struct{}, 1),
	}
}

// Bind acquires the HTTP listener before the application is marked ready.
// Binding errors are therefore terminal startup errors rather than failures in
// an unobserved goroutine.
func (s *Server) Bind(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.cfg == nil || s.cfg.HealthPort == 0 {
		return nil
	}
	if s.cfg.UploadEnabled && s.importer == nil {
		return errors.New("upload endpoint requires an authoritative artwork collection")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/live", s.handleLive)
	mux.HandleFunc("/ready", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/upload", s.HandleUpload)

	server := &http.Server{
		Addr:              net.JoinHostPort(s.cfg.HealthBindAddress, fmt.Sprintf("%d", s.cfg.HealthPort)),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", server.Addr, err)
	}
	s.mu.Lock()
	s.server = server
	s.listener = listener
	s.mu.Unlock()
	s.logger.Info("health server bound", "address", listener.Addr().String())
	return nil
}

// Serve runs the already-bound server until shutdown.
func (s *Server) Serve() error {
	s.mu.Lock()
	server, listener := s.server, s.listener
	s.mu.Unlock()
	if server == nil || listener == nil {
		return nil
	}
	err := server.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve health HTTP: %w", err)
	}
	return err
}

// Shutdown gracefully stops the health server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

// Close immediately releases the listener after a graceful shutdown timeout.
func (s *Server) Close() error {
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Close()
}
