package sync

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestTVReconciler_applyCapacityFilter_WhenTVFull(t *testing.T) {
	tmp := t.TempDir()
	capacityFile := filepath.Join(tmp, "cap_test.json")
	if err := os.WriteFile(capacityFile, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed capacity file: %v", err)
	}

	reconciler, err := NewTVReconciler("1.2.3.4", &config.Config{
		TokenDir: tmp,
	}, &config.MatteConfig{}, slog.Default())
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}

	cm := NewCapacityManager(tmp, "1.2.3.4")
	if err := cm.Save(&CapacityState{IsFull: true, MaxImages: 1}); err != nil {
		t.Fatalf("seed capacity: %v", err)
	}

	localFiles := map[string]struct{}{
		"c.jpg": {}, "a.jpg": {}, "b.jpg": {},
	}
	filtered, _ := reconciler.applyCapacityFilter(localFiles)
	if len(filtered) != 1 {
		t.Fatalf("expected filtered files to be limited, got %d", len(filtered))
	}
}

func TestTVReconciler_handleCapacityError_RecordsState(t *testing.T) {
	tmp := t.TempDir()
	reconciler, err := NewTVReconciler("1.2.3.4", &config.Config{
		TokenDir: tmp,
	}, &config.MatteConfig{}, slog.Default())
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}

	capacity := NewCapacityManager(tmp, "1.2.3.4")
	execResult := &TVSyncResult{
		StorageFull:  true,
		Uploaded:     3,
		Deleted:      1,
		ErrorMessage: "storage full",
	}
	plan := &SyncPlan{TrackedFilesCount: 5}
	reconciler.handleCapacityError(execResult, plan, capacity)

	state, err := capacity.Load()
	if err != nil {
		t.Fatalf("load capacity: %v", err)
	}
	if !state.IsFull {
		t.Fatal("expected stored capacity state to remain full")
	}
	if state.MaxImages != 7 {
		t.Fatalf("unexpected max images %d", state.MaxImages)
	}
}
