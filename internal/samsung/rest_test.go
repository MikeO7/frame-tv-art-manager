package samsung

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestFetchDeviceInfo(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		expected := DeviceInfo{
			ModelName:       "QN55LS03AAFXZA",
			FirmwareVersion: "1234",
			FrameTVSupport:  stringTrue,
			PowerState:      "on",
		}

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2/" {
				t.Errorf("expected path /api/v2/, got %s", r.URL.Path)
			}
			resp := DeviceInfoResponse{Device: expected}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		u, _ := url.Parse(server.URL)
		host, portStr, _ := net.SplitHostPort(u.Host)
		port, _ := strconv.Atoi(portStr)

		info, err := FetchDeviceInfo(context.Background(), host, port, 2*time.Second)
		if err != nil {
			t.Fatalf("FetchDeviceInfo failed: %v", err)
		}

		if info.ModelName != expected.ModelName {
			t.Errorf("expected model %s, got %s", expected.ModelName, info.ModelName)
		}
		if info.IsFrameTV() != true {
			t.Error("expected IsFrameTV to be true")
		}
		if info.IsOn() != true {
			t.Error("expected IsOn to be true")
		}
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{invalid json`))
		}))
		defer server.Close()

		u, _ := url.Parse(server.URL)
		host, portStr, _ := net.SplitHostPort(u.Host)
		port, _ := strconv.Atoi(portStr)

		_, err := FetchDeviceInfo(context.Background(), host, port, 2*time.Second)
		if err == nil {
			t.Fatal("expected error due to invalid JSON, got nil")
		}
	})

	t.Run("Timeout", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()

		u, _ := url.Parse(server.URL)
		host, portStr, _ := net.SplitHostPort(u.Host)
		port, _ := strconv.Atoi(portStr)

		// Set a very short timeout.
		_, err := FetchDeviceInfo(context.Background(), host, port, 10*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
	})

	t.Run("ContextCancelled", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
		}))
		defer server.Close()

		u, _ := url.Parse(server.URL)
		host, portStr, _ := net.SplitHostPort(u.Host)
		port, _ := strconv.Atoi(portStr)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := FetchDeviceInfo(ctx, host, port, 2*time.Second)
		if err == nil {
			t.Fatal("expected error due to cancelled context, got nil")
		}
	})
}
