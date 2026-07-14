package sync

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestConvergenceEngineRunsCommittedSnapshotThroughCachedRuntime(t *testing.T) {
	root := t.TempDir()
	snapshot := preparedSnapshot(t, root, nil)
	adapter := &recordingConvergenceAdapter{}
	service := &recordingConvergenceService{result: reconcile.Result{
		Status: reconcile.StatusComplete,
		AppliedCommands: []reconcile.CommandKind{
			reconcile.CommandUpload, reconcile.CommandDeleteUnknown,
		},
		Observation: samsung.Observation{
			TV:    samsung.TVIdentity{Address: "192.0.2.10", Model: "Frame", Known: true},
			Power: samsung.PowerStateOn, ArtMode: samsung.ArtModeOn,
			Inventory: samsung.Inventory{Known: true, ContentIDs: []string{"one", "two"}},
		},
	}}
	healthStatus := health.NewStatus()
	engine := &convergenceEngine{
		cfg: &config.Config{
			ArtworkDir: root, SyncIntervalMin: 5, DryRun: true,
			RemoveUnknownImages: true, MatteStyle: "none",
		},
		logger: slog.New(slog.DiscardHandler), health: healthStatus,
		collection: &staticArtworkCollection{snapshot: snapshot}, cycleGate: make(chan struct{}, 1),
		runtimes: []*convergenceRuntime{{
			address: "192.0.2.10", adapter: adapter, reconciler: service,
		}},
	}

	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	request := service.lastRequest()
	if request.CycleID != "cycle-1-192.0.2.10" || request.Snapshot.Generation != snapshot.Generation ||
		!request.DryRun || !request.Policy.RemoveUnknown || request.Policy.Select {
		t.Fatalf("reconciliation request = %+v", request)
	}
	if engine.cycleNum != 1 {
		t.Fatalf("cycle number = %d, want 1", engine.cycleNum)
	}
	if service.result.AppliedCommands[0] != reconcile.CommandUpload {
		t.Fatal("runtime mutated reconciler result command history")
	}
}

