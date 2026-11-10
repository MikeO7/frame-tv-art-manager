// Package health provides a lightweight HTTP health check server
// for monitoring the sync service from Docker healthchecks,
// Uptime Kuma, Home Assistant, or similar systems.
package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// JSON field names and status values shared across the health endpoints.
// Centralizing them keeps the wire contract consistent in one place.
const (
	fieldStatus        = "status"
	fieldError         = "error"
	fieldUptime        = "uptime"
	fieldLastSync      = "last_sync"
	fieldLastSyncOK    = "last_sync_ok"
	fieldLastError     = "last_error"
	fieldCurrentStage  = "current_stage"
	fieldSyncCount     = "sync_count"
	fieldLifecycle     = "lifecycle"
	fieldRedacted      = "redacted"
	fieldSourceWarning = "source_warning"
	// redactedSourceWarning replaces source failure detail for anonymous callers.
	redactedSourceWarning = "artwork sources failed"
	statusOK              = "ok"
	statusError           = "error"
	lifecycleReady        = "ready"
	lifecycleFailed       = "failed"
)

// Status holds the current service health state.
type Status struct {
	mu sync.RWMutex
	statusSnapshot
}

// TVStatus tracks per-TV health information.
type TVStatus struct {
	IP               string `json:"ip,omitempty"`
	LastSeen         string `json:"last_seen"`
	ImageCount       int    `json:"image_count"`
	ArtMode          bool   `json:"art_mode"`
	Status           string `json:"status"` // "ok", "unreachable", "backoff"
	LastErrorMessage string `json:"last_error,omitempty"`
	Rotating         bool   `json:"rotating,omitempty"`
	ArtworkWaiting   int    `json:"artwork_waiting,omitempty"`
	NextRotation     string `json:"next_rotation,omitempty"`
}

// statusSnapshot is the copyable state guarded by Status.mu.
type statusSnapshot struct {
	StartedAt        time.Time
	LastSyncAt       time.Time
	LastSyncOK       bool
	LastErrorMessage string
	SyncCount        int
	CurrentStage     string
	Lifecycle        string
	SourceWarning    string
	TVStatuses       map[string]TVStatus
}

// NewStatus creates a new thread-safe health status tracker.
func NewStatus() *Status {
	return &Status{statusSnapshot: statusSnapshot{
		StartedAt:  time.Now(),
		Lifecycle:  "starting",
		TVStatuses: make(map[string]TVStatus),
	}}
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

// SetSourceWarning records why the last source sync failed, or clears it with
// an empty message. Source failures degrade status without failing a cycle
// that synced TVs from the existing collection.
func (s *Status) SetSourceWarning(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SourceWarning = message
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
	s.TVStatuses[ip] = status
}

// Report is an immutable copy of the service health state.
type Report struct {
	StartedAt    time.Time
	LastSyncAt   time.Time
	LastSyncOK   bool
	LastError    string
	SyncCount    int
	CurrentStage string
	Lifecycle    string
	// TVs is ordered by TV address.
	TVs []TVStatus
}

// Report returns a consistent copy of the current health state.
func (s *Status) Report() Report {
	snapshot := s.snapshot()
	report := Report{
		StartedAt: snapshot.StartedAt, LastSyncAt: snapshot.LastSyncAt, LastSyncOK: snapshot.LastSyncOK,
		LastError: snapshot.LastErrorMessage, SyncCount: snapshot.SyncCount,
		CurrentStage: snapshot.CurrentStage, Lifecycle: snapshot.Lifecycle,
		TVs: make([]TVStatus, 0, len(snapshot.TVStatuses)),
	}
	for _, address := range slices.Sorted(maps.Keys(snapshot.TVStatuses)) {
		report.TVs = append(report.TVs, snapshot.TVStatuses[address])
	}
	return report
}

func (s *Status) snapshot() statusSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := s.statusSnapshot
	snapshot.TVStatuses = make(map[string]TVStatus, len(s.TVStatuses))
	maps.Copy(snapshot.TVStatuses, s.TVStatuses)
	return snapshot
}

// server runs a lightweight HTTP health check endpoint.
type server struct {
	cfg      *config.Config
	status   *Status
	logger   *slog.Logger
	server   *http.Server
	listener net.Listener
	mu       sync.Mutex
	now      func() time.Time
	importer artworkImporter
	imports  chan struct{}
	// authFailures rate-limits wrong UPLOAD_TOKEN and MCP_TOKEN guesses per
	// client address.
	authFailures *authLimiter
	// mcp is the Model Context Protocol handler mounted at /mcp, or nil.
	mcp http.Handler
}

// ServerOption configures an optional HTTP module.
type ServerOption func(*server)

// WithMCP mounts handler at /mcp. The server serves it only while
// MCP_ENABLED is on, and only to requests that present MCP_TOKEN.
func WithMCP(handler http.Handler) ServerOption {
	return func(server *server) { server.mcp = handler }
}

// artworkImporter is the only collection authority required by HTTP uploads.
type artworkImporter interface {
	Import(context.Context, collection.ImportRequest) (collection.Snapshot, error)
}

// NewServer constructs the supervised HTTP module. Uploads are available only
// when importer is the authoritative transactional Artwork Collection.
func NewServer(
	cfg *config.Config,
	status *Status,
	logger *slog.Logger,
	importer artworkImporter,
	options ...ServerOption,
) *server {
	server := &server{
		cfg: cfg, status: status, logger: logger, now: time.Now,
		importer: importer, imports: make(chan struct{}, 1),
	}
	server.authFailures = newAuthLimiter(func() time.Time { return server.now() })
	for _, option := range options {
		option(server)
	}
	return server
}

// Bind acquires the HTTP listener before the application is marked ready.
// Binding errors are therefore terminal startup errors rather than failures in
// an unobserved goroutine.
func (s *server) Bind(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.cfg == nil || s.cfg.HealthPort == 0 {
		return nil
	}
	if s.cfg.UploadEnabled && s.importer == nil {
		return errors.New("upload endpoint requires an authoritative artwork collection")
	}
	if s.cfg.MCPEnabled && s.mcp == nil {
		return errors.New("MCP endpoint is enabled but no MCP handler was provided")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/live", s.handleLive)
	mux.HandleFunc("/ready", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/upload", s.HandleUpload)
	mux.Handle("/mcp", s.mcpEndpoint())

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
func (s *server) Serve() error {
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
func (s *server) Shutdown(ctx context.Context) error {
	if server := s.boundServer(); server != nil {
		return server.Shutdown(ctx)
	}
	return nil
}

// Close immediately releases the listener after a graceful shutdown timeout.
func (s *server) Close() error {
	if server := s.boundServer(); server != nil {
		return server.Close()
	}
	return nil
}

func (s *server) boundServer() *http.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server
}
