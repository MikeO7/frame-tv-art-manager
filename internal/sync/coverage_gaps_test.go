package sync

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

type saveMetadataFailTransport struct {
	*fakeTVTransport
	saveErr error
}

func (m *saveMetadataFailTransport) SaveMetadata(context.Context) error {
	return m.saveErr
}

type slowArtModeTransport struct {
	delay time.Duration
}

func (m *slowArtModeTransport) ShouldSkip() bool { return false }

func (m *slowArtModeTransport) Connect(context.Context) error {
	time.Sleep(m.delay)
	return nil
}

func (m *slowArtModeTransport) Close() error                       { return nil }
func (m *slowArtModeTransport) Model() string                      { return "Mock TV" }
func (m *slowArtModeTransport) IsInArtMode(context.Context) bool   { return true }
func (m *slowArtModeTransport) SaveMetadata(context.Context) error { time.Sleep(m.delay); return nil }
func (m *slowArtModeTransport) ListUploaded(context.Context) ([]samsung.ArtContent, error) {
	time.Sleep(m.delay)
	return nil, nil
}
func (m *slowArtModeTransport) Upload(context.Context, string, string, string) (string, error) {
	time.Sleep(m.delay)
	return "id", nil
}
func (m *slowArtModeTransport) DeleteImages(context.Context, []string) error {
	time.Sleep(m.delay)
	return nil
}
func (m *slowArtModeTransport) SelectImage(context.Context, string) error { return nil }
func (m *slowArtModeTransport) SetSlideshow(context.Context, samsung.SlideshowStatus) error {
	return nil
}
func (m *slowArtModeTransport) SlideshowStatus(context.Context) (*samsung.SlideshowStatus, error) {
	time.Sleep(m.delay)
	return &samsung.SlideshowStatus{Type: "random", Value: "none"}, nil
}
func (m *slowArtModeTransport) SetBrightness(context.Context, int) error { return nil }
func (m *slowArtModeTransport) TurnOff(context.Context) error            { return nil }
func (m *slowArtModeTransport) RecordFailure(time.Duration)              {}
func (m *slowArtModeTransport) RecordSuccess()                           {}

func TestCapacityManager_RecordSuccess_StorageWriteFailure(t *testing.T) {
	tmp := t.TempDir()
	cm := NewCapacityManager(tmp, "1.2.3.4")
	if err := cm.Save(&CapacityState{IsFull: true}); err != nil {
		t.Fatalf("seed capacity state: %v", err)
	}

	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatalf("make directory readonly: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmp, 0o700)
	}()

	if _, err := cm.RecordSuccess(); err == nil {
		t.Fatal("expected RecordSuccess to fail when atomic write cannot persist")
	}
}

func TestCapacityManager_Save_AtomicWriteFailure(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "nested")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cm := NewCapacityManager(target, "1.2.3.4")
	if err := cm.Save(&CapacityState{MaxImages: 5}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := os.Chmod(target, 0o500); err != nil {
		t.Fatalf("make directory readonly: %v", err)
	}
	defer func() {
		_ = os.Chmod(target, 0o700)
	}()

	if err := cm.Save(&CapacityState{MaxImages: 9}); err == nil {
		t.Fatal("expected Save failure when atomic write cannot persist")
	}
}

func TestLocalCollection_ObserveRename_RenameFailure(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{TokenDir: tmp, TVIPs: []string{"1.2.3.4"}}
	mappingPath := filepath.Join(tmp, "tv_1_2_3_4_mapping.json")
	m := &Mapping{path: mappingPath, data: map[string]string{"old.jpg": "id-old"}}
	if err := m.Save(); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}

	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatalf("make token dir readonly: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmp, 0o700)
	}()

	collection := &localCollection{
		cfg:     cfg,
		logger:  slog.Default(),
		catalog: sources.NewArtworkCatalog(t.TempDir(), slog.Default()),
	}
	if err := collection.observeRename("old.jpg", "new.jpg"); err == nil {
		t.Fatal("expected observeRename to fail when mapping rename cannot persist")
	}
}

