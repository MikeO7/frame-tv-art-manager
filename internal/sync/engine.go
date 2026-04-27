package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/brightness"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/resilience"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
	"github.com/MikeO7/frame-tv-art-manager/internal/sanitize"
	"github.com/MikeO7/frame-tv-art-manager/internal/schedule"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

// Engine orchestrates artwork synchronization across all configured TVs.
// It implements the full sync lifecycle: sources → optimize → scan →
// connect → diff → upload → delete → select → brightness → auto-off.
type Engine struct {
	cfg       *config.Config
	logger    *slog.Logger
	backoff   *resilience.Backoff
	health    *health.Status
	srcLoader *sources.Loader
	cycleNum  int
}

// NewEngine creates a sync engine with the given configuration.
func NewEngine(cfg *config.Config, logger *slog.Logger, healthStatus *health.Status) *Engine {
	return &Engine{
		cfg:     cfg,
		logger:  logger,
		backoff: resilience.NewBackoff(logger),
		health:  healthStatus,
		srcLoader: sources.NewLoader(
			cfg.SourcesFile,
			cfg.ArtworkDir,
			logger,
		),
	}
}

// RunLoop executes RunOnce on a repeating interval until the context is
// cancelled. This is the primary entry point for the long-running service.
func (e *Engine) RunLoop(ctx context.Context) error {
	e.logger.Info("starting sync loop",
		"tvs", len(e.cfg.TVIPs),
		"interval_min", e.cfg.SyncIntervalMin,
		"artwork_dir", e.cfg.ArtworkDir,
	)

	// Run immediately on startup.
	if err := e.RunOnce(ctx); err != nil {
		e.logger.Error("sync cycle failed", "error", err)
	}

	ticker := time.NewTicker(time.Duration(e.cfg.SyncIntervalMin) * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			e.logger.Info("shutting down sync loop")
			return ctx.Err()
		case <-ticker.C:
			if err := e.RunOnce(ctx); err != nil {
				e.logger.Error("sync cycle failed", "error", err)
			}
		}
	}
}

// RunOnce performs a single sync cycle for all configured TVs.
func (e *Engine) RunOnce(ctx context.Context) (err error) {
	var syncErrors []error
	defer func() {
		if e.health != nil {
			e.health.RecordSync(err == nil && len(syncErrors) == 0)
		}
	}()

	e.cycleNum++
	startTime := time.Now()
	e.logger.Info("starting sync cycle",
		"cycle", e.cycleNum,
		"tvs", len(e.cfg.TVIPs),
	)

	// Step 0: Download images from URL sources.
	srcDownloaded, srcErr := e.srcLoader.Sync()
	if srcErr != nil {
		e.logger.Warn("source download error", "error", srcErr)
	}

	// Step 1: Scan local artwork directory.
	localFiles, err := ScanArtworkDir(e.cfg.ArtworkDir)
	if err != nil {
		return fmt.Errorf("scan artwork: %w", err)
	}

	// Step 2: Optimize oversized images.
	optimized := 0
	if e.cfg.OptimizeEnabled {
		optCfg := optimize.Config{
			Enabled:     true,
			MaxWidth:    e.cfg.OptimizeMaxWidth,
			MaxHeight:   e.cfg.OptimizeMaxHeight,
			JPEGQuality: e.cfg.OptimizeJPEGQuality,
		}
		for filename := range localFiles {
			path := filepath.Join(e.cfg.ArtworkDir, filename)
			ok, err := optimize.OptimizeFile(path, optCfg, e.logger)
			if err != nil {
				e.logger.Warn("optimize failed", "file", filename, "error", err)
			}
			if ok {
				optimized++
			}
		}
	}

	e.logger.Info("local artwork ready",
		"total", len(localFiles),
		"from_sources", srcDownloaded,
		"optimized", optimized,
	)

	// Step 3: Sync each TV sequentially.
	tvSummaries := make([]tvSyncSummary, 0, len(e.cfg.TVIPs))

	for _, ip := range e.cfg.TVIPs {
		// Check backoff.
		if e.backoff.ShouldSkip(ip) {
			tvSummaries = append(tvSummaries, tvSyncSummary{
				IP:     ip,
				Status: "backoff",
			})
			continue
		}

		summary, err := e.syncTV(ctx, ip, localFiles)
		if err != nil {
			e.logger.Error("TV sync failed", "tv", ip, "error", err)
			syncErrors = append(syncErrors, fmt.Errorf("tv %s: %w", ip, err))
			e.backoff.RecordFailure(ip, time.Duration(e.cfg.SyncIntervalMin)*time.Minute)
			if e.health != nil {
				e.health.SetTVStatus(ip, health.TVStatus{
					IP:     ip,
					Status: "unreachable",
				})
			}
		} else {
			e.backoff.RecordSuccess(ip)
			if e.health != nil {
				e.health.SetTVStatus(ip, health.TVStatus{
					IP:         ip,
					LastSeen:   time.Now().Format(time.RFC3339),
					ImageCount: summary.TotalImages,
					ArtMode:    summary.ArtMode,
					Status:     "ok",
				})
			}
		}
		tvSummaries = append(tvSummaries, summary)
	}

	// Print summary.
	e.printSummary(startTime, len(localFiles), srcDownloaded, optimized, tvSummaries)

	if len(syncErrors) > 0 {
		return errors.Join(syncErrors...)
	}
	return nil
}

