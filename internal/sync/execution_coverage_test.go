package sync

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

func TestTVReconciler_processUploads_StorageFull(t *testing.T) {
	cfg := &config.Config{
		TokenDir: t.TempDir(),
	}
	reconciler, err := NewTVReconciler("1.2.3.4", cfg, &config.MatteConfig{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	mapping, err := LoadMapping(cfg.TokenDir, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	execCtx := &executionContext{
		ctx: ctx,
		plan: &SyncPlan{
			ToUpload: []UploadJob{
				{Filename: "full.jpg", FilePath: "full.jpg", FileType: "jpg"},
			},
		},
		transport: &mockTVTransportExecution{
			uploadErr: samsung.ErrStorageFull,
		},
		mapping: mapping,
		policy:  config.SyncPolicy{UploadAttempts: 1},
		result:  &TVSyncResult{},
	}

	if err := reconciler.processUploads(execCtx); err != nil {
		t.Fatalf("processUploads() = %v", err)
	}
	if !execCtx.result.StorageFull {
		t.Fatal("expected storage-full flag on result")
	}
}

func TestTVReconciler_processUploads_UploadFailure(t *testing.T) {
	cfg := &config.Config{
		TokenDir: t.TempDir(),
	}
	reconciler, err := NewTVReconciler("1.2.3.4", cfg, &config.MatteConfig{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	mapping, err := LoadMapping(cfg.TokenDir, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	execCtx := &executionContext{
		ctx: ctx,
		plan: &SyncPlan{
			ToUpload: []UploadJob{
				{Filename: "fail.jpg", FilePath: "fail.jpg", FileType: "jpg"},
			},
		},
		transport: &mockTVTransportExecution{
			uploadErr: errors.New("upload failed"),
		},
		mapping: mapping,
		policy:  config.SyncPolicy{UploadAttempts: 1},
		result:  &TVSyncResult{},
	}

	if err := reconciler.processUploads(execCtx); err == nil {
		t.Fatal("expected upload error")
	}
}

func TestTVReconciler_processUploads_PersistFailure(t *testing.T) {
	cfg := &config.Config{
		TokenDir: t.TempDir(),
	}
	reconciler, err := NewTVReconciler("1.2.3.4", cfg, &config.MatteConfig{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	blocker := filepath.Join(cfg.TokenDir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	execCtx := &executionContext{
		ctx: ctx,
		plan: &SyncPlan{
			ToUpload: []UploadJob{
				{Filename: "persist-fail.jpg", FilePath: "persist-fail.jpg", FileType: "jpg"},
			},
		},
		transport: &mockTVTransportExecution{
			uploadId: "id-new",
		},
		mapping: &Mapping{
			path: filepath.Join(blocker, "mapping.json"),
			data: map[string]string{},
		},
		policy: config.SyncPolicy{UploadAttempts: 1},
		result: &TVSyncResult{},
	}

	if err := reconciler.processUploads(execCtx); err == nil {
		t.Fatal("expected persist failure")
	}
}

func TestTVReconciler_deleteTrackedImages_AndDeleteUnknownImages_ErrorPaths(t *testing.T) {
	cfg := &config.Config{
		TokenDir: t.TempDir(),
	}
	reconciler, err := NewTVReconciler("1.2.3.4", cfg, &config.MatteConfig{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	plan := &SyncPlan{
		ToDeleteIDs:  []string{"id-keep"},
		ToDeleteFiles: []string{"old.jpg"},
	}
	execCtx := &executionContext{
		ctx: context.Background(),
		plan: plan,
		transport: &mockTVTransportExecution{
			deleteErr: errors.New("delete failed"),
		},
		mapping:  mapNoOp(),
		policy:   config.SyncPolicy{UploadAttempts: 1},
		result:   &TVSyncResult{},
	}
	if err := reconciler.deleteTrackedImages(execCtx); err == nil {
		t.Fatal("expected tracked deletion error")
	}

	execCtx.plan = &SyncPlan{ToDeleteUnknownIDs: []string{"id-unknown"}}
	execCtx.result = &TVSyncResult{}
	execCtx.transport = &mockTVTransportExecution{deleteErr: errors.New("unknown delete failed")}
	if err := reconciler.deleteUnknownImages(execCtx); err == nil {
		t.Fatal("expected unknown deletion error")
	}
}

func TestTVReconciler_uploadWithRetry_ContextCancelled(t *testing.T) {
	cfg := &config.Config{UploadAttempts: 2, UploadDelay: 10 * time.Millisecond}
	transport := &mockTVTransportExecution{
		uploadErr: errors.New("upload failed"),
	}

	reconciler, err := NewTVReconciler("1.2.3.4", cfg, &config.MatteConfig{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	job := UploadJob{Filename: "a.jpg", FilePath: "a.jpg", FileType: "jpg"}
	if _, err := reconciler.uploadWithRetry(ctx, transport, job, cfg.SyncPolicy()); !errors.Is(err, context.Canceled) {
		t.Fatalf("uploadWithRetry() = %v", err)
	}
}

func mapNoOp() *Mapping {
	return &Mapping{
		path: filepath.Join(os.TempDir(), "unused_mapping.json"),
		data: map[string]string{},
	}
}
