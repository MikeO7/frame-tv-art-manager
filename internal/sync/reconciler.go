package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// TVTransport is the seam for Samsung TV I/O used during reconciliation.
type TVTransport interface {
	ShouldSkip() bool
	Connect(ctx context.Context) error
	Close() error
	Model() string
	IsInArtMode(ctx context.Context) bool
	SaveMetadata(ctx context.Context) error
	ListUploaded(ctx context.Context) ([]samsung.ArtContent, error)
	Upload(ctx context.Context, filePath, fileType, matte string) (string, error)
	DeleteImages(ctx context.Context, ids []string) error
	SelectImage(ctx context.Context, contentID string) error
	SlideshowStatus(ctx context.Context) (*samsung.SlideshowStatus, error)
	SetSlideshow(ctx context.Context, status samsung.SlideshowStatus) error
	SetBrightness(ctx context.Context, val int) error
	TurnOff(ctx context.Context) error
	RecordFailure(baseInterval time.Duration)
	RecordSuccess()
}

// Reconciler runs inventory reconciliation policy against a TVTransport adapter.
type Reconciler struct {
	logger *slog.Logger
}

// NewReconciler creates a reconciler for TV sync policy.
func NewReconciler(logger *slog.Logger) *Reconciler {
	return &Reconciler{logger: logger}
}

func (r *Reconciler) getTVContent(
	ctx context.Context,
	tv TVTransport,
	policy config.SyncPolicy,
	result *TVSyncResult,
) ([]samsung.ArtContent, error) {
	result.Model = tv.Model()

	if !tv.IsInArtMode(ctx) {
		r.logger.Info("skipping — TV not in art mode")
		result.Status = "skipped (not art mode)"
		return nil, nil
	}
	result.ArtMode = true

	if err := tv.SaveMetadata(ctx); err != nil {
		r.logger.Debug("could not save metadata", "error", err)
	}

	tvContent, err := tv.ListUploaded(ctx)
	if err != nil {
		tv.RecordFailure(time.Duration(policy.SyncIntervalMin) * time.Minute)
		result.Status = "error"
		return nil, fmt.Errorf("get TV images: %w", err)
	}

	return tvContent, nil
}

// Run executes the full sync reconciliation cycle.
func (r *Reconciler) Run(
	ctx context.Context,
	tv TVTransport,
	ip string,
	req ReconcileInput,
	policy config.SyncPolicy,
) (TVSyncResult, error) {
	result := TVSyncResult{
		IP:         ip,
		NewUploads: make(map[string]string),
	}

	if tv.ShouldSkip() {
		result.Status = statusBackoff
		return result, nil
	}

	if err := tv.Connect(ctx); err != nil {
		if errors.Is(err, samsung.ErrGateFailed) {
			r.logger.Info("skipping — REST gate says TV is busy")
			result.Status = "skipped (gate)"
			return result, nil
		}
		tv.RecordFailure(time.Duration(policy.SyncIntervalMin) * time.Minute)
		result.Status = "error"
		return result, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = tv.Close() }()

	tvContent, err := r.getTVContent(ctx, tv, policy, &result)
	if err != nil || result.Status != "" {
		return result, err
	}

	trackedFiles, unknownIDs, staleFiles := reconcileInventory(req.Mapping, tvContent, r.logger)
	result.DeletedFiles = append(result.DeletedFiles, staleFiles...)

	r.logger.Info("TV inventory",
		"tracked", len(trackedFiles),
		"unknown", len(unknownIDs),
	)

	toUpload := diffSets(req.LocalFiles, trackedFiles)
	toDelete := diffSets(trackedFiles, req.LocalFiles)

	r.logSyncPlan(toUpload, toDelete, unknownIDs, policy)

	var preserveSlideshow *samsung.SlideshowStatus
	hasChanges := len(toUpload) > 0 || len(toDelete) > 0 || (policy.RemoveUnknownImages && len(unknownIDs) > 0)
	if hasChanges && !policy.SlideshowOverride {
		preserveSlideshow, _ = tv.SlideshowStatus(ctx)
	}

	r.processUploads(ctx, tv, req, policy, toUpload, &result)

	r.processDeletions(ctx, tv, req, policy, toDelete, unknownIDs, &result)

	r.applySettings(ctx, tv, req, policy, preserveSlideshow, hasChanges, &result)

	result.TotalImages = len(trackedFiles) + result.Uploaded - result.Deleted
	result.Status = "ok"

	tv.RecordSuccess()
	r.logger.Info("sync completed")
	return result, nil
}