func TestNewConvergenceEngineRejectsDuplicateCanonicalTVsBeforeConstruction(t *testing.T) {
	root := t.TempDir()
	_, err := newConvergenceEngine(context.Background(), &config.Config{
		TVIPs: []string{"2001:db8::1", "2001:0db8:0:0:0:0:0:1"}, ArtworkDir: root,
	}, slog.New(slog.DiscardHandler), nil, &staticArtworkCollection{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("newConvergenceEngine() error = %v, want duplicate rejection", err)
	}
}

func TestConvergenceRuntimeTreatsAdapterBackoffAsDegradedSkip(t *testing.T) {
	backoff := &samsung.Error{
		Kind: samsung.ErrorKindBackoff, Operation: "observe", Retryable: true,
		Outcome: samsung.OutcomeNotAttempted, Cause: errors.New("retry later"),
	}
	service := &recordingConvergenceService{
		result: reconcile.Result{Observation: samsung.Observation{Disposition: samsung.DispositionBlockedBackoff}},
		err:    backoff,
	}
	runtime := &convergenceRuntime{
		address: "192.0.2.10", adapter: &recordingConvergenceAdapter{}, reconciler: service,
	}

	summary, err := runtime.run(context.Background(), "cycle", collectionpkg.Snapshot{}, reconcile.Policy{}, false)
	if !errors.Is(err, backoff) || summary.Status != statusBackoff || summary.ErrorMessage == "" {
		t.Fatalf("run() = %+v, %v, want degraded backoff", summary, err)
	}
}

func TestConvergenceSummaryReportsExactAppliedMutationCounts(t *testing.T) {
	t.Parallel()
	result := reconcile.Result{
		Status: reconcile.StatusComplete,
		AppliedCommands: []reconcile.CommandKind{
			reconcile.CommandWake,
			reconcile.CommandUpload,
			reconcile.CommandDeleteOwned,
			reconcile.CommandDeleteUnknown,
			reconcile.CommandBrightness,
		},
		Observation: samsung.Observation{
			Inventory:  samsung.Inventory{ContentIDs: []string{"remaining"}, Known: true},
			Brightness: samsung.SettingObservation{Value: 8, Known: true},
		},
	}
	summary := convergenceSummary("192.0.2.10", result)
	if summary.Uploaded != 1 || summary.Deleted != 2 || summary.TotalImages != 1 ||
		summary.Brightness != "8" || summary.Status != "ok" {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestConvergenceStatusProjectionIsExhaustive(t *testing.T) {
	t.Parallel()

	errorCases := []struct {
		name        string
		status      reconcile.Status
		hasError    bool
		samsungKind samsung.ErrorKind
		wantStatus  string
		storageFull bool
	}{
		{name: "recovery", status: reconcile.StatusRecoveryRequired, wantStatus: statusRecoveryRequired},
		{name: "persistence", status: reconcile.StatusPersistenceUnknown, wantStatus: statusPersistenceUnknown},
		{name: "unsupported", status: reconcile.StatusUnsupported, wantStatus: statusUnsupported},
		{name: "generic", wantStatus: statusError},
		{name: "unauthorized", hasError: true, samsungKind: samsung.ErrorKindUnauthorized, wantStatus: "authorization required"},
		{name: "storage", hasError: true, samsungKind: samsung.ErrorKindStorageFull, wantStatus: "storage full", storageFull: true},
		{name: "unknown outcome", hasError: true, samsungKind: samsung.ErrorKindOutcomeUnknown, wantStatus: statusRecoveryRequired},
		{name: "unreachable", hasError: true, samsungKind: samsung.ErrorKindUnreachable, wantStatus: statusUnreachable},
		{name: "timeout", hasError: true, samsungKind: samsung.ErrorKindTimeout, wantStatus: statusUnreachable},
		{name: "canceled", hasError: true, samsungKind: samsung.ErrorKindCanceled, wantStatus: statusCanceled},
		{name: "other adapter error", hasError: true, samsungKind: samsung.ErrorKindProtocol, wantStatus: statusError},
	}
	for _, testCase := range errorCases {
		t.Run(testCase.name, func(t *testing.T) {
			var adapterErr *samsung.Error
			if testCase.hasError {
				adapterErr = &samsung.Error{Kind: testCase.samsungKind}
			}
			got, storageFull := convergenceErrorStatus(testCase.status, adapterErr)
			if got != testCase.wantStatus || storageFull != testCase.storageFull {
				t.Fatalf("convergenceErrorStatus() = %q, %v; want %q, %v", got, storageFull, testCase.wantStatus, testCase.storageFull)
			}
		})
	}

	summaryCases := []struct {
		name        string
		status      reconcile.Status
		disposition samsung.Disposition
		want        string
	}{
		{name: "dry run", status: reconcile.StatusIncompleteDryRun, want: "dry-run"},
		{name: "unsupported", status: reconcile.StatusUnsupported, want: statusUnsupported},
		{name: "recovery", status: reconcile.StatusRecoveryRequired, want: statusRecoveryRequired},
		{name: "persistence", status: reconcile.StatusPersistenceUnknown, want: statusPersistenceUnknown},
		{name: "not applied", status: reconcile.StatusNotApplied, want: statusNotApplied},
		{name: "unknown", status: reconcile.Status(255), want: statusError},
		{name: "backoff", status: reconcile.StatusKnownSkip, disposition: samsung.DispositionBlockedBackoff, want: statusBackoff},
		{name: "not art mode", status: reconcile.StatusKnownSkip, disposition: samsung.DispositionBlockedNotArtMode, want: statusSkippedNotArtMode},
		{name: "powered off", status: reconcile.StatusKnownSkip, disposition: samsung.DispositionBlockedPowerOff, want: "skipped (powered off)"},
		{name: "quiet gate", status: reconcile.StatusKnownSkip, disposition: samsung.DispositionBlockedQuietGate, want: "skipped (quiet gate)"},
		{name: "unsafe state", status: reconcile.StatusKnownSkip, want: "skipped (unsafe TV state)"},
	}
	for _, testCase := range summaryCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := reconcile.Result{Status: testCase.status, Observation: samsung.Observation{
				Disposition: testCase.disposition,
				Slideshow:   samsung.SlideshowObservation{Known: true, Setting: samsung.SlideshowSetting{}},
			}}
			if got := convergenceSummary("192.0.2.10", result); got.Status != testCase.want || got.Slideshow != "off" {
				t.Fatalf("convergenceSummary() = %+v, want status %q and disabled slideshow", got, testCase.want)
			}
		})
	}

	active := convergenceSummary("192.0.2.10", reconcile.Result{Observation: samsung.Observation{
		Slideshow: samsung.SlideshowObservation{Known: true, Setting: samsung.SlideshowSetting{Kind: "daily", Interval: 15}},
	}})
	if active.Slideshow != "daily every 15 min" {
		t.Fatalf("active slideshow = %q", active.Slideshow)
	}
}

