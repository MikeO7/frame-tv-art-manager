package health

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"
)

// handleHealth returns a simple 200 OK with basic status, or 503 when the
// most recent sync cycle failed. It is unauthenticated, so it carries only
// coarse state; error text and TV details are on /status.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.status.snapshot()

	healthy := snapshot.Lifecycle == lifecycleReady && snapshot.SyncCount > 0 && snapshot.LastSyncOK
	statusStr, code := statusOK, http.StatusOK
	if !healthy {
		statusStr, code = statusError, http.StatusServiceUnavailable
	}
	writeJSON(w, code, s.syncFields(snapshot, statusStr))
}

// syncFields returns the sync-state fields shared by /health and /status.
// They reveal no error text, addresses, or file names.
func (s *Server) syncFields(snapshot statusSnapshot, status string) map[string]any {
	return map[string]any{
		fieldStatus:       status,
		fieldUptime:       s.now().Sub(snapshot.StartedAt).Round(time.Second).String(),
		fieldLastSync:     snapshot.LastSyncAt.Format(time.RFC3339),
		fieldLastSyncOK:   snapshot.LastSyncOK,
		fieldCurrentStage: snapshot.CurrentStage,
		fieldSyncCount:    snapshot.SyncCount,
		fieldLifecycle:    snapshot.Lifecycle,
	}
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

// handleStatus returns detailed per-TV status information. When UPLOAD_TOKEN
// is set, the error text and TV addresses are shown only to a request with
// valid credentials; anonymous callers get the same fields with TVs numbered
// and errors withheld. Without a token the endpoint stays fully open, matching
// the unauthenticated upload endpoint.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
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
	fields := s.syncFields(snapshot, statusOK)
	fields["started_at"] = snapshot.StartedAt.Format(time.RFC3339)
	if redact {
		fields[fieldRedacted] = true
		fields["tvs"] = redactedTVs(snapshot.TVStatuses)
	} else {
		fields[fieldLastError] = snapshot.LastErrorMessage
		fields["tvs"] = snapshot.TVStatuses
	}
	writeJSON(w, http.StatusOK, fields)
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
