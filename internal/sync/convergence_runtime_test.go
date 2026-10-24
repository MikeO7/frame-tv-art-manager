package sync

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestSamsungConfigForTVPreservesPairingAndSafetySettings(t *testing.T) {
	stateDir := t.TempDir()
	cfg := &config.Config{
		TVIPs: []string{"192.0.2.10"}, TVMAC: net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff},
		TokenDir: stateDir, ClientName: "Frame Manager", VerifyTLS: true, EnableRESTGate: true,
		ConnectionTimeout: 5 * time.Second, APITimeout: 7 * time.Second,
		GateTimeout: 2 * time.Second, SyncIntervalMin: 120,
	}
	got := samsungConfigForTV(cfg, "192.0.2.10")
	if got.Address != "192.0.2.10" || got.ClientName != cfg.ClientName ||
		got.TokenPath != filepath.Join(stateDir, "tv_192_0_2_10.txt") ||
		!got.VerifyTLS || !got.QuietGate || got.ConnectTimeout != 5*time.Second ||
		got.RequestTimeout != 7*time.Second || got.GateTimeout != 2*time.Second ||
		got.BackoffBase != time.Hour || got.BackoffMaximum != time.Hour || got.MAC.String() != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("Samsung config = %+v", got)
	}
}

func TestSamsungConfigForTVDerivesIPv6TokenPathAndShortBackoff(t *testing.T) {
	stateDir := t.TempDir()
	cfg := &config.Config{
		TVIPs: []string{"2001:db8::1"}, TokenDir: stateDir, ClientName: "Frame Manager",
		SyncIntervalMin: 1, ConnectionTimeout: time.Second, APITimeout: time.Second, GateTimeout: time.Second,
	}
	got := samsungConfigForTV(cfg, "2001:db8::1")
	if got.Address != "2001:db8::1" || got.TokenPath != filepath.Join(stateDir, "tv_2001_db8__1.txt") ||
		got.BackoffBase != time.Minute || got.MAC != nil {
		t.Fatalf("Samsung config = %+v", got)
	}
}

func TestNewConvergenceRuntimeEnablesLegacyMigration(t *testing.T) {
	stateDir := t.TempDir()
	cfg := &config.Config{
		TVIPs: []string{"192.0.2.10"}, TokenDir: stateDir,
		ClientName: "Frame Manager", SyncIntervalMin: 5,
		ConnectionTimeout: time.Second, APITimeout: time.Second, GateTimeout: time.Second,
	}
	runtime, err := newConvergenceRuntime(cfg, "192.0.2.10", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("newConvergenceRuntime() error = %v", err)
	}
	if runtime.adapter == nil || runtime.reconciler == nil {
		t.Fatalf("runtime = %+v", runtime)
	}
}

func TestNewConvergenceRuntimeRejectsUnsafeAdapterConfiguration(t *testing.T) {
	cfg := &config.Config{
		TVIPs: []string{"192.0.2.10"}, TokenDir: t.TempDir(), ClientName: "Frame Manager",
		ConnectionTimeout: time.Second, APITimeout: time.Second, GateTimeout: time.Second,
	}
	if _, err := newConvergenceRuntime(cfg, "192.0.2.10", slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("newConvergenceRuntime() with nonpositive sync interval succeeded, want error")
	}
}

func TestConvergenceRuntimeCloseHandlesEmptyAndFailedAdapters(t *testing.T) {
	t.Parallel()

	if err := (*convergenceRuntime)(nil).close(context.Background()); err != nil {
		t.Fatalf("nil runtime close error = %v", err)
	}
	if err := (&convergenceRuntime{}).close(context.Background()); err != nil {
		t.Fatalf("empty runtime close error = %v", err)
	}
	wantErr := errors.New("close failed")
	runtime := &convergenceRuntime{
		address: "192.0.2.10",
		adapter: &recordingConvergenceAdapter{closeErr: wantErr},
	}
	if err := runtime.close(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("runtime close error = %v, want %v", err, wantErr)
	}
}