func TestConvergenceEngineCloseIsContextAwareAndIdempotent(t *testing.T) {
	adapter := &recordingConvergenceAdapter{}
	engine := &convergenceEngine{
		cfg:      &config.Config{},
		runtimes: []*convergenceRuntime{{address: "192.0.2.10", adapter: adapter}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.Close(ctx); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := engine.Close(ctx); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closeCalls != 1 || adapter.closeContext != ctx {
		t.Fatalf("close calls = %d, context identity = %v", adapter.closeCalls, adapter.closeContext == ctx)
	}
}

type staticArtworkCollection struct {
	snapshot collectionpkg.Snapshot
	err      error
}

func (collection *staticArtworkCollection) prepareCycle(context.Context) (preparedCollection, error) {
	return preparedCollection{snapshot: collection.snapshot}, collection.err
}

func (collection *staticArtworkCollection) Prepare(
	context.Context,
	collectionpkg.PrepareRequest,
) (collectionpkg.Snapshot, error) {
	return collection.snapshot, collection.err
}

func (collection *staticArtworkCollection) Import(
	context.Context,
	collectionpkg.ImportRequest,
) (collectionpkg.Snapshot, error) {
	return collection.snapshot, collection.err
}

func (collection *staticArtworkCollection) Apply(
	context.Context,
	collectionpkg.ApplyRequest,
) (collectionpkg.Snapshot, error) {
	return collection.snapshot, collection.err
}

type recordingConvergenceService struct {
	mu      sync.Mutex
	request reconcile.Request
	result  reconcile.Result
	err     error
}

func (service *recordingConvergenceService) Run(_ context.Context, request reconcile.Request) (reconcile.Result, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.request = request
	return service.result, service.err
}

func (service *recordingConvergenceService) lastRequest() reconcile.Request {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.request
}

type recordingConvergenceAdapter struct {
	mu           sync.Mutex
	closeCalls   int
	closeContext context.Context
	closeErr     error
}

func (*recordingConvergenceAdapter) Observe(
	context.Context,
	samsung.ObserveRequest,
) (samsung.Observation, error) {
	return samsung.Observation{}, errors.New("unexpected observation")
}

func (*recordingConvergenceAdapter) Apply(
	context.Context,
	samsung.Authorization,
	samsung.Command,
) (samsung.Receipt, error) {
	return samsung.Receipt{}, errors.New("unexpected mutation")
}

func (adapter *recordingConvergenceAdapter) Close(ctx context.Context) error {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.closeCalls++
	adapter.closeContext = ctx
	return adapter.closeErr
}

func preparedSnapshot(t *testing.T, root string, files map[string][]byte) collectionpkg.Snapshot {
	t.Helper()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatalf("write artwork %q: %v", name, err)
		}
	}
	store, err := collectionpkg.New(collectionpkg.Config{Root: root})
	if err != nil {
		t.Fatalf("new collection: %v", err)
	}
	snapshot, err := store.Prepare(context.Background(), collectionpkg.PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare collection: %v", err)
	}
	return snapshot
}