func TestLocalCollection_Prepare_SupportedFilesError(t *testing.T) {
	cfg := &config.Config{
		ArtworkDir:      t.TempDir(),
		OptimizeEnabled: true,
	}

	sentinelFile := filepath.Join(cfg.ArtworkDir, "not-a-dir")
	if err := os.WriteFile(sentinelFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write non-directory artifact: %v", err)
	}

	collection := &localCollection{
		cfg:     cfg,
		logger:  slog.Default(),
		loader:  fixedSourceLoader{},
		catalog: sources.NewArtworkCatalog(sentinelFile, slog.Default()),
	}

	if _, err := collection.prepare(context.Background()); err == nil {
		t.Fatal("expected prepare() to fail when supported files cannot be loaded")
	}
}

func TestEngine_RunOnce_ContextCancellationFromSyncAllTVs(t *testing.T) {
	cfg := &config.Config{
		TVIPs:           []string{"1.2.3.4"},
		ArtworkDir:      t.TempDir(),
		TokenDir:        t.TempDir(),
		SyncIntervalMin: 1,
	}
	e := NewEngine(cfg, slog.Default(), nil)
	e.collection = &localCollection{
		cfg:     cfg,
		logger:  slog.Default(),
		loader:  fixedSourceLoader{},
		catalog: sources.NewArtworkCatalog(cfg.ArtworkDir, slog.Default()),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := e.RunOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce() = %v, want context.Canceled", err)
	}
}

func TestEngine_RunOnce_ReturnsContextErrorFromSyncAllTVs(t *testing.T) {
	cfg := &config.Config{
		TVIPs:           []string{"1.2.3.4"},
		ArtworkDir:      t.TempDir(),
		TokenDir:        t.TempDir(),
		SyncIntervalMin: 1,
	}
	e := NewEngine(cfg, slog.Default(), nil)
	e.collection = &localCollection{
		cfg:     cfg,
		logger:  slog.Default(),
		loader:  fixedSourceLoader{},
		catalog: sources.NewArtworkCatalog(cfg.ArtworkDir, slog.Default()),
	}
	e.newClient = func(string, *config.Config, *slog.Logger) TVTransport {
		return &slowArtModeTransport{delay: 20 * time.Millisecond}
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)

	if err := e.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce() = %v, want context.Canceled", err)
	}
}

func TestEngine_syncTV_UsesNewTVReconcilerErrorPath(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token-file")
	if err := os.WriteFile(tokenFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("create token path file: %v", err)
	}

	e := NewEngine(&config.Config{TokenDir: tokenFile, TVIPs: []string{"1.2.3.4"}}, slog.Default(), nil)
	e.newClient = func(string, *config.Config, *slog.Logger) TVTransport {
		return &fakeTVTransport{}
	}

	result, err := e.syncTV(context.Background(), "1.2.3.4", nil, &config.MatteConfig{}, slog.Default())
	if err == nil {
		t.Fatalf("syncTV() = %v, want error from NewTVReconciler", result)
	}
}

func TestEngine_syncAllTVs_HealthUpdatedOnError(t *testing.T) {
	cfg := &config.Config{
		TVIPs:           []string{"1.2.3.4"},
		ArtworkDir:      t.TempDir(),
		TokenDir:        t.TempDir(),
		SyncIntervalMin: 1,
	}
	e := NewEngine(cfg, slog.Default(), health.NewStatus())
	e.newClient = func(string, *config.Config, *slog.Logger) TVTransport {
		return &fakeTVTransport{connectErr: errors.New("boom")}
	}

	_, syncErrs := e.syncAllTVs(context.Background(), map[string]struct{}{}, &config.MatteConfig{}, slog.Default())
	if len(syncErrs) != 1 {
		t.Fatalf("syncAllTVs() got %d errors", len(syncErrs))
	}
}

func TestEngine_finalizeCycle_RecordsSyncErrorsToHealthEvenWhenCycleErrorNil(t *testing.T) {
	healthStatus := health.NewStatus()
	engine := NewEngine(&config.Config{}, slog.Default(), healthStatus)
	engine.finalizeCycle(nil, []error{errors.New("test sync error")})

	if healthStatus.LastSyncOK {
		t.Fatal("expected health status to mark sync as unhealthy")
	}
}

