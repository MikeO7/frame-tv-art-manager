package sync

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

type mockTVTransportExecution struct {
	TVTransport
	selectImageErr   error
	setSlideshowErr  error
	setBrightnessErr error
	turnOffErr       error
	slideshowStatus  *samsung.SlideshowStatus
	uploadId         string
	uploadErr        error
	deleteErr        error

	// Trackers for assertions
	selectImageCalled   bool
	selectedImageId     string
	setSlideshowCalled  bool
	setSlideshowVal     samsung.SlideshowStatus
	setBrightnessCalled bool
	setBrightnessVal    int
	turnOffCalled       bool
}

func (m *mockTVTransportExecution) Model() string {
	return "MockTV"
}

func (m *mockTVTransportExecution) SelectImage(ctx context.Context, id string) error {
	m.selectImageCalled = true
	m.selectedImageId = id
	return m.selectImageErr
}

func (m *mockTVTransportExecution) SetSlideshow(ctx context.Context, s samsung.SlideshowStatus) error {
	m.setSlideshowCalled = true
	m.setSlideshowVal = s
	return m.setSlideshowErr
}

func (m *mockTVTransportExecution) SlideshowStatus(ctx context.Context) (*samsung.SlideshowStatus, error) {
	return m.slideshowStatus, nil
}

func (m *mockTVTransportExecution) SetBrightness(ctx context.Context, b int) error {
	m.setBrightnessCalled = true
	m.setBrightnessVal = b
	return m.setBrightnessErr
}

func (m *mockTVTransportExecution) TurnOff(ctx context.Context) error {
	m.turnOffCalled = true
	return m.turnOffErr
}

func (m *mockTVTransportExecution) Upload(ctx context.Context, filePath string, fileType string, matte string) (string, error) {
	return m.uploadId, m.uploadErr
}

func (m *mockTVTransportExecution) DeleteImages(ctx context.Context, ids []string) error {
	return m.deleteErr
}

func TestTVReconciler_updateBrightnessPlan(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	reconciler := &TVReconciler{logger: logger}

	ctx := context.Background()
	brightness := 5
	plan := &Plan{Brightness: &brightness}
	transport := &mockTVTransportExecution{}
	result := &TVSyncResult{}

	reconciler.updateBrightnessPlan(ctx, plan, transport, result)

	if result.Brightness != "5" {
		t.Errorf("expected brightness 5, got %s", result.Brightness)
	}
	if !transport.setBrightnessCalled || transport.setBrightnessVal != 5 {
		t.Errorf("expected SetBrightness(5) to be called")
	}

	planNil := &Plan{Brightness: nil}
	transportNil := &mockTVTransportExecution{}
	reconciler.updateBrightnessPlan(ctx, planNil, transportNil, result)
	if transportNil.setBrightnessCalled {
		t.Errorf("did not expect SetBrightness to be called")
	}

	transportFail := &mockTVTransportExecution{setBrightnessErr: errors.New("fail")}
	reconciler.updateBrightnessPlan(ctx, plan, transportFail, result)
	if !transportFail.setBrightnessCalled {
		t.Errorf("expected SetBrightness to be called even on failure")
	}
}

func TestTVReconciler_updateSlideshowPlan(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	reconciler := &TVReconciler{logger: logger}

	ctx := context.Background()

	// Test needsUpdate = true (different value)
	plan := &Plan{Slideshow: &samsung.SlideshowStatus{Value: "15", Type: "shuffle"}}
	transport := &mockTVTransportExecution{slideshowStatus: &samsung.SlideshowStatus{Value: "10", Type: "shuffle"}}

	reconciler.updateSlideshowPlan(ctx, plan, transport)

	if !transport.setSlideshowCalled || transport.setSlideshowVal.Value != "15" {
		t.Errorf("expected SetSlideshow to be called with 15")
	}

	// Test nil plan
	planNil := &Plan{Slideshow: nil}
	transportNil := &mockTVTransportExecution{}
	reconciler.updateSlideshowPlan(ctx, planNil, transportNil)
	if transportNil.setSlideshowCalled {
		t.Errorf("did not expect SetSlideshow to be called for nil plan")
	}

	// Test no update needed
	planSame := &Plan{Slideshow: &samsung.SlideshowStatus{Value: "10", Type: "shuffle"}}
	transportSame := &mockTVTransportExecution{slideshowStatus: &samsung.SlideshowStatus{Value: "10", Type: "shuffle"}}
	reconciler.updateSlideshowPlan(ctx, planSame, transportSame)
	if transportSame.setSlideshowCalled {
		t.Errorf("did not expect SetSlideshow to be called for identical settings")
	}

	// Test failure is swallowed gracefully
	transportFail := &mockTVTransportExecution{setSlideshowErr: errors.New("fail"), slideshowStatus: &samsung.SlideshowStatus{Value: "10", Type: "shuffle"}}
	reconciler.updateSlideshowPlan(ctx, plan, transportFail)
	if !transportFail.setSlideshowCalled {
		t.Errorf("expected SetSlideshow to be called even when error returned")
	}
}

