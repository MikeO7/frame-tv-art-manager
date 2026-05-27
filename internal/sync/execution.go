package sync

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

// ExecuteSyncPlan applies the computed planning rules onto the target transport.
//
//nolint:gocognit,nestif,gocyclo,funlen // execution flow is inherently complex
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

	uploadsDone := 0
	for _, job := range plan.ToUpload {
		if uploadsDone > 0 && policy.UploadDelay > 0 && !policy.DryRun {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(policy.UploadDelay):
			}
		}

		if policy.DryRun {
			s.logger.Info("[DRY RUN] would upload", "file", job.Filename)
			result.Uploaded++
			continue
		}

		contentID, uploadErr := s.uploadWithRetry(ctx, transport, job.FilePath, job.FileType, job.Matte, policy)
		if uploadErr != nil {
			s.logger.Error("upload failed", "file", job.Filename, "error", uploadErr)
			continue
		}

		mapping.Set(job.Filename, contentID)
		result.NewUploads[job.Filename] = contentID
		s.logger.Info("uploaded", "file", job.Filename, "content_id", contentID, "matte", job.Matte)
		result.Uploaded++
		uploadsDone++
	}

	if len(plan.ToDeleteIDs) > 0 {
		if policy.DryRun {
			s.logger.Info("[DRY RUN] would delete tracked images", "count", len(plan.ToDeleteIDs))
			result.Deleted = len(plan.ToDeleteIDs)
		} else {
			s.logger.Info("deleting tracked images", "count", len(plan.ToDeleteIDs))
			if err := transport.DeleteImages(ctx, plan.ToDeleteIDs); err != nil {
				s.logger.Error("batch delete failed", "error", err)
				result.Deleted = len(plan.ToDeleteIDs)
			} else {
				result.DeletedFiles = append(result.DeletedFiles, plan.ToDeleteFiles...)
				mapping.DeleteBatch(plan.ToDeleteFiles)
				s.logger.Info("deleted tracked images", "count", len(plan.ToDeleteIDs))
				result.Deleted = len(plan.ToDeleteIDs)
			}
		}
	}

	if len(plan.ToDeleteUnknownIDs) > 0 {
		if policy.DryRun {
			s.logger.Info("[DRY RUN] would delete unknown images", "count", len(plan.ToDeleteUnknownIDs))
		} else {
			s.logger.Info("deleting unknown images", "count", len(plan.ToDeleteUnknownIDs))
			if err := transport.DeleteImages(ctx, plan.ToDeleteUnknownIDs); err != nil {
				s.logger.Error("delete unknown images failed", "error", err)
			}
		}
	}

	finalMapping := mapping.AllContentIDs()
	s.applySelectionAndSlideshowPlan(ctx, plan, transport, finalMapping)

	s.updateSlideshowPlan(ctx, plan, transport)
	s.updateBrightnessPlan(ctx, plan, transport, &result)
	s.handleAutoOffPlan(ctx, plan, transport)

	result.TotalImages = plan.TrackedFilesCount + result.Uploaded - result.Deleted
	return result, nil
}

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
