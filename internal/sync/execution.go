package sync

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// ExecuteSyncPlan applies the computed planning rules onto the target transport.
func (s *TVReconciler) ExecuteSyncPlan(
	ctx context.Context,
	plan *SyncPlan,
	transport TVTransport,
	mapping *Mapping,
	policy config.SyncPolicy,
) (TVSyncResult, error) {
	result := TVSyncResult{
		IP:           plan.IP,
		Model:        transport.Model(),
		Status:       "ok",
		ArtMode:      true,
		NewUploads:   make(map[string]string),
		DeletedFiles: plan.StaleFiles,
	}

	eCtx := &executionContext{
		ctx:       ctx,
		plan:      plan,
		transport: transport,
		mapping:   mapping,
		policy:    policy,
		result:    &result,
	}

	if err := s.processUploads(eCtx); err != nil {
		return result, err
	}

	s.processDeletions(eCtx)

	finalMapping := mapping.AllContentIDs()
	s.applySelectionAndSlideshowPlan(ctx, plan, transport, finalMapping)

	s.updateSlideshowPlan(ctx, plan, transport)
	s.updateBrightnessPlan(ctx, plan, transport, &result)
	s.handleAutoOffPlan(ctx, plan, transport)

	result.TotalImages = plan.TrackedFilesCount + result.Uploaded - result.Deleted
	return result, nil
}

type executionContext struct {
	ctx       context.Context
	plan      *SyncPlan
	transport TVTransport
	mapping   *Mapping
	policy    config.SyncPolicy
	result    *TVSyncResult
}

func (s *TVReconciler) processUploads(eCtx *executionContext) error {
	uploadsDone := 0
	for _, job := range eCtx.plan.ToUpload {
		if uploadsDone > 0 && eCtx.policy.UploadDelay > 0 && !eCtx.policy.DryRun {
			select {
			case <-eCtx.ctx.Done():
				return eCtx.ctx.Err()
			case <-time.After(eCtx.policy.UploadDelay):
			}
		}

		if eCtx.policy.DryRun {
			s.logger.Info("[DRY RUN] would upload", "file", job.Filename)
			eCtx.result.Uploaded++
			continue
		}

		contentID, uploadErr := s.uploadWithRetry(eCtx.ctx, eCtx.transport, job.FilePath, job.FileType, job.Matte, eCtx.policy)
		if uploadErr != nil {
			if errors.Is(uploadErr, samsung.ErrStorageFull) {
				eCtx.result.StorageFull = true
			}
			s.logger.Error("upload failed", "file", job.Filename, "error", uploadErr)
			eCtx.result.ErrorMessage = uploadErr.Error()
			return nil
		}

		eCtx.mapping.Set(job.Filename, contentID)
		eCtx.result.NewUploads[job.Filename] = contentID
		s.logger.Info("uploaded", "file", job.Filename, "content_id", contentID, "matte", job.Matte)
		eCtx.result.Uploaded++
		uploadsDone++
	}
	return nil
}

func (s *TVReconciler) processDeletions(eCtx *executionContext) {
	s.deleteTrackedImages(eCtx)
	s.deleteUnknownImages(eCtx)
}

func (s *TVReconciler) deleteTrackedImages(eCtx *executionContext) {
	count := len(eCtx.plan.ToDeleteIDs)
	if count == 0 {
		return
	}

	if eCtx.policy.DryRun {
		s.logger.Info("[DRY RUN] would delete tracked images", "count", count)
		eCtx.result.Deleted = count
		return
	}

	s.logger.Info("deleting tracked images", "count", count)
	if err := eCtx.transport.DeleteImages(eCtx.ctx, eCtx.plan.ToDeleteIDs); err != nil {
		s.logger.Error("batch delete failed", "error", err)
		eCtx.result.Deleted = count
		return
	}

	eCtx.result.DeletedFiles = append(eCtx.result.DeletedFiles, eCtx.plan.ToDeleteFiles...)
	eCtx.mapping.DeleteBatch(eCtx.plan.ToDeleteFiles)
	s.logger.Info("deleted tracked images", "count", count)
	eCtx.result.Deleted = count
}