func TestTVReconciler_handleAutoOffPlan(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	reconciler := &TVReconciler{logger: logger}

	ctx := context.Background()

	// Test true
	plan := &Plan{TurnOff: true}
	transport := &mockTVTransportExecution{}

	reconciler.handleAutoOffPlan(ctx, plan, transport)
	if !transport.turnOffCalled {
		t.Errorf("expected TurnOff to be called")
	}

	// Test false
	planFalse := &Plan{TurnOff: false}
	transportFalse := &mockTVTransportExecution{}
	reconciler.handleAutoOffPlan(ctx, planFalse, transportFalse)
	if transportFalse.turnOffCalled {
		t.Errorf("did not expect TurnOff to be called")
	}

	// Test failure swallowed gracefully
	transportFail := &mockTVTransportExecution{turnOffErr: errors.New("fail")}
	reconciler.handleAutoOffPlan(ctx, plan, transportFail)
	if !transportFail.turnOffCalled {
		t.Errorf("expected TurnOff to be called even when error returned")
	}
}

func TestTVReconciler_applySelectionAndSlideshowPlan(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	reconciler := &TVReconciler{logger: logger, cfg: &config.Config{}}

	ctx := context.Background()

	// 1. No changes plan
	planNoChanges := &Plan{HasChanges: false}
	transport1 := &mockTVTransportExecution{}
	reconciler.applySelectionAndSlideshowPlan(ctx, planNoChanges, transport1, map[string]string{})
	if transport1.selectImageCalled {
		t.Errorf("should not select image if HasChanges is false")
	}

	// 2. Changes, but no local files
	planNoLocalFiles := &Plan{HasChanges: true, LocalFiles: map[string]struct{}{}}
	transport2 := &mockTVTransportExecution{}
	reconciler.applySelectionAndSlideshowPlan(ctx, planNoLocalFiles, transport2, map[string]string{})
	if transport2.selectImageCalled {
		t.Errorf("should not select image if no local files")
	}

	// 3. Changes, local files, but empty mapping
	planEmptyMapping := &Plan{HasChanges: true, LocalFiles: map[string]struct{}{"test.jpg": {}}}
	transport3 := &mockTVTransportExecution{}
	reconciler.applySelectionAndSlideshowPlan(ctx, planEmptyMapping, transport3, map[string]string{})
	if transport3.selectImageCalled {
		t.Errorf("should not select image if empty mapping")
	}

	// 4. Shuffle selection
	planShuffle := &Plan{
		HasChanges:        true,
		LocalFiles:        map[string]struct{}{"test.jpg": {}},
		PreserveSlideshow: &samsung.SlideshowStatus{Value: "15", Type: "shuffle"},
	}
	mapping := map[string]string{"test.jpg": "id-test"}
	transport4 := &mockTVTransportExecution{}
	reconciler.applySelectionAndSlideshowPlan(ctx, planShuffle, transport4, mapping)
	if !transport4.selectImageCalled || transport4.selectedImageId != "id-test" {
		t.Errorf("should select image for shuffle")
	}

	// 5. Normal selection (first image)
	planNormal := &Plan{
		HasChanges:        true,
		LocalFiles:        map[string]struct{}{"test.jpg": {}},
		PreserveSlideshow: &samsung.SlideshowStatus{Value: "15", Type: "normal"},
	}
	transportNormal := &mockTVTransportExecution{}
	reconciler.applySelectionAndSlideshowPlan(ctx, planNormal, transportNormal, mapping)
	if !transportNormal.selectImageCalled || transportNormal.selectedImageId != "id-test" {
		t.Errorf("should select image for normal mode")
	}
	if !transportNormal.setSlideshowCalled {
		t.Errorf("should preserve slideshow settings")
	}

	// 6. Normal selection (first image) failure
	transportFail := &mockTVTransportExecution{selectImageErr: errors.New("fail")}
	reconciler.applySelectionAndSlideshowPlan(ctx, planNormal, transportFail, mapping)
	if !transportFail.selectImageCalled {
		t.Errorf("should attempt to select image even if it fails")
	}

	// 7. Preserve slideshow failure
	transportSlideshowFail := &mockTVTransportExecution{setSlideshowErr: errors.New("fail")}
	reconciler.applySelectionAndSlideshowPlan(ctx, planNormal, transportSlideshowFail, mapping)
	if !transportSlideshowFail.setSlideshowCalled {
		t.Errorf("should attempt to preserve slideshow even if it fails")
	}
}

