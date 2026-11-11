package health

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

// handleHealth returns a simple 200 OK with basic status, or 503 when the
// most recent sync cycle failed. It is unauthenticated, so it carries only
// coarse state; error text and TV details are on /status.
func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.status.snapshot()

	state, code := healthState(snapshot), http.StatusOK
	if state != statusOK {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, s.syncFields(snapshot, state))
}

// healthState is the one status string /health, /ready, and /status report
// for a snapshot: ok only while the supervisor is ready and the most recent
// Sync Cycle succeeded.
func healthState(snapshot statusSnapshot) string {
	if snapshot.Lifecycle == lifecycleReady && snapshot.SyncCount > 0 && snapshot.LastSyncOK {
		return statusOK
	}
	return statusError
}

// syncFields returns the sync-state fields shared by /health and /status.
// They reveal no error text, addresses, or file names.
func (s *server) syncFields(snapshot statusSnapshot, status string) map[string]any {
	// No cycle has finished yet, so there is no time to report.
	lastSync := ""
	if !snapshot.LastSyncAt.IsZero() {
		lastSync = snapshot.LastSyncAt.Format(time.RFC3339)
	}
	return map[string]any{
		fieldStatus:       status,
		fieldUptime:       s.now().Sub(snapshot.StartedAt).Round(time.Second).String(),
		fieldLastSync:     lastSync,
		fieldLastSyncOK:   snapshot.LastSyncOK,
		fieldCurrentStage: snapshot.CurrentStage,
		fieldSyncCount:    snapshot.SyncCount,
		fieldLifecycle:    snapshot.Lifecycle,
	}
}

// handleLive reports only process liveness. It deliberately does not claim the
// application is ready to reconcile a TV.
func (s *server) handleLive(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.status.snapshot()
	code := http.StatusOK
	state := statusOK
	if snapshot.Lifecycle == lifecycleFailed {
		code = http.StatusServiceUnavailable
		state = statusError
	}
	writeJSON(w, code, map[string]any{fieldStatus: state, fieldLifecycle: snapshot.Lifecycle})
}

// handleStatus returns detailed per-TV status information and the credits
// that provider terms require for downloaded artwork. The response is always
// 200; its status field is the same value /health reports. When UPLOAD_TOKEN
// is set, the error text and TV addresses are shown only to a request with
// valid credentials; anonymous callers get the same fields with TVs numbered
// and errors withheld. Credits are public and appear in both views. Without a
// token /status stays fully open; uploads, by contrast, are refused until a
// token is configured.
func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	redact := false
	if s.cfg != nil && s.cfg.UploadToken != "" {
		result, wait := s.authenticate(r)
		switch result {
		case authValid:
		case authAbsent:
			redact = true
		default:
			writeAuthChallenge(w, result, wait)
			return
		}
	}
	snapshot := s.status.snapshot()
	fields := s.syncFields(snapshot, healthState(snapshot))
	fields["started_at"] = snapshot.StartedAt.Format(time.RFC3339)
	if redact {
		fields[fieldRedacted] = true
		fields["tvs"] = redactedTVs(snapshot.TVStatuses)
	} else {
		fields[fieldLastError] = snapshot.LastErrorMessage
		fields["tvs"] = snapshot.TVStatuses
	}
	if snapshot.SourceWarning != "" {
		// The detail can name a source, so anonymous callers see only that
		// a source failed.
		fields[fieldSourceWarning] = snapshot.SourceWarning
		if redact {
			fields[fieldSourceWarning] = redactedSourceWarning
		}
	}
	if s.cfg != nil && s.cfg.SourcesFile != "" {
		attributions, err := s.attributions.load(r.Context(), s.cfg.ArtworkDir)
		if err != nil {
			s.logger.Warn("read source attributions failed", "error", err)
		} else {
			fields["attributions"] = attributions
		}
	}
	writeJSON(w, http.StatusOK, fields)
}

// sourceStateName is the control file sources.ReadAttributions parses. It is
// used only to notice changes: if the file cannot be inspected the cache is
// bypassed, so a mismatch costs speed, never correctness.
const sourceStateName = "source-state.json"

// attributionCache keeps the parsed source credits between /status requests.
// The state file can be tens of megabytes, so it is re-read only when its
// modification time, size, or mode changes.
type attributionCache struct {
	mu      sync.Mutex
	read    func(ctx context.Context, artworkDir string) (any, error)
	valid   bool
	modTime time.Time
	size    int64
	mode    os.FileMode
	value   any
}

func newAttributionCache() *attributionCache {
	return &attributionCache{read: func(ctx context.Context, artworkDir string) (any, error) {
		return sources.ReadAttributions(ctx, artworkDir)
	}}
}

// load returns the credits recorded for artworkDir. A failed read is returned
// and never cached.
func (c *attributionCache) load(ctx context.Context, artworkDir string) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := os.Lstat(collection.ControlPath(artworkDir, sourceStateName))
	if err != nil {
		c.valid, c.value = false, nil
		return c.read(ctx, artworkDir)
	}
	// The mode is part of the key because an unsafe mode or a symlink makes
	// the file unacceptable to the strict reader without changing its bytes.
	if c.valid && info.ModTime().Equal(c.modTime) && info.Size() == c.size && info.Mode() == c.mode {
		return c.value, nil
	}
	value, err := c.read(ctx, artworkDir)
	if err != nil {
		c.valid, c.value = false, nil
		return nil, err
	}
	c.valid, c.modTime, c.size, c.mode, c.value = true, info.ModTime(), info.Size(), info.Mode(), value
	return value, nil
}

// redactedTVs keys TVs as tv-1, tv-2, ... in address order and drops the
// address and error text, keeping the operational fields.
func redactedTVs(statuses map[string]TVStatus) map[string]TVStatus {
	addresses := slices.Sorted(maps.Keys(statuses))
	redacted := make(map[string]TVStatus, len(addresses))
	for index, address := range addresses {
		tv := statuses[address]
		tv.IP, tv.LastErrorMessage = "", ""
		redacted[fmt.Sprintf("tv-%d", index+1)] = tv
	}
	return redacted
}

// writeJSON encodes payload as a JSON response, emitting code only for
// non-200 statuses so the default 200 path avoids a redundant WriteHeader.
func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// writeJSONError writes a structured error response with the given status code.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{fieldStatus: statusError, fieldError: msg})
}
