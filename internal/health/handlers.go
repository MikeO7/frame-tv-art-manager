package health

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleHealth returns a simple 200 OK with basic status, or 503 when the
// most recent sync cycle failed.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.status.mu.RLock()
	defer s.status.mu.RUnlock()

	// Before the first cycle completes the service is still "ok" (starting up);
	// once a cycle has run, the health reflects whether the last sync succeeded.
	healthy := s.status.SyncCount == 0 || s.status.LastSyncOK
	statusStr, code := statusOK, http.StatusOK
	if !healthy {
		statusStr, code = statusError, http.StatusServiceUnavailable
	}

	writeJSON(w, code, map[string]any{
		fieldStatus:     statusStr,
		"uptime":        time.Since(s.status.StartedAt).Round(time.Second).String(),
		"last_sync":     s.status.LastSyncAt.Format(time.RFC3339),
		"last_sync_ok":  s.status.LastSyncOK,
		"last_error":    s.status.LastErrorMessage,
		"current_stage": s.status.CurrentStage,
		"sync_count":    s.status.SyncCount,
	})
}

// handleStatus returns detailed per-TV status information.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.status.mu.RLock()
	defer s.status.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		fieldStatus:     statusOK,
		"uptime":        time.Since(s.status.StartedAt).Round(time.Second).String(),
		"started_at":    s.status.StartedAt.Format(time.RFC3339),
		"last_sync":     s.status.LastSyncAt.Format(time.RFC3339),
		"last_sync_ok":  s.status.LastSyncOK,
		"last_error":    s.status.LastErrorMessage,
		"current_stage": s.status.CurrentStage,
		"sync_count":    s.status.SyncCount,
		"tvs":           s.status.TVStatuses,
	})
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
