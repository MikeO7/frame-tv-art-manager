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

	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(uploadHTML))
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

const uploadHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Frame TV Art Uploader</title>
    <style>
        :root {
            --bg-color: #0b0f19;
            --card-bg: rgba(20, 26, 42, 0.6);
            --border-color: rgba(255, 255, 255, 0.08);
            --text-color: #f3f4f6;
            --text-muted: #9ca3af;
            --primary-color: #6366f1;
            --primary-hover: #4f46e5;
            --success-color: #10b981;
            --error-color: #ef4444;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
            background-color: var(--bg-color);
            color: var(--text-color);
            margin: 0;
            display: flex;
            align-items: center;
            justify-content: center;
            min-height: 100vh;
            padding: 20px;
            box-sizing: border-box;
        }
        .container {
            width: 100%;
            max-width: 500px;
            background: var(--card-bg);
            border: 1px solid var(--border-color);
            border-radius: 24px;
            padding: 32px;
            box-shadow: 0 20px 40px rgba(0, 0, 0, 0.3);
            backdrop-filter: blur(16px);
        }
        h1 {
            font-size: 24px;
            margin-top: 0;
            margin-bottom: 8px;
            font-weight: 700;
            letter-spacing: -0.5px;
            text-align: center;
        }
        .subtitle {
            color: var(--text-muted);
            text-align: center;
            font-size: 14px;
            margin-bottom: 32px;
        }
        .dropzone {
            border: 2px dashed rgba(99, 102, 241, 0.3);
            border-radius: 16px;
            padding: 40px 20px;
            text-align: center;
            cursor: pointer;
            transition: all 0.2s ease;
            background: rgba(99, 102, 241, 0.02);
        }
        .dropzone:hover, .dropzone.dragover {
            border-color: var(--primary-color);
            background: rgba(99, 102, 241, 0.06);
        }
        .dropzone svg {
            width: 48px;
            height: 48px;
            color: var(--primary-color);
            margin-bottom: 16px;
        }
        .dropzone p {
            margin: 0;
            font-size: 15px;
            font-weight: 500;
        }
        .dropzone span {
            display: block;
            font-size: 12px;
            color: var(--text-muted);
            margin-top: 8px;
        }
        #file-input {
            display: none;
        }
        .file-list {
            margin-top: 24px;
            max-height: 200px;
            overflow-y: auto;
        }
        .file-item {
            display: flex;
            align-items: center;
            justify-content: space-between;
            padding: 12px;
            background: rgba(255, 255, 255, 0.02);
            border: 1px solid var(--border-color);
            border-radius: 12px;
            margin-bottom: 8px;
            font-size: 13px;
        }
        .file-name {
            font-weight: 500;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
            max-width: 200px;
        }
        .file-status {
            font-weight: 600;
        }
        .status-uploading { color: var(--primary-color); }
        .status-success { color: var(--success-color); }
        .status-error { color: var(--error-color); }
        .progress-bar {
            height: 4px;
            width: 100%;
            background: rgba(255, 255, 255, 0.05);
            border-radius: 2px;
            margin-top: 8px;
            overflow: hidden;
            display: none;
        }
        .progress-inner {
            height: 100%;
            background: var(--primary-color);
            width: 0%;
            transition: width 0.1s ease;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Frame TV Art Uploader</h1>
        <div class="subtitle">Upload JPEG or PNG images directly to your TV</div>

        <label for="file-input">
            <div class="dropzone" id="dropzone">
                <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M12 16.5V9.75m0 0l3 3m-3-3l-3 3M6.75 19.5a4.5 4.5 0 01-1.41-8.775 5.25 5.25 0 0110.233-2.33 3 3 0 013.758 3.848A3.752 3.752 0 0118 19.5H6.75z" />
                </svg>
                <p>Tap to select or drag photos here</p>
                <span>Supports multiple JPEGs and PNGs</span>
            </div>
        </label>
        <input type="file" id="file-input" accept="image/jpeg,image/png" multiple>

        <div class="progress-bar" id="progress-bar">
            <div class="progress-inner" id="progress-inner"></div>
        </div>

        <div class="file-list" id="file-list"></div>
    </div>

    <script>
        const dropzone = document.getElementById('dropzone');
        const fileInput = document.getElementById('file-input');
        const fileList = document.getElementById('file-list');
        const progressBar = document.getElementById('progress-bar');
        const progressInner = document.getElementById('progress-inner');

        // Drag and drop events
        ['dragenter', 'dragover'].forEach(eventName => {
            dropzone.addEventListener(eventName, (e) => {
                e.preventDefault();
                dropzone.classList.add('dragover');
            }, false);
        });

        ['dragleave', 'drop'].forEach(eventName => {
            dropzone.addEventListener(eventName, (e) => {
                e.preventDefault();
                dropzone.classList.remove('dragover');
            }, false);
        });

        dropzone.addEventListener('drop', (e) => {
            const dt = e.dataTransfer;
            const files = dt.files;
            handleFiles(files);
        });

        fileInput.addEventListener('change', (e) => {
            handleFiles(e.target.files);
        });

        async function handleFiles(files) {
            if (files.length === 0) return;

            progressBar.style.display = 'block';
            progressInner.style.width = '0%';

            for (let i = 0; i < files.length; i++) {
                const file = files[i];
                const item = document.createElement('div');
                item.className = 'file-item';
                item.innerHTML = '<span class=\"file-name\">' + file.name + '</span><span class=\"file-status status-uploading\">Uploading...</span>';
                fileList.insertBefore(item, fileList.firstChild);

                const statusSpan = item.querySelector('.file-status');

                const formData = new FormData();
                formData.append('file', file);

                try {
                    const response = await fetch('/upload', {
                        method: 'POST',
                        body: formData
                    });

                    const result = await response.json();
                    if (response.ok && result.status === 'ok') {
                        statusSpan.className = 'file-status status-success';
                        statusSpan.textContent = result.message.includes('exists') ? 'Deduplicated' : 'Success';
                    } else {
                        statusSpan.className = 'file-status status-error';
                        statusSpan.textContent = result.error || 'Failed';
                    }
                } catch (err) {
                    statusSpan.className = 'file-status status-error';
                    statusSpan.textContent = 'Network Error';
                }

                progressInner.style.width = ((i + 1) / files.length) * 100 + '%';
            }

            setTimeout(() => {
                progressBar.style.display = 'none';
            }, 1000);
        }
    </script>
</body>
</html>`