func (r *Reconciler) logSyncPlan(
	toUpload, toDelete, unknownIDs map[string]struct{},
	policy config.SyncPolicy,
) {
	if len(unknownIDs) > 0 {
		if policy.RemoveUnknownImages {
			r.logger.Info("will remove unknown images", "count", len(unknownIDs))
		} else {
			r.logger.Warn("unknown images on TV (set REMOVE_UNKNOWN_IMAGES=true to remove)",
				"count", len(unknownIDs))
		}
	}

	r.logger.Info("sync plan",
		"to_upload", len(toUpload),
		"to_delete", len(toDelete),
		"unknown_to_delete", boolCount(policy.RemoveUnknownImages, len(unknownIDs)),
	)
}

func (r *Reconciler) applySettings(
	ctx context.Context,
	tv TVTransport,
	req ReconcileInput,
	policy config.SyncPolicy,
	preserveSlideshow *samsung.SlideshowStatus,
	hasChanges bool,
	result *TVSyncResult,
) {
	finalMapping := buildFinalMapping(req.Mapping, result.NewUploads, result.DeletedFiles)
	r.applySelectionAndSlideshow(ctx, tv, req, policy, preserveSlideshow, hasChanges, finalMapping, req.LocalFiles)

	if req.Slideshow != nil && !policy.DryRun {
		current, _ := tv.SlideshowStatus(ctx)
		needsUpdate := current == nil ||
			current.Value != req.Slideshow.Value ||
			current.Type != req.Slideshow.Type

		if needsUpdate {
			r.logger.Info("updating slideshow settings",
				"interval", req.Slideshow.Value,
				"type", req.Slideshow.Type,
			)
			if err := tv.SetSlideshow(ctx, *req.Slideshow); err != nil {
				r.logger.Warn("failed to set slideshow", "error", err)
			}
		}
		result.Slideshow = fmt.Sprintf("%s every %s min", req.Slideshow.Type, req.Slideshow.Value)
	}

	if req.DesiredBrightness != nil && !policy.DryRun {
		if err := tv.SetBrightness(ctx, *req.DesiredBrightness); err != nil {
			r.logger.Warn("failed to set brightness", "error", err)
		}
		result.Brightness = fmt.Sprintf("%d", *req.DesiredBrightness)
	}

	if req.TriggerAutoOff {
		r.logger.Info("within auto-off window, turning off TV")
		if !policy.DryRun {
			if err := tv.TurnOff(ctx); err != nil {
				r.logger.Warn("failed to turn off TV", "error", err)
			} else {
				r.logger.Info("TV turned off")
			}
		}
	}
}

//nolint:nestif // complexity justified for this domain-specific path
func (r *Reconciler) processDeletions(
	ctx context.Context,
	tv TVTransport,
	req ReconcileInput,
	policy config.SyncPolicy,
	toDelete map[string]struct{},
	unknownIDs map[string]struct{},
	result *TVSyncResult,
) {
	if len(toDelete) > 0 {
		var idsToDelete []string
		var filesToDelete []string
		for filename := range toDelete {
			if cid, ok := req.Mapping[filename]; ok {
				idsToDelete = append(idsToDelete, cid)
				filesToDelete = append(filesToDelete, filename)
			}
		}

		if len(idsToDelete) > 0 {
			if policy.DryRun {
				r.logger.Info("[DRY RUN] would delete tracked images", "count", len(idsToDelete))
			} else {
				r.logger.Info("deleting tracked images", "count", len(idsToDelete))
				if err := tv.DeleteImages(ctx, idsToDelete); err != nil {
					r.logger.Error("batch delete failed", "error", err)
				} else {
					result.DeletedFiles = append(result.DeletedFiles, filesToDelete...)
					r.logger.Info("deleted tracked images", "count", len(idsToDelete))
				}
			}
			result.Deleted = len(idsToDelete)
		}
	}

	if policy.RemoveUnknownImages && len(unknownIDs) > 0 {
		ids := setToSlice(unknownIDs)
		if policy.DryRun {
			r.logger.Info("[DRY RUN] would delete unknown images", "count", len(ids))
		} else {
			r.logger.Info("deleting unknown images", "count", len(ids))
			if err := tv.DeleteImages(ctx, ids); err != nil {
				r.logger.Error("delete unknown images failed", "error", err)
			}
		}
	}
}

func (r *Reconciler) processUploads(
	ctx context.Context,
	tv TVTransport,
	req ReconcileInput,
	policy config.SyncPolicy,
	toUpload map[string]struct{},
	result *TVSyncResult,
) {
	uploadsDone := 0
	for filename := range toUpload {
		if uploadsDone > 0 && policy.UploadDelay > 0 && !policy.DryRun {
			time.Sleep(policy.UploadDelay)
		}

		if policy.DryRun {
			r.logger.Info("[DRY RUN] would upload", "file", filename)
			result.Uploaded++
			continue
		}

		filePath := filepath.Join(policy.ArtworkDir, filename)
		fileType := artwork.FileTypeFromExt(filename)
		matte := policy.MatteStyle
		if customMatte, ok := req.MatteOverrides[filename]; ok {
			matte = customMatte
		}

		contentID, uploadErr := r.uploadWithRetry(ctx, tv, filePath, fileType, matte, policy)
		if uploadErr != nil {
			r.logger.Error("upload failed", "file", filename, "error", uploadErr)
			continue
		}

		result.NewUploads[filename] = contentID
		r.logger.Info("uploaded", "file", filename, "content_id", contentID, "matte", matte)
		result.Uploaded++
		uploadsDone++
	}
}

