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

func TestTVReconciler_Reconcile_UploadAndDelete(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{
		artMode: true,
		listContent: []samsung.ArtContent{
			{ContentID: valIDOld},
		},
		uploadContent: valIDNew,
	}

	cfg := &config.Config{
		TokenDir:       tmpDir,
		ArtworkDir:     tmpDir,
		MatteStyle:     "shadowbox_warm",
		DryRun:         false,
		UploadAttempts: 1,
	}

	m, _ := LoadMapping(tmpDir, "1.2.3.4")
	m.Set(testOldJPG, valIDOld)
	_ = m.Save()

	mc := config.LoadMatteConfig(tmpDir)

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	localFiles := map[string]struct{}{
		testNewJPG: {},
	}

	result, err := s.Reconcile(context.Background(), fake, localFiles)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
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

func TestTVReconciler_Reconcile_Backoff(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{skip: true}
	cfg := &config.Config{TokenDir: tmpDir}
	mc := &config.MatteConfig{}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	result, err := s.Reconcile(context.Background(), fake, make(map[string]struct{}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != statusBackoff {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestTVReconciler_Reconcile_RemoveUnknown(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{
		artMode: true,
		listContent: []samsung.ArtContent{
			{ContentID: "id-unknown"},
		},
	}

	cfg := &config.Config{
		TokenDir:            tmpDir,
		RemoveUnknownImages: true,
		DryRun:              false,
	}
	mc := &config.MatteConfig{}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Reconcile(context.Background(), fake, make(map[string]struct{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0][0] != "id-unknown" {
		t.Errorf("deleted = %v", fake.deleted)
	}
}

func TestTVReconciler_Reconcile_GateFailed(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{connectErr: samsung.ErrGateFailed}
	cfg := &config.Config{TokenDir: tmpDir}
	mc := &config.MatteConfig{}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	result, err := s.Reconcile(context.Background(), fake, make(map[string]struct{}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "skipped (gate)" {
		t.Errorf("status = %q", result.Status)
	}
}

func TestTVReconciler_Reconcile_ConnectError(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{connectErr: context.DeadlineExceeded}
	cfg := &config.Config{
		TokenDir:        tmpDir,
		SyncIntervalMin: 1,
	}
	mc := &config.MatteConfig{}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Reconcile(context.Background(), fake, make(map[string]struct{}))
	if err == nil {
		t.Fatal("expected connect error")
	}
	if fake.recordFailures != 1 {
		t.Errorf("recordFailures = %d", fake.recordFailures)
	}
}

func TestTVReconciler_Reconcile_NotArtMode(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{artMode: false}
	cfg := &config.Config{TokenDir: tmpDir}
	mc := &config.MatteConfig{}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	result, err := s.Reconcile(context.Background(), fake, make(map[string]struct{}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "skipped (not art mode)" {
		t.Errorf("status = %q", result.Status)
	}
}

func TestTVReconciler_Reconcile_ListError(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{artMode: true, listErr: context.DeadlineExceeded}
	cfg := &config.Config{
		TokenDir:        tmpDir,
		SyncIntervalMin: 1,
	}
	mc := &config.MatteConfig{}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Reconcile(context.Background(), fake, make(map[string]struct{}))
	if err == nil {
		t.Fatal("expected list error")
	}
}

func TestTVReconciler_Reconcile_DryRun(t *testing.T) {
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
	cfg := &config.Config{
		TokenDir:            tmpDir,
		ArtworkDir:          tmpDir,
		DryRun:              true,
		RemoveUnknownImages: true,
		MatteStyle:          "none",
	}

	m, _ := LoadMapping(tmpDir, "1.2.3.4")
	m.Set("old.jpg", "id-old")
	_ = m.Save()

	mc := &config.MatteConfig{Overrides: map[string]string{"photo.jpg": "modern_apricot"}}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	localFiles := map[string]struct{}{"photo.jpg": {}}

	result, err := s.Reconcile(context.Background(), fake, localFiles)
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

func TestTVReconciler_Reconcile_ShuffleSelection(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{
		artMode: true,
		listContent: []samsung.ArtContent{
			{ContentID: "id-a"},
		},
		uploadContent: "id-b",
	}

	cfg := &config.Config{
		TokenDir:          tmpDir,
		ArtworkDir:        tmpDir,
		MatteStyle:        "none",
		UploadAttempts:    1,
		SlideshowOverride: true,
		SlideshowEnabled:  true,
		SlideshowType:     "shuffle",
		SlideshowInterval: 15,
	}

	m, _ := LoadMapping(tmpDir, "1.2.3.4")
	m.Set("old.jpg", "id-a")
	_ = m.Save()

	mc := &config.MatteConfig{}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	localFiles := map[string]struct{}{"new.jpg": {}}

	_, err = s.Reconcile(context.Background(), fake, localFiles)
	if err != nil {
		t.Fatal(err)
	}
	if fake.selected == "" {
		t.Error("expected image selection in shuffle mode")
	}
}

func TestTVReconciler_Reconcile_AutoOff(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeTVTransport{artMode: true}
	cfg := &config.Config{
		TokenDir:          tmpDir,
		AutoOffTime:       "22:00",
		AutoOffGraceHours: 2.0,
		Timezone:          "America/New_York",
	}
	mc := &config.MatteConfig{}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Reconcile(context.Background(), fake, make(map[string]struct{}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestIsWithinAutoOffWindow(t *testing.T) {
	t.Parallel()

	utc := "UTC"
	time2200 := "22:00"

	tests := []struct {
		name    string
		offTime string
		grace   float64
		tz      string
		now     time.Time
		want    bool
	}{
		{
			name:    "empty off time",
			offTime: "",
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 22, 30, 0, 0, time.UTC),
			want:    false,
		},
		{
			name:    "within window",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 22, 30, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "at exact off time",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 22, 0, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "at exact grace end",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
			want:    false, // exclusive end
		},
		{
			name:    "before window",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 21, 59, 0, 0, time.UTC),
			want:    false,
		},
		{
			name:    "after grace period",
			offTime: time2200,
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 2, 0, 30, 0, 0, time.UTC),
			want:    false,
		},
		{
			name:    "midnight wrap — in yesterday's window",
			offTime: "23:00",
			grace:   3,
			tz:      utc,
			now:     time.Date(2024, 1, 2, 1, 0, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "midnight wrap — past yesterday's grace",
			offTime: "23:00",
			grace:   2,
			tz:      utc,
			now:     time.Date(2024, 1, 2, 1, 30, 0, 0, time.UTC),
			want:    false,
		},
		{
			name:    "fractional grace hours",
			offTime: time2200,
			grace:   1.5,
			tz:      utc,
			now:     time.Date(2024, 1, 1, 23, 20, 0, 0, time.UTC),
			want:    true,
		},
		{
			name:    "invalid timezone returns false",
			offTime: time2200,
			grace:   2,
			tz:      "Invalid/Zone",
			now:     time.Date(2024, 1, 1, 22, 30, 0, 0, time.UTC),
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isWithinAutoOffWindowAt(tc.offTime, tc.grace, tc.tz, tc.now)
			if got != tc.want {
				t.Errorf("isWithinAutoOffWindowAt(%q, %.1f, %q, %v) = %v, want %v",
					tc.offTime, tc.grace, tc.tz, tc.now, got, tc.want)
			}
		})
	}
}

func TestFormatGraceDisplay(t *testing.T) {
	tests := []struct {
		hours float64
		want  string
	}{
		{2.0, "2"},
		{1.5, "1.5"},
		{0.0, "0"},
		{12.0, "12"},
		{0.1, "0.1"},
	}

	for _, tc := range tests {
		got := formatGraceDisplay(tc.hours)
		if got != tc.want {
			t.Errorf("formatGraceDisplay(%.1f) = %q, want %q", tc.hours, got, tc.want)
		}
	}
}

func TestPlanSync(t *testing.T) {
	cfg := &config.Config{
		ArtworkDir:          "/tmp/artwork",
		TokenDir:            "/tmp/tokens",
		MatteStyle:          "none",
		RemoveUnknownImages: true,
	}

	mappingData := map[string]string{
		"keep.jpg":  "id-keep",
		"stale.jpg": "id-stale",
	}

	tvContent := []samsung.ArtContent{
		{ContentID: "id-keep"},
		{ContentID: "id-stale"},
		{ContentID: "id-unknown"},
	}

	localFiles := map[string]struct{}{
		"keep.jpg": {},
		"new.jpg":  {},
	}

	matteCfg := &config.MatteConfig{
		Overrides: map[string]string{
			"new.jpg": "modern_apricot",
		},
	}

	plan := PlanSync(
		"1.2.3.4",
		cfg,
		matteCfg,
		mappingData,
		tvContent,
		localFiles,
		nil,
		slog.Default(),
	)

	if plan.IP != "1.2.3.4" {
		t.Errorf("expected plan IP 1.2.3.4, got %s", plan.IP)
	}

	// Verify uploads
	if len(plan.ToUpload) != 1 || plan.ToUpload[0].Filename != "new.jpg" {
		t.Errorf("expected upload for new.jpg, got %+v", plan.ToUpload)
	}
	if plan.ToUpload[0].Matte != "modern_apricot" {
		t.Errorf("expected matte modern_apricot, got %s", plan.ToUpload[0].Matte)
	}

	// Verify deletions
	if len(plan.ToDeleteIDs) != 1 || plan.ToDeleteIDs[0] != "id-stale" {
		t.Errorf("expected deletion of id-stale, got %v", plan.ToDeleteIDs)
	}
	if len(plan.ToDeleteUnknownIDs) != 1 || plan.ToDeleteUnknownIDs[0] != "id-unknown" {
		t.Errorf("expected deletion of unknown id-unknown, got %v", plan.ToDeleteUnknownIDs)
	}

	if !plan.HasChanges {
		t.Error("expected HasChanges to be true")
	}
}

func TestTVReconciler_Reconcile_Capacity(t *testing.T) {
	tmpDir := t.TempDir()

	// Case 1: Storage full upload error
	fake := &fakeTVTransport{
		artMode:       true,
		uploadErr:     samsung.ErrStorageFull,
		listContent:   []samsung.ArtContent{},
		uploadContent: "",
	}

	cfg := &config.Config{
		TokenDir:       tmpDir,
		ArtworkDir:     tmpDir,
		MatteStyle:     "none",
		DryRun:         false,
		UploadAttempts: 1,
	}

	mc := &config.MatteConfig{}

	s, err := NewTVReconciler("1.2.3.4", cfg, mc, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	localFiles := map[string]struct{}{
		"photo.jpg": {},
	}

	result, err := s.Reconcile(context.Background(), fake, localFiles)
	if err != nil {
		t.Fatalf("unexpected Reconcile error: %v", err)
	}

	if !result.StorageFull {
		t.Error("expected StorageFull to be true on result")
	}

	// Verify CapacityState is full
	capMgr := NewCapacityManager(tmpDir, "1.2.3.4")
	state, err := capMgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !state.IsFull {
		t.Error("expected capacity state to be marked full")
	}

	// Case 2: Success increments success streak
	fakeSuccess := &fakeTVTransport{
		artMode:       true,
		listContent:   []samsung.ArtContent{},
		uploadContent: "id-success",
	}

	state.IsFull = true
	state.MaxImages = 10
	state.SuccessStreak = 8
	if err := capMgr.Save(state); err != nil {
		t.Fatal(err)
	}

	// First success sync
	_, err = s.Reconcile(context.Background(), fakeSuccess, localFiles)
	if err != nil {
		t.Fatal(err)
	}

	state, err = capMgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !state.IsFull || state.SuccessStreak != 9 {
		t.Errorf("expected still full with success streak 9, got %+v", state)
	}

	// Second success sync triggers recovery
	_, err = s.Reconcile(context.Background(), fakeSuccess, localFiles)
	if err != nil {
		t.Fatal(err)
	}

	state, err = capMgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.IsFull || state.SuccessStreak != 0 || state.MaxImages != 15 {
		t.Errorf("expected recovery triggered, got %+v", state)
	}
}
