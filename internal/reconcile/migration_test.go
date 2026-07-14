package reconcile

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestRunMigratesLegacyOwnedArtworkBeforePlanning(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	item := artworkItem("monet.jpg", "committed monet bytes")
	snapshot := snapshot(item)
	writeLegacyMapping(t, directory, map[string]string{"monet.jpg": "content-1"})
	adapter := &fakeAdapter{observations: []samsung.Observation{
		knownObservation(samsung.PowerStateOn, "content-1"),
	}}
	service, err := New(Config{
		StateDirectory: directory, LegacyMappingDirectory: directory,
	}, Dependencies{Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "migration-cycle", TV: adapter, Snapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != StatusComplete || result.Applied != 0 || adapter.applyCalls != 0 {
		t.Fatalf("result/apply calls = %+v/%d", result, adapter.applyCalls)
	}
	digest := hex.EncodeToString(item.Digest[:])
	binding, exists := result.State.Bindings[digest]
	if !exists || binding.ContentID != "content-1" || binding.Name != item.Name ||
		binding.CollectionGeneration != snapshot.Generation || binding.ConfirmedAt != testTime {
		t.Fatalf("migrated binding = %+v, exists = %v", binding, exists)
	}
	if _, err := os.Stat(newStateStore(directory).path(result.State.TV)); err != nil {
		t.Fatalf("new reconciliation state was not persisted: %v", err)
	}
	if _, err := os.Stat(legacyMappingPath(directory, "192.0.2.10")); err != nil {
		t.Fatalf("legacy mapping was removed: %v", err)
	}
}

func TestPowerOnFirstRunWakesThenMigratesBeforeCollectionPlanning(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	item := artworkItem("monet.jpg", "committed monet bytes")
	writeLegacyMapping(t, directory, map[string]string{"monet.jpg": "content-1"})
	adapter := &fakeAdapter{
		observations: []samsung.Observation{
			wakeObservation(samsung.SupportSupported),
			knownObservation(samsung.PowerStateOn, "content-1"),
		},
		receipts: []samsung.Receipt{{
			CommandID: "wake", Outcome: samsung.OutcomeApplied, CompletedAt: testTime,
		}},
	}
	service, err := New(Config{
		StateDirectory: directory, LegacyMappingDirectory: directory, Policy: Policy{Power: PowerOn},
	}, Dependencies{Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "wake-then-migrate", TV: adapter, Snapshot: snapshot(item),
	})
	if err != nil || result.Status != StatusComplete || result.Applied != 1 {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if !slices.Equal(adapter.commands, []string{"samsung.Wake"}) {
		t.Fatalf("commands = %v, want wake without duplicate upload", adapter.commands)
	}
	digest := hex.EncodeToString(item.Digest[:])
	if result.State.Bindings[digest].ContentID != "content-1" || result.State.Pending != nil {
		t.Fatalf("post-wake migrated state = %+v", result.State)
	}
}

func TestPowerOnUnknownWakeRecoversThenMigratesBeforePlanning(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	item := artworkItem("monet.jpg", "committed monet bytes")
	writeLegacyMapping(t, directory, map[string]string{"monet.jpg": "content-1"})
	firstAdapter := &fakeAdapter{
		observations: []samsung.Observation{wakeObservation(samsung.SupportSupported)},
		receipts: []samsung.Receipt{{
			CommandID: "wake", Outcome: samsung.OutcomeUnknown, CompletedAt: testTime,
		}},
		applyErrs: []error{errors.New("wake acknowledgement lost")},
	}
	newService := func(adapterClock Clock) Service {
		service, newErr := New(Config{
			StateDirectory: directory, LegacyMappingDirectory: directory, Policy: Policy{Power: PowerOn},
		}, Dependencies{Clock: adapterClock, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler)})
		if newErr != nil {
			t.Fatal(newErr)
		}
		return service
	}
	first := newService(fixedClock{testTime})
	result, err := first.Run(context.Background(), Request{
		CycleID: "wake-before-migration-crash", TV: firstAdapter, Snapshot: snapshot(item),
	})
	if !errors.Is(err, ErrRecoveryRequired) || result.State.Pending == nil ||
		!result.State.LegacyMigrationPending {
		t.Fatalf("first Run() = %+v, %v", result, err)
	}

	restartAdapter := &fakeAdapter{observations: []samsung.Observation{
		knownObservation(samsung.PowerStateOn),
		knownObservation(samsung.PowerStateOn, "content-1"),
	}}
	restarted := newService(fixedClock{testTime.Add(time.Minute)})
	result, err = restarted.Run(context.Background(), Request{
		CycleID: "recover-wake-then-migrate", TV: restartAdapter, Snapshot: snapshot(item),
	})
	if err != nil || result.Status != StatusComplete || result.Applied != 0 ||
		result.State.Pending != nil || result.State.LegacyMigrationPending {
		t.Fatalf("restart Run() = %+v, %v", result, err)
	}
	if restartAdapter.applyCalls != 0 {
		t.Fatalf("restart repeated mutation; Apply() calls = %d", restartAdapter.applyCalls)
	}
	digest := hex.EncodeToString(item.Digest[:])
	if result.State.Bindings[digest].ContentID != "content-1" {
		t.Fatalf("restart migrated bindings = %+v", result.State.Bindings)
	}
}

func TestPowerOnLegacyMigrationDryRunDoesNotWriteState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	item := artworkItem("monet.jpg", "committed monet bytes")
	writeLegacyMapping(t, directory, map[string]string{"monet.jpg": "content-1"})
	adapter := &fakeAdapter{observations: []samsung.Observation{wakeObservation(samsung.SupportSupported)}}
	service, err := New(Config{
		StateDirectory: directory, LegacyMappingDirectory: directory, Policy: Policy{Power: PowerOn},
	}, Dependencies{Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background(), Request{
		CycleID: "dry-wake-before-migration", TV: adapter, Snapshot: snapshot(item), DryRun: true,
	})
	if err != nil || result.Status != StatusIncompleteDryRun || adapter.applyCalls != 0 {
		t.Fatalf("Run() = %+v, %v; apply calls = %d", result, err, adapter.applyCalls)
	}
	identity, _ := identityFromObservation(adapter.observations[0])
	if _, exists, loadErr := newStateStore(directory).load(context.Background(), identity); loadErr != nil || exists {
		t.Fatalf("dry-run durable state exists=%v, error=%v", exists, loadErr)
	}
}

func TestRunProjectsLegacyMigrationWithoutWritingDuringDryRun(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	item := artworkItem("monet.jpg", "committed monet bytes")
	writeLegacyMapping(t, directory, map[string]string{"monet.jpg": "content-1"})
	adapter := &fakeAdapter{observations: []samsung.Observation{
		knownObservation(samsung.PowerStateOn, "content-1"),
	}}
	service, err := New(Config{
		StateDirectory: directory, LegacyMappingDirectory: directory,
	}, Dependencies{Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "dry-migration", TV: adapter, Snapshot: snapshot(item), DryRun: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != StatusIncompleteDryRun || len(result.State.Bindings) != 1 || adapter.applyCalls != 0 {
		t.Fatalf("dry-run result/apply calls = %+v/%d", result, adapter.applyCalls)
	}
	if _, err := os.Stat(newStateStore(directory).path(result.State.TV)); !os.IsNotExist(err) {
		t.Fatalf("dry-run reconciliation state stat error = %v, want not-exist", err)
	}
}

func TestRunExistingReconciliationStateWinsOverCorruptLegacyMapping(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	observation := knownObservation(samsung.PowerStateOn)
	first := newTestService(t, directory, Policy{})
	if _, err := first.Run(context.Background(), Request{
		CycleID: "establish-state", TV: &fakeAdapter{observations: []samsung.Observation{observation}}, Snapshot: snapshot(),
	}); err != nil {
		t.Fatalf("establish reconciliation state: %v", err)
	}
	legacyPath := legacyMappingPath(directory, "192.0.2.10")
	if err := os.WriteFile(legacyPath, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{
		StateDirectory: directory, LegacyMappingDirectory: directory,
	}, Dependencies{Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "ignore-legacy", TV: &fakeAdapter{observations: []samsung.Observation{observation}}, Snapshot: snapshot(),
	})
	if err != nil || result.Status != StatusComplete {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
}

func TestRunCorruptLegacyMappingBlocksReconciliation(t *testing.T) {
	tests := []struct {
		name  string
		write func(*testing.T, string)
	}{
		{name: "invalid JSON", write: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"broken":`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "trailing JSON", write: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{} {}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "group readable", write: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", write: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "mapping-target")
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unsafe filename", write: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"../art.jpg":"content"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "duplicate content ID", write: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"a.jpg":"content","b.jpg":"content"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "duplicate filename", write: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"a.jpg":"content-1","a.jpg":"content-2"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			test.write(t, legacyMappingPath(directory, "192.0.2.10"))
			adapter := &fakeAdapter{observations: []samsung.Observation{knownObservation(samsung.PowerStateOn)}}
			service, err := New(Config{
				StateDirectory: directory, LegacyMappingDirectory: directory,
			}, Dependencies{Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler)})
			if err != nil {
				t.Fatal(err)
			}

			_, err = service.Run(context.Background(), Request{
				CycleID: "blocked-migration", TV: adapter, Snapshot: snapshot(),
			})
			if err == nil || !strings.Contains(err.Error(), "legacy mapping") || adapter.applyCalls != 0 {
				t.Fatalf("Run() error/apply calls = %v/%d", err, adapter.applyCalls)
			}
		})
	}
}

func TestRunRecoversLegacyMappingFromValidBackup(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	item := artworkItem("monet.jpg", "committed monet bytes")
	primary := legacyMappingPath(directory, "192.0.2.10")
	if err := os.WriteFile(primary, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLegacyMappingFile(t, primary+".bak", map[string]string{"monet.jpg": "content-1"})
	adapter := &fakeAdapter{observations: []samsung.Observation{
		knownObservation(samsung.PowerStateOn, "content-1"),
	}}
	service, err := New(Config{
		StateDirectory: directory, LegacyMappingDirectory: directory,
	}, Dependencies{Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "backup-migration", TV: adapter, Snapshot: snapshot(item),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	digest := hex.EncodeToString(item.Digest[:])
	if result.State.Bindings[digest].ContentID != "content-1" || adapter.applyCalls != 0 {
		t.Fatalf("result/apply calls = %+v/%d", result, adapter.applyCalls)
	}
	if _, err := os.Stat(primary + ".bak"); err != nil {
		t.Fatalf("legacy backup was removed: %v", err)
	}
}

func TestRunLogsIgnoredLegacyMappingEntries(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	item := artworkItem("monet.jpg", "committed monet bytes")
	writeLegacyMapping(t, directory, map[string]string{
		"monet.jpg":   "content-1",
		"missing.jpg": "content-2",
		"stale.jpg":   "content-stale",
	})
	var logs bytes.Buffer
	service, err := New(Config{
		StateDirectory: directory, LegacyMappingDirectory: directory,
	}, Dependencies{
		Clock: fixedClock{testTime}, IDs: &sequenceIDs{},
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Run(context.Background(), Request{
		CycleID: "ignored-migration", TV: &fakeAdapter{observations: []samsung.Observation{
			knownObservation(samsung.PowerStateOn, "content-1", "content-2"),
		}}, Snapshot: snapshot(item),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.State.Bindings) != 1 {
		t.Fatalf("bindings = %d, want 1", len(result.State.Bindings))
	}
	if got := logs.String(); !strings.Contains(got, `"bindings":1`) || !strings.Contains(got, `"ignored_entries":2`) {
		t.Fatalf("migration log = %s", got)
	}
}

func TestRunDoesNotApplyWhenMigratedStateCannotBePersisted(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	migrated := artworkItem("monet.jpg", "committed monet bytes")
	missing := artworkItem("vangogh.jpg", "committed vangogh bytes")
	writeLegacyMapping(t, directory, map[string]string{"monet.jpg": "content-1"})
	adapter := &fakeAdapter{observations: []samsung.Observation{
		knownObservation(samsung.PowerStateOn, "content-1"),
	}}
	created, err := New(Config{
		StateDirectory: directory, LegacyMappingDirectory: directory,
	}, Dependencies{Clock: fixedClock{testTime}, IDs: &sequenceIDs{}, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("injected migration persistence failure")
	created.(*service).store.replace = func(context.Context, string, os.FileMode, func(io.Writer) error) error {
		return persistErr
	}

	_, err = created.Run(context.Background(), Request{
		CycleID: "failed-persistence", TV: adapter, Snapshot: snapshot(migrated, missing),
	})
	if !errors.Is(err, persistErr) {
		t.Fatalf("Run() error = %v, want %v", err, persistErr)
	}
	if adapter.applyCalls != 0 || adapter.observeCalls != 1 {
		t.Fatalf("apply/observe calls = %d/%d, want 0/1", adapter.applyCalls, adapter.observeCalls)
	}
}

func writeLegacyMapping(t *testing.T, directory string, mapping map[string]string) {
	t.Helper()
	path := legacyMappingPath(directory, "192.0.2.10")
	writeLegacyMappingFile(t, path, mapping)
}

func writeLegacyMappingFile(t *testing.T, path string, mapping map[string]string) {
	t.Helper()
	raw, err := json.Marshal(mapping)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
