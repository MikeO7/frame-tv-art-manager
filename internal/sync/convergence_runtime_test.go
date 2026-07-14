package sync

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestSamsungConfigForTVPreservesPairingAndSafetySettings(t *testing.T) {
	stateDir := t.TempDir()
	cfg := &config.Config{
		TVIPs: []string{"192.0.2.10"}, TVMAC: "AA:BB:CC:DD:EE:FF",
		TokenDir: stateDir, ClientName: "Frame Manager", VerifyTLS: true, EnableRESTGate: true,
		ConnectionTimeout: 5 * time.Second, APITimeout: 7 * time.Second,
		GateTimeout: 2 * time.Second, SyncIntervalMin: 120,
	}
	got, err := samsungConfigForTV(cfg, "192.0.2.10")
	if err != nil {
		t.Fatalf("samsungConfigForTV() error = %v", err)
	}
	if got.Address != "192.0.2.10" || got.ClientName != cfg.ClientName ||
		got.TokenPath != filepath.Join(stateDir, "tv_192_0_2_10.txt") ||
		!got.VerifyTLS || !got.QuietGate || got.ConnectTimeout != 5*time.Second ||
		got.RequestTimeout != 7*time.Second || got.GateTimeout != 2*time.Second ||
		got.BackoffBase != time.Hour || got.BackoffMaximum != time.Hour || got.MAC.String() != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("Samsung config = %+v", got)
	}
}

func TestSamsungConfigForTVDisablesAmbiguousMultiTVMAC(t *testing.T) {
	cfg := &config.Config{
		TVIPs: []string{"192.0.2.10", "192.0.2.11"}, TVMAC: "AA:BB:CC:DD:EE:FF",
		TokenDir: t.TempDir(), ClientName: "Frame Manager", SyncIntervalMin: 5,
	}
	got, err := samsungConfigForTV(cfg, "192.0.2.10")
	if err != nil {
		t.Fatalf("samsungConfigForTV() error = %v", err)
	}
	if got.MAC != nil {
		t.Fatalf("ambiguous MAC = %v, want disabled", got.MAC)
	}
}

func TestSamsungConfigForTVCanonicalizesIPv6(t *testing.T) {
	stateDir := t.TempDir()
	cfg := &config.Config{
		TVIPs: []string{"2001:db8::1"}, TokenDir: stateDir, ClientName: "Frame Manager",
		SyncIntervalMin: 1, ConnectionTimeout: time.Second, APITimeout: time.Second, GateTimeout: time.Second,
	}
	got, err := samsungConfigForTV(cfg, "2001:0db8:0:0:0:0:0:1")
	if err != nil {
		t.Fatalf("samsungConfigForTV() error = %v", err)
	}
	if got.Address != "2001:db8::1" || got.TokenPath != filepath.Join(stateDir, "tv_2001_db8__1.txt") {
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

func TestSamsungConfigForTVRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *config.Config
		address string
	}{
		{name: "nil configuration", address: "192.0.2.10"},
		{name: "invalid address", cfg: &config.Config{}, address: "television.local"},
		{name: "invalid MAC", cfg: &config.Config{TVIPs: []string{"192.0.2.10"}, TVMAC: "not-a-mac", SyncIntervalMin: 1}, address: "192.0.2.10"},
		{name: "nonpositive interval", cfg: &config.Config{TVIPs: []string{"192.0.2.10"}}, address: "192.0.2.10"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := samsungConfigForTV(testCase.cfg, testCase.address); err == nil {
				t.Fatal("samsungConfigForTV() succeeded, want error")
			}
		})
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
