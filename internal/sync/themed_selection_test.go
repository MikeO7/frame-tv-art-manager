package sync

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/reconcile"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

type themedEngineFixture struct {
	engine   *convergenceEngine
	service  *countingConvergenceService
	store    *themes.Store
	snapshot collectionpkg.Snapshot
}

func newThemedEngineFixture(t *testing.T, timezone string) themedEngineFixture {
	t.Helper()
	root := t.TempDir()
	snapshot := preparedSnapshot(t, root, map[string][]byte{
		"everyday.jpg": createJPEG(t, 32, 18),
		"tree.jpg":     createJPEG(t, 48, 27),
		"snow.jpg":     createJPEG(t, 64, 36),
	})
	store, err := themes.NewStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	service := &countingConvergenceService{result: reconcile.Result{Status: reconcile.StatusComplete}}
	engine := &convergenceEngine{
		cfg:    &config.Config{ArtworkDir: root, SyncIntervalMin: 60, MatteStyle: "none", Timezone: timezone, DryRun: true},
		logger: slog.New(slog.DiscardHandler), health: health.NewStatus(),
		collection: &staticArtworkCollection{snapshot: snapshot}, cycleGate: make(chan struct{}, 1),
		runtimes: []*convergenceRuntime{{address: "192.0.2.10", adapter: &recordingConvergenceAdapter{}, reconciler: service}},
		themes:   store, wake: make(chan struct{}, 1),
	}
	return themedEngineFixture{engine: engine, service: service, store: store, snapshot: snapshot}
}

func (fixture themedEngineFixture) schedule(t *testing.T, activation themes.Activation, exclusive bool, members ...string) {
	t.Helper()
	if _, err := fixture.store.Update(context.Background(), func(file *themes.File) error {
		christmas, err := themes.NewCollection("Christmas", "", exclusive, activation)
		if err != nil {
			return err
		}
		for _, name := range members {
			if _, err := christmas.AddMember(themes.Member{Key: name}); err != nil {
				return err
			}
		}
		file.Collections = []themes.Collection{christmas}
		return nil
	}); err != nil {
		t.Fatalf("schedule collection: %v", err)
	}
}

func itemNames(snapshot collectionpkg.Snapshot) []string {
	names := make([]string, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		names = append(names, item.Name)
	}
	slices.Sort(names)
	return names
}

func TestSelectThemedArtworkFiltersByTheConfiguredCalendarDay(t *testing.T) {
	t.Parallel()
	window := themes.Activation{Mode: themes.ModeWindow, Start: "12-01", End: "01-06"}
	tests := []struct {
		name     string
		timezone string
		instant  time.Time
		want     []string
	}{
		{"inside the window", "UTC", time.Date(2026, time.December, 24, 12, 0, 0, 0, time.UTC), []string{"snow.jpg", "tree.jpg"}},
		{"after the window", "UTC", time.Date(2027, time.January, 7, 12, 0, 0, 0, time.UTC), []string{"everyday.jpg"}},
		{
			"still November in Denver", "America/Denver", time.Date(2026, time.December, 1, 3, 0, 0, 0, time.UTC),
			[]string{"everyday.jpg"},
		},
		{
			"December 1 in Denver", "America/Denver", time.Date(2026, time.December, 1, 8, 0, 0, 0, time.UTC),
			[]string{"snow.jpg", "tree.jpg"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newThemedEngineFixture(t, test.timezone)
			fixture.schedule(t, window, true, "tree.jpg", "snow.jpg")
			desired, err := fixture.engine.selectThemedArtwork(context.Background(), fixture.snapshot, test.instant, fixture.engine.logger)
			if err != nil || desired.degraded != nil {
				t.Fatalf("selectThemedArtwork() = %+v, %v", desired, err)
			}
			if got := itemNames(desired.snapshot); !slices.Equal(got, test.want) {
				t.Fatalf("desired artwork = %v, want %v", got, test.want)
			}
			if err := collectionpkg.ValidateSnapshot(fixture.engine.cfg.ArtworkDir, desired.snapshot); err != nil {
				t.Fatalf("filtered snapshot is invalid: %v", err)
			}
		})
	}
}

func TestSelectThemedArtworkWithoutCollectionsKeepsTheSnapshot(t *testing.T) {
	t.Parallel()
	fixture := newThemedEngineFixture(t, "UTC")
	desired, err := fixture.engine.selectThemedArtwork(context.Background(), fixture.snapshot, time.Now(), fixture.engine.logger)
	if err != nil || desired.snapshot.Generation != fixture.snapshot.Generation || len(desired.snapshot.Items) != 3 {
		t.Fatalf("selectThemedArtwork() = %+v, %v, want the unchanged snapshot", desired, err)
	}
	fixture.engine.themes = nil
	desired, err = fixture.engine.selectThemedArtwork(context.Background(), fixture.snapshot, time.Now(), fixture.engine.logger)
	if err != nil || desired.snapshot.Generation != fixture.snapshot.Generation {
		t.Fatalf("selectThemedArtwork(no store) = %+v, %v", desired, err)
	}
}