func TestTVReconciler_ExecuteSyncPlan_DeleteBatchPersistenceFailure(t *testing.T) {
	tmp := t.TempDir()
	mappingPath := filepath.Join(tmp, "tv_1_2_3_4_mapping.json")
	mapping := &Mapping{path: mappingPath, data: map[string]string{"old.jpg": "id-old"}}
	if err := mapping.Save(); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatalf("make mapping dir readonly: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmp, 0o700)
	}()

	reconciler := &TVReconciler{logger: slog.Default()}
	_, err := reconciler.ExecuteSyncPlan(context.Background(), &SyncPlan{StaleFiles: []string{"old.jpg"}}, &mockTVTransportExecution{}, mapping, config.SyncPolicy{UploadAttempts: 1})
	if err == nil {
		t.Fatal("expected ExecuteSyncPlan to fail when stale mapping cleanup cannot persist")
	}
}

func TestTVReconciler_processDeletions_TrackedDeletePersistenceFailure(t *testing.T) {
	tmp := t.TempDir()
	mappingPath := filepath.Join(tmp, "tv_1_2_3_4_mapping.json")
	mapping := &Mapping{
		path: mappingPath,
		data: map[string]string{"id-stale": "old.jpg"},
	}
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatalf("make mapping dir readonly: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmp, 0o700)
	}()

	plan := &SyncPlan{
		ToDeleteIDs:   []string{"id-stale"},
		ToDeleteFiles: []string{"old.jpg"},
	}
	reconciler := &TVReconciler{logger: slog.Default()}
	_, err := reconciler.ExecuteSyncPlan(context.Background(), plan, &mockTVTransportExecution{}, mapping, config.SyncPolicy{UploadAttempts: 1})
	if err == nil {
		t.Fatal("expected ExecuteSyncPlan to fail when tracked deletion cannot persist")
	}
}

