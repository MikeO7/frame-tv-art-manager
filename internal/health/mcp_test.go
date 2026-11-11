package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

const testMCPToken = "mcp-test-token-0123456789abcdef"

func mcpTestServer(t *testing.T, enabled bool) (*server, *int) {
	t.Helper()
	reached := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusAccepted)
	})
	cfg := &config.Config{MCPEnabled: enabled, MCPToken: testMCPToken}
	server := NewServer(cfg, NewStatus(), silentLogger(), nil, WithMCP(handler))
	clock := &fakeClock{now: time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)}
	server.now = clock.Now
	return server, &reached
}

func mcpRequest(client, authorization string, headers map[string]string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://frame.local:8080/mcp", strings.NewReader(`{}`))
	request.RemoteAddr = client + ":40000"
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return request
}

func TestMCPEndpointAuthentication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		enabled       bool
		authorization string
		headers       map[string]string
		wantCode      int
		wantReached   bool
	}{
		{name: "disabled endpoint is not found", authorization: "Bearer " + testMCPToken, wantCode: http.StatusNotFound},
		{name: "missing token", enabled: true, wantCode: http.StatusUnauthorized},
		{name: "wrong token", enabled: true, authorization: "Bearer wrong-token", wantCode: http.StatusUnauthorized},
		{name: "basic credentials are not a bearer token", enabled: true, authorization: "Basic ZnJhbWU6dG9rZW4=", wantCode: http.StatusUnauthorized},
		{name: "correct token", enabled: true, authorization: "Bearer " + testMCPToken, wantCode: http.StatusAccepted, wantReached: true},
		{
			name: "cross-origin browser request", enabled: true, authorization: "Bearer " + testMCPToken,
			headers: map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, wantCode: http.StatusForbidden,
		},
		{
			name: "same-origin browser request", enabled: true, authorization: "Bearer " + testMCPToken,
			headers: map[string]string{"Origin": "http://frame.local:8080"}, wantCode: http.StatusAccepted, wantReached: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server, reached := mcpTestServer(t, test.enabled)
			response := httptest.NewRecorder()
			server.mcpEndpoint().ServeHTTP(response, mcpRequest("192.0.2.60", test.authorization, test.headers))
			if response.Code != test.wantCode || (*reached > 0) != test.wantReached {
				t.Fatalf("status = %d reached = %d, want %d reached %t", response.Code, *reached, test.wantCode, test.wantReached)
			}
			if strings.Contains(response.Body.String(), testMCPToken) {
				t.Fatal("response echoed the MCP token")
			}
		})
	}
}

func TestMCPEndpointRateLimitsWrongTokens(t *testing.T) {
	t.Parallel()
	server, reached := mcpTestServer(t, true)
	endpoint := server.mcpEndpoint()
	serve := func(client, authorization string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		endpoint.ServeHTTP(response, mcpRequest(client, authorization, nil))
		return response
	}
	for attempt := 1; attempt <= authFailureLimit; attempt++ {
		if code := serve("192.0.2.61", "Bearer guess").Code; code != http.StatusUnauthorized {
			t.Fatalf("wrong attempt %d status = %d, want 401", attempt, code)
		}
	}
	limited := serve("192.0.2.61", "Bearer another-guess")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" || *reached != 0 {
		t.Fatalf("wrong token beyond the budget status = %d retry %q reached %d, want 429",
			limited.Code, limited.Header().Get("Retry-After"), *reached)
	}
	if code := serve("192.0.2.61", "").Code; code != http.StatusUnauthorized {
		t.Fatalf("limited client without a token status = %d, want the ordinary 401", code)
	}
	if code := serve("192.0.2.61", "Bearer "+testMCPToken).Code; code != http.StatusAccepted || *reached != 1 {
		t.Fatalf("correct token after wrong guesses from the same address = %d reached %d, want 202", code, *reached)
	}
	if code := serve("192.0.2.62", "Bearer guess").Code; code != http.StatusUnauthorized {
		t.Fatalf("other client status = %d, want its own budget (401)", code)
	}
}

func TestMCPCrossOriginRequestsDoNotSpendTheFailureBudget(t *testing.T) {
	t.Parallel()
	server, _ := mcpTestServer(t, true)
	endpoint := server.mcpEndpoint()
	crossSite := map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}
	for range authFailureLimit * 2 {
		response := httptest.NewRecorder()
		endpoint.ServeHTTP(response, mcpRequest("192.0.2.63", "Bearer guess", crossSite))
		if response.Code != http.StatusForbidden {
			t.Fatalf("cross-origin request status = %d, want 403", response.Code)
		}
	}
	response := httptest.NewRecorder()
	endpoint.ServeHTTP(response, mcpRequest("192.0.2.63", "Bearer guess", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("first direct wrong token = %d, want 401: a browser page must not spend the client's budget", response.Code)
	}
}

func TestBindRejectsMCPWithoutHandler(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{HealthPort: 1, HealthBindAddress: "127.0.0.1", MCPEnabled: true, MCPToken: testMCPToken}
	err := NewServer(cfg, NewStatus(), silentLogger(), nil).Bind(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no MCP handler") {
		t.Fatalf("Bind() error = %v, want missing MCP handler", err)
	}
}

func TestStatusReportOrdersTVsByAddress(t *testing.T) {
	t.Parallel()
	status := NewStatus()
	status.SetTVStatus("192.0.2.20", TVStatus{IP: "192.0.2.20", Status: "ok"})
	status.SetTVStatus("192.0.2.10", TVStatus{IP: "192.0.2.10", Status: "unreachable"})
	status.RecordSync(true, nil)
	report := status.Report()
	if len(report.TVs) != 2 || report.TVs[0].IP != "192.0.2.10" || report.TVs[1].IP != "192.0.2.20" ||
		!report.LastSyncOK || report.SyncCount != 1 {
		t.Fatalf("Report() = %+v", report)
	}
}