func TestRunOnceShowsOnlyActiveExclusiveCollection(t *testing.T) {
	t.Parallel()
	fixture := newThemedEngineFixture(t, "UTC")
	fixture.schedule(t, themes.Activation{Mode: themes.ModeAlways}, true, "tree.jpg")
	if err := fixture.engine.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	request := fixture.service.lastRequest()
	if got := itemNames(request.Snapshot); !slices.Equal(got, []string{"tree.jpg"}) {
		t.Fatalf("reconciled artwork = %v, want only the active collection", got)
	}
	if request.Snapshot.Generation == fixture.snapshot.Generation {
		t.Fatal("filtered snapshot kept the unfiltered generation")
	}
}

func TestRunOnceUnreadableCollectionsFailsSafe(t *testing.T) {
	t.Parallel()
	fixture := newThemedEngineFixture(t, "UTC")
	corrupt := func() {
		if err := os.WriteFile(fixture.store.Path(), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(fixture.store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	corrupt()
	err := fixture.engine.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "before any TV work") || fixture.service.calls() != 0 {
		t.Fatalf("RunOnce(no last good rules) = %v after %d TV runs, want failure before TV work", err, fixture.service.calls())
	}

	if err := os.Remove(fixture.store.Path()); err != nil {
		t.Fatal(err)
	}
	fixture.schedule(t, themes.Activation{Mode: themes.ModeAlways}, true, "tree.jpg")
	if err := fixture.engine.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce(valid rules) error = %v", err)
	}
	corrupt()
	err = fixture.engine.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "keeping the last good selection rules") {
		t.Fatalf("RunOnce(corrupt after good) error = %v, want degraded cycle", err)
	}
	if got := itemNames(fixture.service.lastRequest().Snapshot); !slices.Equal(got, []string{"tree.jpg"}) {
		t.Fatalf("degraded cycle reconciled %v, want the last good selection", got)
	}
	if report := fixture.engine.health.Report(); report.LastSyncOK || !strings.Contains(report.LastError, "unreadable") {
		t.Fatalf("health report = %+v, want the unreadable collections file reported", report)
	}
}

func TestRequestCycleCoalescesAndRunLoopServesIt(t *testing.T) {
	t.Parallel()
	fixture := newThemedEngineFixture(t, "UTC")
	if !fixture.engine.requestCycle() {
		t.Fatal("first requestCycle() was not queued")
	}
	if fixture.engine.requestCycle() {
		t.Fatal("second requestCycle() queued another cycle instead of collapsing")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fixture.engine.RunLoop(ctx) }()
	deadline := time.After(10 * time.Second)
	for fixture.service.calls() < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("RunLoop ran %d cycles, want the initial and the requested cycle", fixture.service.calls())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunLoop() error = %v", err)
	}
	if calls := fixture.service.calls(); calls != 2 {
		t.Fatalf("RunLoop ran %d cycles, want exactly 2 for coalesced requests", calls)
	}
	if peak := fixture.service.peakConcurrency(); peak != 1 {
		t.Fatalf("cycles overlapped: peak concurrency %d", peak)
	}
}

func TestPlacementsFollowTheLatestObservedInventory(t *testing.T) {
	t.Parallel()
	engine := &convergenceEngine{}
	engine.recordPlacements(TVSyncResult{IP: "192.0.2.10", OnTV: []string{"aa", "bb"}})
	engine.recordPlacements(TVSyncResult{IP: "192.0.2.11", OnTV: []string{"bb"}})
	engine.recordPlacements(TVSyncResult{IP: "192.0.2.11"})
	placements := engine.placementsByDigest()
	if !slices.Equal(placements["aa"], []string{"192.0.2.10"}) ||
		!slices.Equal(placements["bb"], []string{"192.0.2.10", "192.0.2.11"}) {
		t.Fatalf("placements = %v", placements)
	}
	engine.recordPlacements(TVSyncResult{IP: "192.0.2.10", OnTV: []string{}})
	if placements := engine.placementsByDigest(); len(placements["aa"]) != 0 {
		t.Fatalf("placements after empty inventory = %v", placements)
	}
}

func TestOwnedOnTVReportsOnlyObservedBindings(t *testing.T) {
	t.Parallel()
	result := reconcile.Result{
		Observation: samsung.Observation{Inventory: samsung.Inventory{Known: true, ContentIDs: []string{"MY_F0001"}}},
		State: reconcile.State{Bindings: map[string]reconcile.Binding{
			"bb": {ContentID: "MY_F0001"}, "aa": {ContentID: "MY_F0002"},
		}},
	}
	if got := ownedOnTV(result); !slices.Equal(got, []string{"bb"}) {
		t.Fatalf("ownedOnTV() = %v, want only the binding present in the inventory", got)
	}
	result.Observation.Inventory.Known = false
	if got := ownedOnTV(result); got != nil {
		t.Fatalf("ownedOnTV(unknown inventory) = %v, want nil", got)
	}
}

type countingConvergenceService struct {
	mu      gosync.Mutex
	request reconcile.Request
	result  reconcile.Result
	count   int
	active  int
	peak    int
}

func (service *countingConvergenceService) Run(_ context.Context, request reconcile.Request) (reconcile.Result, error) {
	service.mu.Lock()
	service.count++
	service.active++
	service.peak = max(service.peak, service.active)
	service.request = request
	service.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	service.mu.Lock()
	defer service.mu.Unlock()
	service.active--
	return service.result, nil
}

func (service *countingConvergenceService) lastRequest() reconcile.Request {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.request
}

func (service *countingConvergenceService) calls() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.count
}

func (service *countingConvergenceService) peakConcurrency() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.peak
}