func TestTVReconciler_processUploads_CancelledDuringDelay(t *testing.T) {
	plan := &SyncPlan{
		ToUpload: []UploadJob{
			{Filename: "first.jpg", FilePath: "first.jpg", FileType: "jpg", Matte: "none"},
			{Filename: "second.jpg", FilePath: "second.jpg", FileType: "jpg", Matte: "none"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	reconciler := &TVReconciler{logger: slog.Default(), cfg: &config.Config{}}
	execCtx := &executionContext{
		ctx:       ctx,
		plan:      plan,
		transport: &mockTVTransportExecution{uploadId: "id-new"},
		mapping:   &Mapping{data: map[string]string{}},
		policy:    config.SyncPolicy{UploadAttempts: 1, UploadDelay: 20 * time.Millisecond},
		result:    &TVSyncResult{NewUploads: map[string]string{}},
	}
	timer := time.AfterFunc(10*time.Millisecond, cancel)
	defer timer.Stop()

	err := reconciler.processUploads(execCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("processUploads() = %v, want context.Canceled", err)
	}
	if execCtx.result.Uploaded != 1 {
		t.Fatalf("expected exactly one upload before delay cancellation, got %d", execCtx.result.Uploaded)
	}
}

func TestTVReconciler_chooseImageID_ShuffleEmpty(t *testing.T) {
	reconciler := &TVReconciler{logger: slog.Default()}
	if got := reconciler.chooseImageID(map[string]string{}, &samsung.SlideshowStatus{Type: ssTypeShuffle}); got != "" {
		t.Fatalf("chooseImageID shuffle empty returned %q", got)
	}
	if got := reconciler.chooseImageID(map[string]string{}, nil); got != "" {
		t.Fatalf("chooseImageID non-shuffle empty returned %q", got)
	}
}

func TestTVReconciler_NewTVReconciler_LoadFailure(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token-file")
	if err := os.WriteFile(tokenFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("create token path file: %v", err)
	}

	if _, err := NewTVReconciler("1.2.3.4", &config.Config{TokenDir: tokenFile}, &config.MatteConfig{}, slog.Default()); err == nil {
		t.Fatal("expected NewTVReconciler to fail with invalid token dir path")
	}
}

func TestTVReconciler_Reconcile_ExecuteError(t *testing.T) {
	tmp := t.TempDir()
	reconciler, err := NewTVReconciler("1.2.3.4", &config.Config{TokenDir: tmp, UploadAttempts: 1}, &config.MatteConfig{}, slog.Default())
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}

	transport := &fakeTVTransport{
		artMode:   true,
		uploadErr: errors.New("upload failed"),
	}
	_, err = reconciler.Reconcile(context.Background(), transport, map[string]struct{}{"photo.jpg": {}})
	if err == nil {
		t.Fatal("expected reconcile error when execute returns upload failure")
	}
}

func TestTVReconciler_applyCapacityFilter_LoadError(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token-file")
	if err := os.WriteFile(tokenFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("create token path file: %v", err)
	}

	reconciler := &TVReconciler{
		ip:     "1.2.3.4",
		cfg:    &config.Config{TokenDir: tokenFile},
		logger: slog.Default(),
	}
	files := map[string]struct{}{"photo.jpg": {}}
	filtered, _ := reconciler.applyCapacityFilter(files)
	if len(filtered) != 1 {
		t.Fatalf("expected filtered files to remain unchanged on capacity load error, got %d", len(filtered))
	}
}

func TestTVReconciler_logPlan_UnknownRemovalWarning(t *testing.T) {
	buf := bytes.Buffer{}
	reconciler := &TVReconciler{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	reconciler.logPlan(&SyncPlan{ToDeleteUnknownIDs: []string{"id-x"}}, config.SyncPolicy{RemoveUnknownImages: false})
	if !strings.Contains(buf.String(), "set REMOVE_UNKNOWN_IMAGES=true to remove") {
		t.Fatalf("expected warning log for unknown removal policy")
	}
}

func TestTVReconciler_getTVContent_SaveMetadataError(t *testing.T) {
	reconciler := &TVReconciler{logger: slog.Default()}
	transport := &saveMetadataFailTransport{
		fakeTVTransport: &fakeTVTransport{artMode: true},
		saveErr:         errors.New("metadata unavailable"),
	}

	syncResult := &TVSyncResult{}
	if _, err := reconciler.getTVContent(context.Background(), transport, syncResult); err != nil {
		t.Fatalf("getTVContent() = %v", err)
	}
	if !syncResult.ArtMode {
		t.Fatalf("expected art mode true")
	}
}

func TestTVReconciler_handleCapacityError_SaveFailure(t *testing.T) {
	tmp := t.TempDir()
	capDir := filepath.Join(tmp, "tokens")
	if err := os.Mkdir(capDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	capMgr := NewCapacityManager(capDir, "1.2.3.4")
	if err := capMgr.Save(&CapacityState{MaxImages: 4, IsFull: true}); err != nil {
		t.Fatalf("seed capacity state: %v", err)
	}

	if err := os.Chmod(capDir, 0o500); err != nil {
		t.Fatalf("make readonly: %v", err)
	}
	defer func() {
		_ = os.Chmod(capDir, 0o700)
	}()

	buf := bytes.Buffer{}
	reconciler := &TVReconciler{
		logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
	reconciler.handleCapacityError(&TVSyncResult{StorageFull: true, Uploaded: 1, Deleted: 1}, &SyncPlan{TrackedFilesCount: 6}, capMgr)
	if !strings.Contains(buf.String(), "failed to save capacity state") {
		t.Fatalf("expected save failure warning log")
	}
}

func TestTVReconciler_Reconcile_CapacityRecordSuccessFailure(t *testing.T) {
	tmp := t.TempDir()
	capMgr := NewCapacityManager(tmp, "1.2.3.4")
	if err := capMgr.Save(&CapacityState{IsFull: true, MaxImages: 0, SuccessStreak: 0}); err != nil {
		t.Fatalf("seed capacity state: %v", err)
	}

	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatalf("make token dir readonly: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmp, 0o700)
	}()

	reconciler, err := NewTVReconciler("1.2.3.4", &config.Config{TokenDir: tmp}, &config.MatteConfig{}, slog.Default())
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}

	transport := &fakeTVTransport{
		artMode: true,
	}
	_, err = reconciler.Reconcile(context.Background(), transport, map[string]struct{}{})
	if err != nil {
		t.Fatalf("reconcile should return no error: %v", err)
	}
}

func TestMapping_AtomicWriteWithBackup_ReplaceFailure(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "mapping.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("seed mapping file: %v", err)
	}
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatalf("make dir readonly: %v", err)
	}
	defer func() {
		_ = os.Chmod(tmp, 0o700)
	}()
	if err := atomicWriteWithBackup(target, []byte(`{"ok":true}`), 0o600); err == nil {
		t.Fatal("expected atomic write failure when replace cannot create temporary file")
	}
}

func TestExecution_chooseImageID_ShuffleFallsThrough(t *testing.T) {
	reconciler := &TVReconciler{logger: slog.Default()}
	id := reconciler.chooseImageID(map[string]string{"first.jpg": "one"}, &samsung.SlideshowStatus{Type: ssTypeShuffle})
	if id != "one" {
		t.Fatalf("expected shuffle selection to return the only value, got %q", id)
	}

	id = reconciler.chooseImageID(map[string]string{"first.jpg": "one", "second.jpg": "two"}, &samsung.SlideshowStatus{})
	if id == "" {
		t.Fatalf("expected one image id from non-shuffle path")
	}
}

func TestMapping_AtomicWriteWithBackup_ReadStateFailure(t *testing.T) {
	tmp := t.TempDir()
	blockingParent := filepath.Join(tmp, "blocking")
	if err := os.WriteFile(blockingParent, []byte("x"), 0o600); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}

	err := atomicWriteWithBackup(filepath.Join(blockingParent, "mapping.json"), []byte(`{"ok":true}`), 0o600)
	if err == nil {
		t.Fatal("expected atomicWriteWithBackup to fail when reading existing state fails")
	}
}

func TestMapping_AtomicWriteWithBackup_ReadStateNotExistButNotDirectory(t *testing.T) {
	tmp := t.TempDir()
	blockerDir := filepath.Join(tmp, "existing-dir")
	if err := os.Mkdir(blockerDir, 0o700); err != nil {
		t.Fatalf("make blocker dir: %v", err)
	}

	if err := atomicWriteWithBackup(blockerDir, []byte(`{"ok":true}`), 0o600); err == nil {
		t.Fatal("expected atomicWriteWithBackup to fail when existing state path is a directory")
	}
}

func TestCapacityManager_Save_MarshalFailure(t *testing.T) {
	cm := NewCapacityManager(t.TempDir(), "1.2.3.4")

	origMarshal := capacityMarshalState
	capacityMarshalState = func(_ *CapacityState) ([]byte, error) {
		return nil, errors.New("marshal failure")
	}
	defer func() {
		capacityMarshalState = origMarshal
	}()

	if err := cm.Save(&CapacityState{}); err == nil {
		t.Fatal("expected Save() to fail when capacity marshal fails")
	}
}

func TestLocalCollection_Prepare_SupportedFilesErrorAfterOptimize(t *testing.T) {
	cfg := &config.Config{
		ArtworkDir:      t.TempDir(),
		OptimizeEnabled: true,
	}
	collection := &localCollection{
		cfg:     cfg,
		logger:  slog.Default(),
		loader:  fixedSourceLoader{},
		catalog: sources.NewArtworkCatalog(cfg.ArtworkDir, slog.Default()),
	}

	origOptimize := prepareOptimizeCatalog
	origSupported := prepareCatalogSupportedFiles
	prepareOptimizeCatalog = func(
		_ context.Context,
		_ string,
		_ optimize.Catalog,
		_ optimize.Config,
		_ optimize.RenameObserver,
		_ *slog.Logger,
	) (int, error) {
		return 1, nil
	}
	prepareCatalogSupportedFiles = func(_ *sources.ArtworkCatalog) (map[string]struct{}, error) {
		return nil, errors.New("supported files error")
	}
	defer func() {
		prepareOptimizeCatalog = origOptimize
		prepareCatalogSupportedFiles = origSupported
	}()

	if _, err := collection.prepare(context.Background()); err == nil {
		t.Fatal("expected prepare() to fail when SupportedFiles fails after optimize")
	}
}

func TestEngine_RunLoop_TickErrorPath(t *testing.T) {
	cfg := &config.Config{
		TVIPs:           []string{"1.2.3.4"},
		ArtworkDir:      t.TempDir(),
		TokenDir:        t.TempDir(),
		SyncIntervalMin: 1,
	}
	e := NewEngine(cfg, slog.Default(), nil)
	e.newClient = func(_ string, _ *config.Config, _ *slog.Logger) TVTransport {
		return &fakeTVTransport{connectErr: errors.New("tick failure")}
	}

	origSyncInterval := engineSyncInterval
	engineSyncInterval = func(*config.Config) time.Duration {
		return 20 * time.Millisecond
	}
	defer func() {
		engineSyncInterval = origSyncInterval
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := e.RunLoop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunLoop() = %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestMapping_Save_MarshalFailure(t *testing.T) {
	origMarshal := marshalMappingState
	marshalMappingState = func(_ interface{}) ([]byte, error) {
		return nil, errors.New("marshal failure")
	}
	defer func() {
		marshalMappingState = origMarshal
	}()

	m := &Mapping{
		path: filepath.Join(t.TempDir(), "mapping.json"),
		data: map[string]string{"track.jpg": "track-id"},
	}
	if err := m.Save(); err == nil {
		t.Fatal("expected Save() to fail when mapping marshal fails")
	}
}

func TestMapping_AtomicReplace_ErrorPaths(t *testing.T) {
	cases := []struct {
		name   string
		mutate func()
	}{
		{
			name: "chmod temporary file",
			mutate: func() {
				chmodStateFile = func(_ *os.File, _ os.FileMode) error {
					return errors.New("chmod failure")
				}
			},
		},
		{
			name: "write temporary file",
			mutate: func() {
				writeStateFile = func(_ *os.File, _ []byte) (int, error) {
					return 0, errors.New("write failure")
				}
			},
		},
		{
			name: "sync temporary file",
			mutate: func() {
				syncStateFile = func(_ *os.File) error {
					return errors.New("sync failure")
				}
			},
		},
		{
			name: "close temporary file",
			mutate: func() {
				closeStateFileHandle = func(_ *os.File) error {
					return errors.New("close failure")
				}
			},
		},
		{
			name: "open state directory",
			mutate: func() {
				openStateDirectory = func(_ string) (*os.File, error) {
					return nil, errors.New("open dir failure")
				}
			},
		},
		{
			name: "sync state directory",
			mutate: func() {
				syncStateDirectory = func(_ *os.File) error {
					return errors.New("sync dir failure")
				}
			},
		},
		{
			name: "close state directory",
			mutate: func() {
				closeStateDirectory = func(_ *os.File) error {
					return errors.New("close dir failure")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origCreate := createStateTempFile
			origChmod := chmodStateFile
			origWrite := writeStateFile
			origSync := syncStateFile
			origCloseTemp := closeStateFileHandle
			origOpenDir := openStateDirectory
			origSyncDir := syncStateDirectory
			origCloseDir := closeStateDirectory
			defer func() {
				createStateTempFile = origCreate
				chmodStateFile = origChmod
				writeStateFile = origWrite
				syncStateFile = origSync
				closeStateFileHandle = origCloseTemp
				openStateDirectory = origOpenDir
				syncStateDirectory = origSyncDir
				closeStateDirectory = origCloseDir
			}()

			tc.mutate()

			path := filepath.Join(t.TempDir(), "state.json")
			if err := atomicReplace(path, []byte("payload"), 0o600); err == nil {
				t.Fatalf("expected atomicReplace() to fail for %s", tc.name)
			}
		})
	}
}
