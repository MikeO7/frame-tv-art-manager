package samsung

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"time"
)

// SyncRequest defines the dynamic state and configurations for a single TV sync run.
type SyncRequest struct {
	LocalFiles        map[string]struct{}
	Mapping           map[string]string // filename -> contentID
	MatteOverrides    map[string]string // filename -> matte style
	DesiredBrightness *int
	Slideshow         *SlideshowStatus
	TriggerAutoOff    bool
}

// SyncResult returns the synchronization outcome, including details for updating the mapping cache.
type SyncResult struct {
	Model        string
	Status       string
	ArtMode      bool
	Uploaded     int
	Deleted      int
	TotalImages  int
	Brightness   string
	Slideshow    string
	ErrorMessage string

	// Mapping database updates
	NewUploads   map[string]string // filename -> contentID
	DeletedFiles []string          // list of filenames deleted
}

// Sync performs the complete synchronization cycle for the TV, completely encapsulating
// WOL, connection, art mode checks, inventory diffing, uploads, deletes, and auto-off.
func (c *Client) Sync(ctx context.Context, req SyncRequest) (SyncResult, error) {
	result := SyncResult{
		NewUploads: make(map[string]string),
	}

	if c.ShouldSkip() {
		result.Status = "backoff"
		return result, nil
	}

	// 1. Connect to the TV.
	if err := c.connect(ctx); err != nil {
		if errors.Is(err, ErrGateFailed) {
			c.logger.Info("skipping — REST gate says TV is busy")
			result.Status = "skipped (gate)"
			return result, nil
		}
		c.RecordFailure(time.Duration(c.cfg.SyncIntervalMin) * time.Minute)
		result.Status = "error"
		return result, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = c.Close() }()

	// 2. Fetch basic device info.
	if c.info != nil {
		result.Model = c.info.ModelName
	}

	// 3. Check art mode.
	if !c.isInArtMode(ctx) {
		c.logger.Info("skipping — TV not in art mode")
		result.Status = "skipped (not art mode)"
		return result, nil
	}
	result.ArtMode = true

	// 4. Background metadata save (handled by caller if throttled, or just run inline here)
	if err := c.saveMetadata(ctx); err != nil {
		c.logger.Debug("could not save metadata", "error", err)
	}

	// 5. Query currently uploaded images.
	tvContent, err := c.getUploadedImages(ctx)
	if err != nil {
		c.RecordFailure(time.Duration(c.cfg.SyncIntervalMin) * time.Minute)
		result.Status = "error"
		return result, fmt.Errorf("get TV images: %w", err)
	}

	// 6. Reconciliation: split TV content into tracked vs unknown.
	trackedFiles := make(map[string]struct{})
	unknownIDs := make(map[string]struct{})

	// Reverse map
	reverseMap := make(map[string]string, len(req.Mapping))
	for filename, cid := range req.Mapping {
		reverseMap[cid] = filename
	}

	// Reconcile database entries: find stale mappings that are no longer on the TV.
	liveIDs := make(map[string]bool)
	for _, item := range tvContent {
		liveIDs[item.ContentID] = true
	}

	for filename, contentID := range req.Mapping {
		if !liveIDs[contentID] {
			c.logger.Debug("purging stale mapping (not on TV)", "file", filename, "id", contentID)
			result.DeletedFiles = append(result.DeletedFiles, filename)
		}
	}

	for _, item := range tvContent {
		if filename, ok := reverseMap[item.ContentID]; ok {
			trackedFiles[filename] = struct{}{}
		} else {
			unknownIDs[item.ContentID] = struct{}{}
		}
	}

	c.logger.Info("TV inventory",
		"tracked", len(trackedFiles),
		"unknown", len(unknownIDs),
	)

	// 7. Diff: determine uploads and deletes.
	toUpload := diffSets(req.LocalFiles, trackedFiles)
	toDelete := diffSets(trackedFiles, req.LocalFiles)

	if len(unknownIDs) > 0 {
		if c.cfg.RemoveUnknownImages {
			c.logger.Info("will remove unknown images", "count", len(unknownIDs))
		} else {
			c.logger.Warn("unknown images on TV (set REMOVE_UNKNOWN_IMAGES=true to remove)",
				"count", len(unknownIDs))
		}
	}

	c.logger.Info("sync plan",
		"to_upload", len(toUpload),
		"to_delete", len(toDelete),
		"unknown_to_delete", boolCount(c.cfg.RemoveUnknownImages, len(unknownIDs)),
	)

	// 8. Capture slideshow settings.
	var preserveSlideshow *SlideshowStatus
	hasChanges := len(toUpload) > 0 || len(toDelete) > 0 || (c.cfg.RemoveUnknownImages && len(unknownIDs) > 0)
	if hasChanges && !c.cfg.SlideshowOverride {
		preserveSlideshow, _ = c.slideshowStatus(ctx)
	}

	// 9. Upload new images.
	for filename := range toUpload {
		if c.cfg.DryRun {
			c.logger.Info("[DRY RUN] would upload", "file", filename)
			result.Uploaded++
			continue
		}

		filePath := filepath.Join(c.cfg.ArtworkDir, filename)
		fileType := fileTypeFromExt(filename)

		// Determine matte style
		matte := c.cfg.MatteStyle
		if customMatte, ok := req.MatteOverrides[filename]; ok {
			matte = customMatte
		}

		c.logger.Info("uploading", "file", filename, "matte", matte)

		contentID, err := c.upload(ctx, filePath, fileType)
		if err != nil {
			c.logger.Error("upload failed", "file", filename, "error", err)
			time.Sleep(c.cfg.UploadDelay * 2)
			continue
		}

		result.NewUploads[filename] = contentID
		c.logger.Info("uploaded", "file", filename, "content_id", contentID)
		result.Uploaded++

		time.Sleep(c.cfg.UploadDelay)
	}

	// 10. Delete tracked images.
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
			if c.cfg.DryRun {
				c.logger.Info("[DRY RUN] would delete tracked images", "count", len(idsToDelete))
			} else {
				c.logger.Info("deleting tracked images", "count", len(idsToDelete))
				if err := c.deleteImages(ctx, idsToDelete); err != nil {
					c.logger.Error("batch delete failed", "error", err)
				} else {
					result.DeletedFiles = append(result.DeletedFiles, filesToDelete...)
					c.logger.Info("deleted tracked images", "count", len(idsToDelete))
				}
			}
			result.Deleted = len(idsToDelete)
		}
	}

	// 11. Delete unknown images.
	if c.cfg.RemoveUnknownImages && len(unknownIDs) > 0 {
		ids := setToSlice(unknownIDs)
		if c.cfg.DryRun {
			c.logger.Info("[DRY RUN] would delete unknown images", "count", len(ids))
		} else {
			c.logger.Info("deleting unknown images", "count", len(ids))
			if err := c.deleteImages(ctx, ids); err != nil {
				c.logger.Error("delete unknown images failed", "error", err)
			}
		}
	}

	// 12. Select image and restore/apply slideshow.
	finalMapping := make(map[string]string)
	for k, v := range req.Mapping {
		finalMapping[k] = v
	}
	for _, f := range result.DeletedFiles {
		delete(finalMapping, f)
	}
	for k, v := range result.NewUploads {
		finalMapping[k] = v
	}

	if hasChanges && len(req.LocalFiles) > 0 {
		if len(finalMapping) > 0 {
			var selectedID string

			settingsForMode := req.Slideshow
			if settingsForMode == nil {
				settingsForMode = preserveSlideshow
			}

			if settingsForMode != nil && settingsForMode.Type == "shuffleslideshow" {
				values := mapValues(finalMapping)
				//nolint:gosec
				selectedID = values[rand.IntN(len(values))]
				c.logger.Info("selecting random image for shuffle mode")
			} else if len(finalMapping) > 0 {
				for _, id := range finalMapping {
					selectedID = id
					break
				}
				c.logger.Info("selecting first image")
			}

			if selectedID != "" && !c.cfg.DryRun {
				if err := c.selectImage(ctx, selectedID); err != nil {
					c.logger.Warn("failed to select image", "error", err)
				}
			}

			if preserveSlideshow != nil && !c.cfg.DryRun {
				if err := c.setSlideshow(ctx, *preserveSlideshow); err != nil {
					c.logger.Warn("failed to restore slideshow", "error", err)
				}
			}
		}
	}

	// Apply slideshow override.
	if req.Slideshow != nil && !c.cfg.DryRun {
		current, _ := c.slideshowStatus(ctx)
		needsUpdate := current == nil ||
			current.Value != req.Slideshow.Value ||
			current.Type != req.Slideshow.Type

		if needsUpdate {
			c.logger.Info("updating slideshow settings",
				"interval", req.Slideshow.Value,
				"type", req.Slideshow.Type,
			)
			if err := c.setSlideshow(ctx, *req.Slideshow); err != nil {
				c.logger.Warn("failed to set slideshow", "error", err)
			}
		}
		result.Slideshow = fmt.Sprintf("%s every %s min", req.Slideshow.Type, req.Slideshow.Value)
	}

	// 13. Apply brightness.
	if req.DesiredBrightness != nil && !c.cfg.DryRun {
		if err := c.setBrightness(ctx, *req.DesiredBrightness); err != nil {
			c.logger.Warn("failed to set brightness", "error", err)
		}
		result.Brightness = fmt.Sprintf("%d", *req.DesiredBrightness)
	}

	// 14. Auto-off trigger.
	if req.TriggerAutoOff {
		c.logger.Info("within auto-off window, turning off TV")
		if !c.cfg.DryRun {
			if err := c.turnOff(ctx); err != nil {
				c.logger.Warn("failed to turn off TV", "error", err)
			} else {
				c.logger.Info("TV turned off")
			}
		}
	}

	// 15. Final stats.
	result.TotalImages = len(trackedFiles) + result.Uploaded - result.Deleted
	result.Status = "ok"

	c.RecordSuccess()
	c.logger.Info("sync completed")
	return result, nil
}

// --- Internal Helper Functions ---

func fileTypeFromExt(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".png" {
		return "png"
	}
	return "jpg"
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
