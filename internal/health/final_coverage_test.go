package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLiveEndpointReflectsProcessFailure(t *testing.T) {
	tests := []struct {
		name      string
		lifecycle string
		wantCode  int
		wantState string
	}{
		{name: "starting remains live", lifecycle: "starting", wantCode: http.StatusOK, wantState: statusOK},
		{name: "ready remains live", lifecycle: "ready", wantCode: http.StatusOK, wantState: statusOK},
		{name: "failed is not live", lifecycle: "failed", wantCode: http.StatusServiceUnavailable, wantState: statusError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := NewStatus()
			status.SetLifecycle(test.lifecycle)
			server := newTestServer(t, testConfig(0, false, ""), status, silentLogger())
			response := httptest.NewRecorder()

			server.handleLive(response, httptest.NewRequest(http.MethodGet, "/live", nil))

			if response.Code != test.wantCode {
				t.Fatalf("status code = %d, want %d", response.Code, test.wantCode)
			}
			var payload map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode liveness response: %v", err)
			}
			if payload[fieldStatus] != test.wantState || payload[fieldLifecycle] != test.lifecycle {
				t.Fatalf("liveness response = %#v", payload)
			}
		})
	}
}

func TestServerCloseIsSafeBeforeBind(t *testing.T) {
	server := newTestServer(t, testConfig(0, false, ""), NewStatus(), silentLogger())
	if err := server.Close(); err != nil {
		t.Fatalf("Close() before Bind error = %v", err)
	}

	server.server = &http.Server{}
	if err := server.Close(); err != nil {
		t.Fatalf("Close() on idle HTTP server error = %v", err)
	}
}