func TestTVReconciler_uploadWithRetry(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	reconciler := &TVReconciler{logger: logger, cfg: &config.Config{}}
	ctx := context.Background()

	// 1. Success on first attempt
	transportSuccess := &mockTVTransportExecution{uploadId: "id-test"}
	policySuccess := config.SyncPolicy{UploadAttempts: 1}
	job := UploadJob{Filename: "file.jpg", FilePath: "file.jpg", FileType: "jpeg", Matte: "none"}
	id, err := reconciler.uploadWithRetry(ctx, transportSuccess, job, policySuccess)
	if err != nil || id != "id-test" {
		t.Errorf("expected success, got err: %v, id: %s", err, id)
	}

	// 2. Failure after all attempts
	transportFail := &mockTVTransportExecution{uploadErr: errors.New("fail")}
	policyFail := config.SyncPolicy{UploadAttempts: 2, UploadDelay: 1}
	_, err = reconciler.uploadWithRetry(ctx, transportFail, job, policyFail)
	if err == nil {
		t.Errorf("expected failure")
	}
}

func TestTVReconciler_ExecutePlan(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	reconciler := &TVReconciler{logger: logger, cfg: &config.Config{}}
	ctx := context.Background()

	// Setup mapping
	mapping := &Mapping{data: map[string]string{}}
	transport := &mockTVTransportExecution{uploadId: "id-test"}

	plan := &Plan{
		IP: "1.2.3.4",
		ToUpload: []UploadJob{
			{Filename: "file.jpg", FilePath: "file.jpg", FileType: "jpeg", Matte: "none"},
			{Filename: "file2.jpg", FilePath: "file2.jpg", FileType: "jpeg", Matte: "none"},
		},
		ToDeleteIDs:        []string{"id-del"},
		ToDeleteFiles:      []string{"del.jpg"},
		ToDeleteUnknownIDs: []string{"id-unknown"},
		HasChanges:         true,
		LocalFiles:         map[string]struct{}{"file.jpg": {}},
		PreserveSlideshow:  &samsung.SlideshowStatus{Value: "15", Type: "shuffle"},
		TurnOff:            true,
	}

	brightness := 5
	plan.Brightness = &brightness

	policy := config.SyncPolicy{UploadAttempts: 1, UploadDelay: 1}

	// Test normal execution
	result, err := reconciler.ExecutePlan(ctx, plan, transport, mapping, policy)
	if err != nil {
		t.Errorf("expected success, got err: %v", err)
	}
	if result.Uploaded != 2 {
		t.Errorf("expected 2 upload, got %d", result.Uploaded)
	}
	if result.Deleted != 1 {
		t.Errorf("expected 1 delete, got %d", result.Deleted)
	}
	if !transport.turnOffCalled {
		t.Errorf("expected turnOff to be triggered in execution plan")
	}

	// Test dry run
	policy.DryRun = true
	result, err = reconciler.ExecutePlan(ctx, plan, transport, mapping, policy)
	if err != nil {
		t.Errorf("expected success on dry run, got err: %v", err)
	}
	if result.Uploaded != 2 {
		t.Errorf("expected 2 upload in dry run, got %d", result.Uploaded)
	}
	if result.Deleted != 1 {
		t.Errorf("expected 1 delete in dry run, got %d", result.Deleted)
	}

	// Test upload error
	policy.DryRun = false
	transportFail := &mockTVTransportExecution{uploadErr: errors.New("fail")}
	planUploadErr := &Plan{
		IP: "1.2.3.4",
		ToUpload: []UploadJob{
			{Filename: "file.jpg", FilePath: "file.jpg", FileType: "jpeg", Matte: "none"},
		},
	}
	res, err := reconciler.ExecutePlan(ctx, planUploadErr, transportFail, mapping, policy)
	if err == nil {
		t.Errorf("expected a genuine upload failure to propagate as an error")
	}
	if res.StorageFull {
		t.Errorf("did not expect StorageFull to be true on general error")
	}

	// Test storage full upload error: benign, must not propagate as an error.
	transportStorageFull := &mockTVTransportExecution{uploadErr: samsung.ErrStorageFull}
	res, err = reconciler.ExecutePlan(ctx, planUploadErr, transportStorageFull, mapping, policy)
	if err != nil {
		t.Errorf("expected no error when upload reports storage full, got: %v", err)
	}
	if !res.StorageFull {
		t.Errorf("expected StorageFull to be true when upload returns ErrStorageFull")
	}

	// Test delete error
	transportDeleteFail := &mockTVTransportExecution{deleteErr: errors.New("fail")}
	_, err = reconciler.ExecutePlan(ctx, plan, transportDeleteFail, mapping, policy)
	if err != nil {
		t.Errorf("expected no error from execution itself when delete fails, got: %v", err)
	}
}
