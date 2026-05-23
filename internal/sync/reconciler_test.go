package sync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

type fakeTVTransport struct {
	skip           bool
	artMode        bool
	uploaded       []string
	deleted        [][]string
	selected       string
	brightness     *int
	slideshowSet   *samsung.SlideshowStatus
	uploadContent  string
	listContent    []samsung.ArtContent
	connectErr     error
	listErr        error
	uploadErr      error
	deleteErr      error
	recordFailures int
	recordSuccess  int
}

func (f *fakeTVTransport) ShouldSkip() bool { return f.skip }

func (f *fakeTVTransport) Connect(context.Context) error { return f.connectErr }

func (f *fakeTVTransport) Close() error { return nil }

func (f *fakeTVTransport) Model() string { return "Fake TV" }

func (f *fakeTVTransport) IsInArtMode(context.Context) bool { return f.artMode }

func (f *fakeTVTransport) SaveMetadata(context.Context) error { return nil }

func (f *fakeTVTransport) ListUploaded(context.Context) ([]samsung.ArtContent, error) {
	return f.listContent, f.listErr
}

func (f *fakeTVTransport) Upload(_ context.Context, filePath, _, _ string) (string, error) {
	if f.uploadErr != nil {
		return "", f.uploadErr
	}
	f.uploaded = append(f.uploaded, filePath)
	if f.uploadContent == "" {
		f.uploadContent = "new-content-id"
	}
	return f.uploadContent, nil
}

func (f *fakeTVTransport) DeleteImages(_ context.Context, ids []string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	copied := append([]string(nil), ids...)
	f.deleted = append(f.deleted, copied)
	return nil
}

func (f *fakeTVTransport) SelectImage(_ context.Context, contentID string) error {
	f.selected = contentID
	return nil
}

func (f *fakeTVTransport) SlideshowStatus(context.Context) (*samsung.SlideshowStatus, error) {
	return &samsung.SlideshowStatus{Value: "15", Type: "shuffleslideshow"}, nil
}

func (f *fakeTVTransport) SetSlideshow(_ context.Context, status samsung.SlideshowStatus) error {
	copyStatus := status
	f.slideshowSet = &copyStatus
	return nil
}

func (f *fakeTVTransport) SetBrightness(_ context.Context, val int) error {
	f.brightness = &val
	return nil
}

func (f *fakeTVTransport) TurnOff(context.Context) error { return nil }

func (f *fakeTVTransport) RecordFailure(_ time.Duration) { f.recordFailures++ }

func (f *fakeTVTransport) RecordSuccess() { f.recordSuccess++ }

func TestReconcileInventory(t *testing.T) {
	mapping := map[string]string{
		"keep.jpg":    "id-keep",
		"stale.jpg":   "id-stale",
		"missing.jpg": "id-missing",
	}
	tvContent := []samsung.ArtContent{
		{ContentID: valIDKeep},
		{ContentID: valIDUnknown},
	}

	tracked, unknown, stale := reconcileInventory(mapping, tvContent, slog.Default())

	if _, ok := tracked["keep.jpg"]; !ok {
		t.Error("expected keep.jpg tracked")
	}
	if _, ok := tracked["stale.jpg"]; ok {
		t.Error("stale.jpg should not be tracked")
	}
	if len(unknown) != 1 {
		t.Fatalf("expected 1 unknown, got %d", len(unknown))
	}
	if _, ok := unknown[valIDUnknown]; !ok {
		t.Error("expected id-unknown")
	}
	if len(stale) != 2 {
		t.Fatalf("expected 2 stale mappings, got %v", stale)
	}
}

func TestDiffSets(t *testing.T) {
	a := map[string]struct{}{testAJPG: {}, testBJPG: {}}
	b := map[string]struct{}{testBJPG: {}, testCJPG: {}}

	got := diffSets(a, b)
	if len(got) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(got))
	}
	if _, ok := got[testAJPG]; !ok {
		t.Error("expected a.jpg in diff")
	}
}

func TestBuildFinalMapping(t *testing.T) {
	base := map[string]string{testOldJPG: valIDOld, testStayJPG: "id-stay"}
	uploads := map[string]string{testNewJPG: valIDNew}
	deleted := []string{testOldJPG}

	got := buildFinalMapping(base, uploads, deleted)
	if _, ok := got[testOldJPG]; ok {
		t.Error("old.jpg should be deleted")
	}
	if got[testNewJPG] != valIDNew {
		t.Errorf("new.jpg = %q", got[testNewJPG])
	}
	if got[testStayJPG] != "id-stay" {
		t.Errorf("stay.jpg = %q", got[testStayJPG])
	}
}

func TestReconciler_Run_UploadAndDelete(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{
		artMode: true,
		listContent: []samsung.ArtContent{
			{ContentID: valIDOld},
		},
		uploadContent: valIDNew,
	}

	policy := config.SyncPolicy{
		ArtworkDir:     tmpDir,
		MatteStyle:     "shadowbox_warm",
		DryRun:         false,
		UploadAttempts: 1,
	}

	req := ReconcileInput{
		LocalFiles: map[string]struct{}{
			testNewJPG: {},
		},
		Mapping: map[string]string{
			testOldJPG: valIDOld,
		},
		MatteOverrides: map[string]string{
			testNewJPG: "modern_apricot",
		},
	}

	r := NewReconciler(slog.Default())
	result, err := r.Run(context.Background(), fake, "1.2.3.4", req, policy)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if result.Status != "ok" {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Uploaded != 1 {
		t.Fatalf("uploaded = %d", result.Uploaded)
	}
	if result.NewUploads["new.jpg"] != "id-new" {
		t.Errorf("NewUploads = %v", result.NewUploads)
	}
	if len(result.DeletedFiles) != 1 || result.DeletedFiles[0] != "old.jpg" {
		t.Errorf("DeletedFiles = %v", result.DeletedFiles)
	}
	if len(fake.uploaded) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(fake.uploaded))
	}
	if len(fake.deleted) != 1 || len(fake.deleted[0]) != 1 || fake.deleted[0][0] != "id-old" {
		t.Errorf("delete calls = %v", fake.deleted)
	}
	if fake.recordSuccess != 1 {
		t.Errorf("recordSuccess = %d", fake.recordSuccess)
	}
}

