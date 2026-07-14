package health

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleHealth returns a simple 200 OK with basic status, or 503 when the
// most recent sync cycle failed.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.status.snapshot()

	healthy := snapshot.Lifecycle == lifecycleReady && snapshot.SyncCount > 0 && snapshot.LastSyncOK
	statusStr, code := statusOK, http.StatusOK
	if !healthy {
		statusStr, code = statusError, http.StatusServiceUnavailable
	}

	writeJSON(w, code, map[string]any{
		fieldStatus:       statusStr,
		fieldUptime:       s.now().Sub(snapshot.StartedAt).Round(time.Second).String(),
		fieldLastSync:     snapshot.LastSyncAt.Format(time.RFC3339),
		fieldLastSyncOK:   snapshot.LastSyncOK,
		fieldLastError:    snapshot.LastErrorMessage,
		fieldCurrentStage: snapshot.CurrentStage,
		fieldSyncCount:    snapshot.SyncCount,
		fieldLifecycle:    snapshot.Lifecycle,
	})
}

// handleLive reports only process liveness. It deliberately does not claim the
// application is ready to reconcile a TV.
func (s *Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.status.snapshot()
	code := http.StatusOK
	state := statusOK
	if snapshot.Lifecycle == lifecycleFailed {
		code = http.StatusServiceUnavailable
		state = statusError
	}
	writeJSON(w, code, map[string]any{fieldStatus: state, fieldLifecycle: snapshot.Lifecycle})
}

// handleStatus returns detailed per-TV status information.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.status.snapshot()

	writeJSON(w, http.StatusOK, map[string]any{
		fieldStatus:       statusOK,
		fieldUptime:       s.now().Sub(snapshot.StartedAt).Round(time.Second).String(),
		"started_at":      snapshot.StartedAt.Format(time.RFC3339),
		fieldLastSync:     snapshot.LastSyncAt.Format(time.RFC3339),
		fieldLastSyncOK:   snapshot.LastSyncOK,
		fieldLastError:    snapshot.LastErrorMessage,
		fieldCurrentStage: snapshot.CurrentStage,
		fieldSyncCount:    snapshot.SyncCount,
		fieldLifecycle:    snapshot.Lifecycle,
		"tvs":             snapshot.TVStatuses,
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
