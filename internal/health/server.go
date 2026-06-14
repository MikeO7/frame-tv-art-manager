// Package health provides a lightweight HTTP health check server
// for monitoring the sync service from Docker healthchecks,
// Uptime Kuma, Home Assistant, or similar systems.
//
//nolint:goconst // string literals are used for JSON key and values in health check endpoints
package health

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
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

// NewStatus creates a new health status tracker.
//
// Returns:
//   - *Status: An instantiated, thread-safe health status tracker struct.
//
// Example:
//
//	status := health.NewStatus()
//	status.SetStage("initialization")
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

// NewServer creates a health check server. If cfg is nil or HealthPort is 0, the server
// is effectively disabled (Start will be a no-op).
//
// Parameters:
//   - cfg:    The application configuration struct.
//   - status: A reference to the application's global health status tracker.
//   - logger: A structured logger for recording server startup and shutdown.
//
// Returns:
//   - *Server: An instantiated server struct ready to be started.
//
// Example:
//
//	server := health.NewServer(cfg, status, logger)
//	server.Start()
//	defer server.Shutdown(ctx)
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
	mux.HandleFunc("/upload", s.handleUpload)

	s.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", s.cfg.HealthPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
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

// handleHealth returns a simple 200 OK with basic status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = r
	s.status.mu.RLock()
	defer s.status.mu.RUnlock()

	resp := map[string]any{
		"status":        "ok",
		"uptime":        time.Since(s.status.StartedAt).Round(time.Second).String(),
		"last_sync":     s.status.LastSyncAt.Format(time.RFC3339),
		"last_sync_ok":  s.status.LastSyncOK,
		"last_error":    s.status.LastErrorMessage,
		"current_stage": s.status.CurrentStage,
		"sync_count":    s.status.SyncCount,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleStatus returns detailed per-TV status information.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	_ = r
	s.status.mu.RLock()
	defer s.status.mu.RUnlock()

	resp := map[string]any{
		"status":        "ok",
		"uptime":        time.Since(s.status.StartedAt).Round(time.Second).String(),
		"started_at":    s.status.StartedAt.Format(time.RFC3339),
		"last_sync":     s.status.LastSyncAt.Format(time.RFC3339),
		"last_sync_ok":  s.status.LastSyncOK,
		"last_error":    s.status.LastErrorMessage,
		"current_stage": s.status.CurrentStage,
		"sync_count":    s.status.SyncCount,
		"tvs":           s.status.TVStatuses,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleUpload processes HTTP multipart file uploads for artwork.
//
//nolint:gocyclo,funlen // complexity and length are due to sequential validation of multipart file parameters
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil || !s.cfg.UploadEnabled {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":"error","error":"Upload endpoint is disabled"}`))
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"status":"error","error":"Method not allowed"}`))
		return
	}

	maxSize := int64(s.cfg.MaxDownloadSizeMB) * 1024 * 1024
	if maxSize <= 0 {
		maxSize = 20 * 1024 * 1024
	}

	// Limit request body size to prevent DoS attacks
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	err := r.ParseMultipartForm(maxSize)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","error":"File too large or invalid request"}`))
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","error":"Missing 'file' parameter"}`))
		return
	}
	defer func() { _ = file.Close() }()

	// Read header bytes to check file signature
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","error":"Failed to read upload stream"}`))
		return
	}

	contentType := http.DetectContentType(buf[:n])
	var ext string
	switch contentType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","error":"Unsupported file type (only JPEG and PNG are allowed)"}`))
		return
	}

	// Buffer the entire file to compute hash and write to disk
	var bodyBytes bytes.Buffer
	bodyBytes.Write(buf[:n])
	_, err = io.Copy(&bodyBytes, file)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","error":"Failed to read upload payload"}`))
		return
	}

	hasher := sha256.New()
	hasher.Write(bodyBytes.Bytes())
	hash := fmt.Sprintf("%x", hasher.Sum(nil))
	hashPrefix := hash[:12]

	filename := artwork.BuildHashName("upload", hashPrefix, ext)
	destPath := filepath.Join(s.cfg.ArtworkDir, filename)

	if _, err := os.Stat(destPath); err == nil {
		// File already exists - deduplicated!
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"message":  "File already exists (deduplicated)",
			"filename": filename,
		})
		return
	}

	err = os.WriteFile(destPath, bodyBytes.Bytes(), 0o644)
	if err != nil {
		s.logger.Error("Failed to write uploaded file", "path", destPath, "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","error":"Failed to save uploaded file"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"message":  "File uploaded successfully",
		"filename": filename,
	})
}
