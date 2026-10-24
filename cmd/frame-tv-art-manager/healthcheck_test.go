package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// newHealthServer starts an IPv4 loopback server, because the probe always
// dials 127.0.0.1, and returns the port the probe should target.
func newHealthServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("create test listener: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

func TestEndpointCheckClassifiesResponses(t *testing.T) {
	tests := []struct {
		name       string
		wantPath   string
		statusCode int
		body       string
		wantError  string
	}{
		{name: "healthy", wantPath: "/health", statusCode: http.StatusOK, body: `{"status":"ok"}`},
		{name: "liveness uses live endpoint", wantPath: "/live", statusCode: http.StatusOK, body: `{"status":"ok"}`},
		{
			name: "http failure", wantPath: "/health",
			statusCode: http.StatusServiceUnavailable, body: `{}`, wantError: "HTTP status 503",
		},
		{
			name: "invalid JSON", wantPath: "/health",
			statusCode: http.StatusOK, body: `{`, wantError: "decode health check response",
		},
		{
			name: "degraded status", wantPath: "/health",
			statusCode: http.StatusOK, body: `{"status":"error"}`, wantError: `status: "error"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			port := newHealthServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.wantPath {
					http.Error(w, "wrong endpoint", http.StatusNotFound)
					return
				}
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Setenv("HEALTH_PORT", port)

			err := performEndpointCheck(test.wantPath)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("performEndpointCheck() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("performEndpointCheck() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestEndpointCheckReportsConnectionFailure(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEALTH_PORT", strconv.Itoa(port))
	if err := performEndpointCheck("/health"); err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("performEndpointCheck() error = %v", err)
	}
}

func TestHealthCheckPortFallsBackToDefault(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{value: "", want: defaultHealthPort},
		{value: "not-a-number", want: defaultHealthPort},
		{value: "4321", want: 4321},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("HEALTH_PORT", test.value)
			if got := healthCheckPort(); got != test.want {
				t.Fatalf("healthCheckPort() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestHealthcheckFlagExitsWithProbeResult(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Args = []string{"frame-tv-art-manager", os.Getenv("CLI_ARG")}
		handleCLIArgs()
		return
	}

	healthy := newHealthServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	unhealthy := newHealthServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	tests := []struct {
		arg     string
		port    string
		wantErr bool
	}{
		{arg: "-healthcheck", port: healthy},
		{arg: "--livenesscheck", port: healthy},
		{arg: "--healthcheck", port: unhealthy, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.arg+"/"+strconv.FormatBool(test.wantErr), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestHealthcheckFlagExitsWithProbeResult$")
			cmd.Env = append(os.Environ(),
				"GO_WANT_HELPER_PROCESS=1",
				"CLI_ARG="+test.arg,
				"HEALTH_PORT="+test.port,
			)
			output, err := cmd.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Fatalf("%s exited with %v, wantErr %t; output %s", test.arg, err, test.wantErr, output)
			}
			if !test.wantErr && !strings.Contains(string(output), "Healthy") {
				t.Fatalf("%s output = %q, want Healthy", test.arg, output)
			}
		})
	}
}