func TestReconciler_Run_Backoff(t *testing.T) {
	fake := &fakeTVTransport{skip: true}
	r := NewReconciler(slog.Default())

	result, err := r.Run(context.Background(), fake, "1.2.3.4", ReconcileInput{}, config.SyncPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != statusBackoff {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestReconciler_Run_RemoveUnknown(t *testing.T) {
	fake := &fakeTVTransport{
		artMode: true,
		listContent: []samsung.ArtContent{
			{ContentID: "id-unknown"},
		},
	}

	policy := config.SyncPolicy{
		RemoveUnknownImages: true,
		DryRun:              false,
	}

	r := NewReconciler(slog.Default())
	_, err := r.Run(context.Background(), fake, "1.2.3.4", ReconcileInput{
		LocalFiles: map[string]struct{}{},
		Mapping:    map[string]string{},
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0][0] != "id-unknown" {
		t.Errorf("deleted = %v", fake.deleted)
	}
}

func TestReconciler_Run_GateFailed(t *testing.T) {
	fake := &fakeTVTransport{connectErr: samsung.ErrGateFailed}
	r := NewReconciler(slog.Default())
	result, err := r.Run(context.Background(), fake, "1.2.3.4", ReconcileInput{}, config.SyncPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "skipped (gate)" {
		t.Errorf("status = %q", result.Status)
	}
}

func TestReconciler_Run_ConnectError(t *testing.T) {
	fake := &fakeTVTransport{connectErr: context.DeadlineExceeded}
	r := NewReconciler(slog.Default())
	_, err := r.Run(context.Background(), fake, "1.2.3.4", ReconcileInput{}, config.SyncPolicy{SyncIntervalMin: 1})
	if err == nil {
		t.Fatal("expected connect error")
	}
	if fake.recordFailures != 1 {
		t.Errorf("recordFailures = %d", fake.recordFailures)
	}
}

func TestReconciler_Run_NotArtMode(t *testing.T) {
	fake := &fakeTVTransport{artMode: false}
	r := NewReconciler(slog.Default())
	result, err := r.Run(context.Background(), fake, "1.2.3.4", ReconcileInput{}, config.SyncPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "skipped (not art mode)" {
		t.Errorf("status = %q", result.Status)
	}
}

func TestReconciler_Run_ListError(t *testing.T) {
	fake := &fakeTVTransport{artMode: true, listErr: context.DeadlineExceeded}
	r := NewReconciler(slog.Default())
	_, err := r.Run(context.Background(), fake, "1.2.3.4", ReconcileInput{}, config.SyncPolicy{SyncIntervalMin: 1})
	if err == nil {
		t.Fatal("expected list error")
	}
}

func TestReconciler_Run_DryRun(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "photo.jpg")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &fakeTVTransport{
		artMode: true,
		listContent: []samsung.ArtContent{
			{ContentID: "id-old"},
		},
	}
	policy := config.SyncPolicy{
		ArtworkDir:          tmpDir,
		DryRun:              true,
		RemoveUnknownImages: true,
		MatteStyle:          "none",
	}
	req := ReconcileInput{
		LocalFiles: map[string]struct{}{"photo.jpg": {}},
		Mapping:    map[string]string{"old.jpg": "id-old"},
		MatteOverrides: map[string]string{
			"photo.jpg": "modern_apricot",
		},
		DesiredBrightness: intPtr(6),
		Slideshow:         &samsung.SlideshowStatus{Value: "15", Type: "shuffleslideshow"},
	}

	r := NewReconciler(slog.Default())
	result, err := r.Run(context.Background(), fake, "1.2.3.4", req, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Uploaded != 1 {
		t.Errorf("uploaded = %d", result.Uploaded)
	}
	if result.Deleted != 1 {
		t.Errorf("deleted = %d", result.Deleted)
	}
}

func TestReconciler_Run_ShuffleSelection(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{
		artMode: true,
		listContent: []samsung.ArtContent{
			{ContentID: "id-a"},
		},
		uploadContent: "id-b",
	}
	policy := config.SyncPolicy{
		ArtworkDir:     tmpDir,
		MatteStyle:     "none",
		UploadAttempts: 1,
	}
	req := ReconcileInput{
		LocalFiles: map[string]struct{}{"new.jpg": {}},
		Mapping:    map[string]string{"old.jpg": "id-a"},
		MatteOverrides: map[string]string{
			"new.jpg": "none",
		},
		Slideshow: &samsung.SlideshowStatus{Value: "15", Type: "shuffleslideshow"},
	}

	r := NewReconciler(slog.Default())
	_, err := r.Run(context.Background(), fake, "1.2.3.4", req, policy)
	if err != nil {
		t.Fatal(err)
	}
	if fake.selected == "" {
		t.Error("expected image selection in shuffle mode")
	}
}

func TestReconciler_Run_AutoOff(t *testing.T) {
	fake := &fakeTVTransport{artMode: true}
	r := NewReconciler(slog.Default())
	_, err := r.Run(context.Background(), fake, "1.2.3.4", ReconcileInput{TriggerAutoOff: true}, config.SyncPolicy{})
	if err != nil {
		t.Fatal(err)
	}
}

func intPtr(v int) *int {
	return &v
}
