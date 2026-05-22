package samsung

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
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
			_ = w
			_ = r
			if r.URL.Path != "/api/v2/" {
				t.Errorf("expected path /api/v2/, got %s", r.URL.Path)
			}
			resp := deviceInfoResponse{Device: expected}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		u, _ := url.Parse(server.URL)
		host, portStr, _ := net.SplitHostPort(u.Host)
		port, _ := strconv.Atoi(portStr)

		c := NewClient(host, &config.Config{APITimeout: 2 * time.Second}, slog.Default())
		info, err := c.fetchDeviceInfo(context.Background(), port)
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
			_ = w
			_ = r
			_ = r
			_, _ = w.Write([]byte(`{invalid json`))
		}))
		defer server.Close()

		u, _ := url.Parse(server.URL)
		host, portStr, _ := net.SplitHostPort(u.Host)
		port, _ := strconv.Atoi(portStr)

		c := NewClient(host, &config.Config{APITimeout: 2 * time.Second}, slog.Default())
		_, err := c.fetchDeviceInfo(context.Background(), port)
		if err == nil {
			t.Fatal("expected error due to invalid JSON, got nil")
		}
	})

	t.Run("Timeout", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = w
			_ = r
			_ = r
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer server.Close()

		u, _ := url.Parse(server.URL)
		host, portStr, _ := net.SplitHostPort(u.Host)
		port, _ := strconv.Atoi(portStr)

		// Set a very short timeout.
		c := NewClient(host, &config.Config{APITimeout: 10 * time.Millisecond}, slog.Default())
		_, err := c.fetchDeviceInfo(context.Background(), port)
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
	})

	t.Run("ContextCancelled", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = w
			_ = r
			_ = r
			time.Sleep(100 * time.Millisecond)
		}))
		defer server.Close()

		u, _ := url.Parse(server.URL)
		host, portStr, _ := net.SplitHostPort(u.Host)
		port, _ := strconv.Atoi(portStr)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		c := NewClient(host, &config.Config{APITimeout: 2 * time.Second}, slog.Default())
		_, err := c.fetchDeviceInfo(ctx, port)
		if err == nil {
			t.Fatal("expected error due to cancelled context, got nil")
		}
	})
}
