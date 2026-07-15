package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPStatusWireContract(t *testing.T) {
	startedAt := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	lastSyncAt := startedAt.Add(30 * time.Second)
	status := NewStatus()
	status.StartedAt = startedAt
	status.LastSyncAt = lastSyncAt
	status.LastSyncOK = true
	status.SyncCount = 1
	status.CurrentStage = "idle"
	status.SetLifecycle("ready")
	status.SetTVStatus("192.0.2.10", TVStatus{
		IP:         "192.0.2.10",
		LastSeen:   "2026-07-12T12:00:00Z",
		ImageCount: 3,
		ArtMode:    true,
		Status:     "ok",
	})

	server := newTestServer(t, testConfig(0, false, ""), status, silentLogger())
	server.now = func() time.Time { return startedAt.Add(90 * time.Second) }
	response := httptest.NewRecorder()
	server.handleStatus(response, httptest.NewRequest(http.MethodGet, "/status", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	wantKeys := map[string]struct{}{
		fieldStatus: {}, fieldUptime: {}, "started_at": {}, fieldLastSync: {},
		fieldLastSyncOK: {}, fieldLastError: {}, fieldCurrentStage: {},
		fieldSyncCount: {}, "tvs": {}, "lifecycle": {},
	}
	if len(payload) != len(wantKeys) {
		t.Fatalf("status keys = %v, want exactly %v", payload, wantKeys)
	}
	for key := range wantKeys {
		if _, ok := payload[key]; !ok {
			t.Errorf("status response missing key %q", key)
		}
	}
	if payload[fieldStatus] != statusOK || payload[fieldUptime] != "1m30s" ||
		payload["started_at"] != startedAt.Format(time.RFC3339) ||
		payload[fieldLastSync] != lastSyncAt.Format(time.RFC3339) ||
		payload[fieldLastSyncOK] != true || payload[fieldLastError] != "" ||
		payload[fieldCurrentStage] != "idle" || payload[fieldSyncCount] != float64(1) ||
		payload["lifecycle"] != "ready" {
		t.Fatalf("status wire values = %#v", payload)
	}

	tvs, ok := payload["tvs"].(map[string]any)
	if !ok {
		t.Fatalf("tvs = %#v, want object", payload["tvs"])
	}
	tv, ok := tvs["192.0.2.10"].(map[string]any)
	if !ok {
		t.Fatalf("TV entry = %#v, want object", tvs["192.0.2.10"])
	}
	if len(tv) != 5 {
		t.Fatalf("TV wire keys = %#v, want exactly five non-empty fields", tv)
	}
	if tv["image_count"] != float64(3) || tv["art_mode"] != true || tv["status"] != "ok" {
		t.Fatalf("TV wire state = %#v", tv)
	}
}