//nolint:revive // upload retry needs explicit transport and policy args
func (r *Reconciler) uploadWithRetry(
	ctx context.Context,
	tv TVTransport,
	filePath, fileType, matte string,
	policy config.SyncPolicy,
) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= policy.UploadAttempts; attempt++ {
		contentID, err := tv.Upload(ctx, filePath, fileType, matte)
		if err == nil {
			return contentID, nil
		}
		lastErr = err
		if attempt < policy.UploadAttempts {
			r.logger.Warn("upload retry", "attempt", attempt, "error", err)
			time.Sleep(policy.UploadDelay * 2)
		}
	}
	return "", lastErr
}

//nolint:gocyclo,revive // complexity justified for this domain-specific path
func (r *Reconciler) applySelectionAndSlideshow(
	ctx context.Context,
	tv TVTransport,
	req ReconcileInput,
	policy config.SyncPolicy,
	preserveSlideshow *samsung.SlideshowStatus,
	hasChanges bool,
	finalMapping map[string]string,
	localFiles map[string]struct{},
) {
	if !hasChanges || len(localFiles) == 0 || len(finalMapping) == 0 {
		return
	}

	var selectedID string
	settingsForMode := req.Slideshow
	if settingsForMode == nil {
		settingsForMode = preserveSlideshow
	}

	if settingsForMode != nil && settingsForMode.Type == "shuffleslideshow" {
		values := mapValues(finalMapping)
		//nolint:gosec // complexity justified for this domain-specific path
		selectedID = values[rand.IntN(len(values))]
		r.logger.Info("selecting random image for shuffle mode")
	} else {
		for _, id := range finalMapping {
			selectedID = id
			break
		}
		r.logger.Info("selecting first image")
	}

	if selectedID != "" && !policy.DryRun {
		if err := tv.SelectImage(ctx, selectedID); err != nil {
			r.logger.Warn("failed to select image", "error", err)
		}
	}

	if preserveSlideshow != nil && !policy.DryRun {
		if err := tv.SetSlideshow(ctx, *preserveSlideshow); err != nil {
			r.logger.Warn("failed to restore slideshow", "error", err)
		}
	}
}

func reconcileInventory(
	mapping map[string]string,
	tvContent []samsung.ArtContent,
	logger *slog.Logger,
) (trackedFiles, unknownIDs map[string]struct{}, staleFiles []string) {
	trackedFiles = make(map[string]struct{})
	unknownIDs = make(map[string]struct{})

	reverseMap := make(map[string]string, len(mapping))
	for filename, cid := range mapping {
		reverseMap[cid] = filename
	}

	liveIDs := make(map[string]bool, len(tvContent))
	for _, item := range tvContent {
		liveIDs[item.ContentID] = true
	}

	for filename, contentID := range mapping {
		if !liveIDs[contentID] {
			logger.Debug("purging stale mapping (not on TV)", "file", filename, "id", contentID)
			staleFiles = append(staleFiles, filename)
		}
	}

	for _, item := range tvContent {
		if filename, ok := reverseMap[item.ContentID]; ok {
			trackedFiles[filename] = struct{}{}
		} else {
			unknownIDs[item.ContentID] = struct{}{}
		}
	}

	return trackedFiles, unknownIDs, staleFiles
}

func buildFinalMapping(base, uploads map[string]string, deleted []string) map[string]string {
	finalMapping := make(map[string]string, len(base)+len(uploads))
	for k, v := range base {
		finalMapping[k] = v
	}
	for _, f := range deleted {
		delete(finalMapping, f)
	}
	for k, v := range uploads {
		finalMapping[k] = v
	}
	return finalMapping
}

func diffSets(a, b map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{})
	for k := range a {
		if _, ok := b[k]; !ok {
			result[k] = struct{}{}
		}
	}
	return result
}

func setToSlice(s map[string]struct{}) []string {
	result := make([]string, 0, len(s))
	for k := range s {
		result = append(result, k)
	}
	return result
}

func mapValues(m map[string]string) []string {
	result := make([]string, 0, len(m))
	for _, v := range m {
		result = append(result, v)
	}
	return result
}

func boolCount(cond bool, count int) int {
	if cond {
		return count
	}
	return 0
}