func (s *TVReconciler) deleteUnknownImages(eCtx *executionContext) {
	count := len(eCtx.plan.ToDeleteUnknownIDs)
	if count == 0 {
		return
	}

	if eCtx.policy.DryRun {
		s.logger.Info("[DRY RUN] would delete unknown images", "count", count)
		return
	}

	s.logger.Info("deleting unknown images", "count", count)
	if err := eCtx.transport.DeleteImages(eCtx.ctx, eCtx.plan.ToDeleteUnknownIDs); err != nil {
		s.logger.Error("delete unknown images failed", "error", err)
	}
}

//nolint:revive // justified argument count for retry logic
func (s *TVReconciler) uploadWithRetry(
	ctx context.Context,
	transport TVTransport,
	filePath, fileType, matte string,
	policy config.SyncPolicy,
) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= policy.UploadAttempts; attempt++ {
		contentID, err := transport.Upload(ctx, filePath, fileType, matte)
		if err == nil {
			return contentID, nil
		}
		lastErr = err
		if attempt < policy.UploadAttempts {
			s.logger.Warn("upload retry", "attempt", attempt, "error", err)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(policy.UploadDelay * 2):
			}
		}
	}
	return "", lastErr
}

//nolint:gocyclo // complexity justified for this domain-specific path
func (s *TVReconciler) applySelectionAndSlideshowPlan(
	ctx context.Context,
	plan *SyncPlan,
	transport TVTransport,
	finalMapping map[string]string,
) {
	if !plan.HasChanges || len(plan.LocalFiles) == 0 || len(finalMapping) == 0 {
		return
	}

	var selectedID string
	slideshow := determineSlideshowSettings(s.cfg, s.logger)
	settingsForMode := slideshow
	if settingsForMode == nil {
		settingsForMode = plan.PreserveSlideshow
	}

	if settingsForMode != nil && settingsForMode.Type == ssTypeShuffle {
		values := mapValues(finalMapping)
		if len(values) > 0 {
			//nolint:gosec // weak random number generator is fine for selecting artwork shuffle order
			selectedID = values[rand.IntN(len(values))]
		}
		s.logger.Info("selecting random image for shuffle mode")
	} else {
		for _, id := range finalMapping {
			selectedID = id
			break
		}
		s.logger.Info("selecting first image")
	}

	if selectedID != "" {
		if err := transport.SelectImage(ctx, selectedID); err != nil {
			s.logger.Warn("failed to select image", "error", err)
		}
	}

	if plan.PreserveSlideshow != nil {
		if err := transport.SetSlideshow(ctx, *plan.PreserveSlideshow); err != nil {
			s.logger.Warn("failed to restore slideshow", "error", err)
		}
	}
}

func (s *TVReconciler) updateSlideshowPlan(
	ctx context.Context,
	plan *SyncPlan,
	transport TVTransport,
) {
	if plan.Slideshow == nil {
		return
	}
	current, _ := transport.SlideshowStatus(ctx)
	needsUpdate := current == nil ||
		current.Value != plan.Slideshow.Value ||
		current.Type != plan.Slideshow.Type

	if needsUpdate {
		s.logger.Info("updating slideshow settings",
			"interval", plan.Slideshow.Value,
			"type", plan.Slideshow.Type,
		)
		if err := transport.SetSlideshow(ctx, *plan.Slideshow); err != nil {
			s.logger.Warn("failed to set slideshow", "error", err)
		}
	}
}

func (s *TVReconciler) updateBrightnessPlan(
	ctx context.Context,
	plan *SyncPlan,
	transport TVTransport,
	result *TVSyncResult,
) {
	if plan.Brightness == nil {
		return
	}
	if err := transport.SetBrightness(ctx, *plan.Brightness); err != nil {
		s.logger.Warn("failed to set brightness", "error", err)
	}
	result.Brightness = fmt.Sprintf("%d", *plan.Brightness)
}

func (s *TVReconciler) handleAutoOffPlan(
	ctx context.Context,
	plan *SyncPlan,
	transport TVTransport,
) {
	if !plan.TurnOff {
		return
	}
	s.logger.Info("within auto-off window, turning off TV")
	if err := transport.TurnOff(ctx); err != nil {
		s.logger.Warn("failed to turn off TV", "error", err)
	} else {
		s.logger.Info("TV turned off")
	}
}