// tvSyncSummary captures results for summary logging.
type tvSyncSummary struct {
	IP          string
	Model       string
	Status      string // "ok", "skipped", "backoff", "error"
	ArtMode     bool
	Uploaded    int
	Deleted     int
	TotalImages int
	Brightness  string
	Slideshow   string
}

// syncTV performs the full sync for a single TV.
func (e *Engine) syncTV(ctx context.Context, ip string, localFiles map[string]struct{}) (tvSyncSummary, error) {
	log := e.logger.With("tv", ip)
	summary := tvSyncSummary{IP: ip}

	// Connect to TV.
	client := samsung.NewClient(ip, e.cfg, e.logger)
	if err := client.Connect(ctx); err != nil {
		if errors.Is(err, samsung.ErrGateFailed) {
			log.Info("skipping — REST gate says TV is busy")
			summary.Status = "skipped (gate)"
			return summary, nil
		}
		summary.Status = "error"
		return summary, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Get model info.
	if info := client.DeviceInfo(); info != nil {
		summary.Model = info.ModelName
	}

	// Check art mode.
	if !client.IsInArtMode(ctx) {
		log.Info("skipping — TV not in art mode")
		summary.Status = "skipped (not art mode)"
		return summary, nil
	}
	summary.ArtMode = true

	// Save detailed metadata for auditing.
	if err := client.SaveMetadata(ctx); err != nil {
		log.Warn("could not save metadata", "error", err)
	}

	// Load filename→content_id mapping.
	mapping, err := LoadMapping(e.cfg.TokenDir, ip)
	if err != nil {
		summary.Status = "error"
		return summary, fmt.Errorf("load mapping: %w", err)
	}

	// Load per-image matte config.
	matteConfig := LoadMatteConfig(e.cfg.ArtworkDir)

	// Get list of images currently on the TV.
	tvContent, err := client.GetUploadedImages(ctx)
	if err != nil {
		summary.Status = "error"
		return summary, fmt.Errorf("get TV images: %w", err)
	}

	// Reconcile: split TV content into tracked vs unknown.
	trackedFiles := make(map[string]struct{})
	unknownIDs := make(map[string]struct{})

	allMappings := mapping.AllContentIDs()
	reverseMap := make(map[string]string, len(allMappings))
	for filename, cid := range allMappings {
		reverseMap[cid] = filename
	}

	for _, item := range tvContent {
		if filename, ok := reverseMap[item.ContentID]; ok {
			trackedFiles[filename] = struct{}{}
		} else {
			unknownIDs[item.ContentID] = struct{}{}
		}
	}

	log.Info("TV inventory",
		"tracked", len(trackedFiles),
		"unknown", len(unknownIDs),
	)

	// Diff: determine uploads and deletes.
	toUpload := diffSets(localFiles, trackedFiles)
	toDelete := diffSets(trackedFiles, localFiles)

	// Log unknown image handling.
	if len(unknownIDs) > 0 {
		if e.cfg.RemoveUnknownImages {
			log.Info("will remove unknown images", "count", len(unknownIDs))
		} else {
			log.Warn("unknown images on TV (set REMOVE_UNKNOWN_IMAGES=true to remove)",
				"count", len(unknownIDs))
		}
	}

	log.Info("sync plan",
		"to_upload", len(toUpload),
		"to_delete", len(toDelete),
		"unknown_to_delete", boolCount(e.cfg.RemoveUnknownImages, len(unknownIDs)),
	)

	// Determine slideshow settings.
	var desiredSlideshow *samsung.SlideshowStatus
	if e.cfg.SlideshowOverride && e.cfg.SlideshowEnabled {
		ssType := "shuffleslideshow"
		if e.cfg.SlideshowType == "sequential" {
			ssType = "slideshow"
		}
		desiredSlideshow = &samsung.SlideshowStatus{
			Value:      fmt.Sprintf("%d", e.cfg.SlideshowInterval),
			Type:       ssType,
			CategoryID: "MY-C0002",
		}
	}

	// Preserve current slideshow if no override and we're making changes.
	var preserveSlideshow *samsung.SlideshowStatus
	hasChanges := len(toUpload) > 0 || len(toDelete) > 0 || (e.cfg.RemoveUnknownImages && len(unknownIDs) > 0)
	if hasChanges && !e.cfg.SlideshowOverride {
		preserveSlideshow, _ = client.SlideshowStatus(ctx)
	}

	// Upload new images.
	for filename := range toUpload {
		if e.cfg.DryRun {
			log.Info("[DRY RUN] would upload", "file", filename)
			summary.Uploaded++
			continue
		}

		displayName := filename
		filename = sanitize.Filename(filename)
		filePath := filepath.Join(e.cfg.ArtworkDir, displayName) // use original for disk path
		fileType := FileTypeFromExt(filename)
		matte := matteConfig.GetMatte(displayName, e.cfg.MatteStyle)

		log.Info("uploading", "file", displayName, "sanitized", filename, "matte", matte)

		contentID, err := client.Upload(ctx, filePath, fileType)
		if err != nil {
			log.Error("upload failed", "file", filename, "error", err)
			time.Sleep(e.cfg.UploadDelay * 2)
			continue
		}

		mapping.Set(filename, contentID)
		log.Info("uploaded", "file", filename, "content_id", contentID)
		summary.Uploaded++

		time.Sleep(e.cfg.UploadDelay)
	}

	// Batch save mapping after all uploads
	if summary.Uploaded > 0 && !e.cfg.DryRun {
		if err := mapping.Save(); err != nil {
			log.Error("failed to save mapping after uploads", "error", err)
		}
	}

	// Delete tracked images no longer in local directory.
	if len(toDelete) > 0 {
		var idsToDelete []string
		for filename := range toDelete {
			if cid, ok := mapping.GetContentID(filename); ok {
				idsToDelete = append(idsToDelete, cid)
			}
		}

		if len(idsToDelete) > 0 {
			if e.cfg.DryRun {
				log.Info("[DRY RUN] would delete tracked images", "count", len(idsToDelete))
			} else {
				log.Info("deleting tracked images", "count", len(idsToDelete))
				if err := client.DeleteImages(ctx, idsToDelete); err != nil {
					log.Error("batch delete failed", "error", err)
				} else {
					for filename := range toDelete {
						mapping.Delete(filename)
					}
					if err := mapping.Save(); err != nil {
						log.Error("failed to save mapping after delete", "error", err)
					}
					log.Info("deleted tracked images", "count", len(idsToDelete))
				}
			}
			summary.Deleted = len(idsToDelete)
		}
	}

	// Delete unknown images if configured.
	if e.cfg.RemoveUnknownImages && len(unknownIDs) > 0 {
		ids := setToSlice(unknownIDs)
		if e.cfg.DryRun {
			log.Info("[DRY RUN] would delete unknown images", "count", len(ids))
		} else {
			log.Info("deleting unknown images", "count", len(ids))
			if err := client.DeleteImages(ctx, ids); err != nil {
				log.Error("delete unknown images failed", "error", err)
			}
		}
	}

	// Select an image + restore slideshow after changes.
	if hasChanges && len(localFiles) > 0 {
		verifiedMap := mapping.AllContentIDs()
		if len(verifiedMap) > 0 {
			var selectedID string

			settingsForMode := desiredSlideshow
			if settingsForMode == nil {
				settingsForMode = preserveSlideshow
			}

			if settingsForMode != nil && settingsForMode.Type == "shuffleslideshow" {
				values := mapValues(verifiedMap)
				selectedID = values[rand.IntN(len(values))]
				log.Info("selecting random image for shuffle mode")
			} else if len(verifiedMap) > 0 {
				for _, id := range verifiedMap {
					selectedID = id
					break
				}
				log.Info("selecting first image")
			}

			if selectedID != "" && !e.cfg.DryRun {
				if err := client.SelectImage(ctx, selectedID); err != nil {
					log.Warn("failed to select image", "error", err)
				}
			}

			if preserveSlideshow != nil && !e.cfg.DryRun {
				if err := client.SetSlideshow(ctx, *preserveSlideshow); err != nil {
					log.Warn("failed to restore slideshow", "error", err)
				}
			}
		}
	}

	// Apply slideshow override.
	if desiredSlideshow != nil && !e.cfg.DryRun {
		current, _ := client.SlideshowStatus(ctx)
		needsUpdate := current == nil ||
			current.Value != desiredSlideshow.Value ||
			current.Type != desiredSlideshow.Type

		if needsUpdate {
			log.Info("updating slideshow settings",
				"interval", desiredSlideshow.Value,
				"type", desiredSlideshow.Type,
			)
			if err := client.SetSlideshow(ctx, *desiredSlideshow); err != nil {
				log.Warn("failed to set slideshow", "error", err)
			}
		}
		summary.Slideshow = fmt.Sprintf("%s every %s min", desiredSlideshow.Type, desiredSlideshow.Value)
	}

	// Apply brightness.
	brightnessVal := e.determineBrightness()
	if brightnessVal != nil && !e.cfg.DryRun {
		if err := client.SetBrightness(ctx, *brightnessVal); err != nil {
			log.Warn("failed to set brightness", "error", err)
		}
		summary.Brightness = fmt.Sprintf("%d", *brightnessVal)
	}

	// Auto-off check.
	if schedule.IsWithinAutoOffWindow(e.cfg.AutoOffTime, e.cfg.AutoOffGraceHours, e.cfg.Timezone) {
		log.Info("within auto-off window, turning off TV",
			"off_time", e.cfg.AutoOffTime,
			"grace_hours", schedule.FormatGraceDisplay(e.cfg.AutoOffGraceHours),
		)
		if !e.cfg.DryRun {
			if err := client.TurnOff(ctx); err != nil {
				log.Warn("failed to turn off TV", "error", err)
			} else {
				log.Info("TV turned off")
			}
		}
	}

	// Calculate total images on TV.
	summary.TotalImages = len(trackedFiles) + summary.Uploaded - summary.Deleted
	summary.Status = "ok"

	log.Info("sync completed")
	return summary, nil
}

// determineBrightness calculates the brightness to apply.
func (e *Engine) determineBrightness() *int {
	if e.cfg.SolarEnabled {
		b, err := brightness.Calculate(
			e.cfg.Latitude, e.cfg.Longitude,
			e.cfg.Timezone,
			e.cfg.BrightnessMin, e.cfg.BrightnessMax,
		)
		if err != nil {
			e.logger.Warn("solar brightness calculation failed", "error", err)
		}
		if b != nil {
			e.logger.Info("solar brightness", "value", *b)
			return b
		}
	}

	if e.cfg.ManualBrightness != nil {
		e.logger.Info("manual brightness", "value", *e.cfg.ManualBrightness)
		return e.cfg.ManualBrightness
	}

	return nil
}

// printSummary outputs a formatted sync cycle summary.
func (e *Engine) printSummary(startTime time.Time, totalLocal, fromSources, optimized int, tvs []tvSyncSummary) {
	elapsed := time.Since(startTime).Round(time.Millisecond)
	nextSync := time.Now().Add(time.Duration(e.cfg.SyncIntervalMin) * time.Minute)

	var sb strings.Builder
	sb.WriteString("\n╔══════════════════════════════════════════════════╗\n")
	sb.WriteString(fmt.Sprintf("║  Sync Cycle #%-3d — %-27s ║\n", e.cycleNum, time.Now().Format("2006-01-02 15:04:05")))
	sb.WriteString("╠══════════════════════════════════════════════════╣\n")

	for _, tv := range tvs {
		name := tv.IP
		if tv.Model != "" {
			name = fmt.Sprintf("%s (%s)", tv.IP, tv.Model)
		}
		sb.WriteString(fmt.Sprintf("║  TV: %-44s║\n", name))

		switch tv.Status {
		case "ok":
			sb.WriteString("║    Status:     ✔ Art Mode                        ║\n")
			sb.WriteString(fmt.Sprintf("║    Uploaded:   %-3d new  │  Deleted: %-3d            ║\n", tv.Uploaded, tv.Deleted))
			sb.WriteString(fmt.Sprintf("║    Total:      %-3d images on TV                   ║\n", tv.TotalImages))
			if tv.Brightness != "" {
				sb.WriteString(fmt.Sprintf("║    Brightness: %-35s║\n", tv.Brightness))
			}
			if tv.Slideshow != "" {
				sb.WriteString(fmt.Sprintf("║    Slideshow:  %-35s║\n", tv.Slideshow))
			}
		case "backoff":
			sb.WriteString("║    Status:     ⏸ Backing off (unreachable)        ║\n")
		default:
			sb.WriteString(fmt.Sprintf("║    Status:     %-35s║\n", tv.Status))
		}
		sb.WriteString("╠══════════════════════════════════════════════════╣\n")
	}

	fmt.Fprintf(&sb, "║  Local:  %-3d images", totalLocal)
	if fromSources > 0 {
		fmt.Fprintf(&sb, "  │  %d from URLs", fromSources)
	}
	if optimized > 0 {
		fmt.Fprintf(&sb, "  │  %d resized", optimized)
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "║  Took:   %-40s║\n", elapsed.String())
	fmt.Fprintf(&sb, "║  Next:   %-40s║\n", nextSync.Format("15:04:05"))
	sb.WriteString("╚══════════════════════════════════════════════════╝\n")

	e.logger.Info(sb.String())
}

// --- helpers ---

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
