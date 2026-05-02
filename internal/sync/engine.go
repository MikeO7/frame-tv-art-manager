package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
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

const (
	ssTypeShuffle    = "shuffleslideshow"
	ssTypeSequential = "slideshow"
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
	mappings          map[string]*Mapping
	mapMu             sync.Mutex
	lastLocalFiles    map[string]struct{}
	lastDirModTime    time.Time
	lastMetadataSaves map[string]time.Time
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
			cfg.UnsplashAppID,
			cfg.UnsplashAccessKey,
			cfg.UnsplashSecretKey,
			cfg.NasaApiKey,
			cfg.PexelsApiKey,
			cfg.PixabayApiKey,
			cfg.MaxArtworkImages,
			cfg.MaxDownloadSizeMB,
			logger,
		),
		mappings:          make(map[string]*Mapping),
		lastMetadataSaves: make(map[string]time.Time),
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
	var cycleWarnings []string
	defer func() {
		if e.health != nil {
			var finalErr error
			if err != nil {
				finalErr = err
			} else if len(syncErrors) > 0 {
				finalErr = errors.Join(syncErrors...)
			}
			e.health.RecordSync(finalErr == nil, finalErr)
			e.health.SetStage("idle")
		}

		// Force Go to release unused memory back to the OS during the idle period.
		// Image processing allocates large buffers that cause docker memory stats to stay high.
		runtime.GC()
		debug.FreeOSMemory()
	}()

	e.cycleNum++
	cycleLog := e.logger.With("cycle", e.cycleNum)

	startTime := time.Now()
	cycleLog.Info("starting sync cycle",
		"tvs", len(e.cfg.TVIPs),
	)

	var srcDownloaded int
	srcDownloaded, cycleWarnings = e.downloadSources(cycleLog)

	var localFiles map[string]struct{}
	var optimized int
	localFiles, optimized, err = e.scanAndOptimize(cycleLog)
	if err != nil {
		return err
	}

	// Step 3: Sync each TV in parallel.
	if e.health != nil {
		e.health.SetStage("syncing TVs")
	}
	tvSummaries := make([]tvSyncSummary, 0, len(e.cfg.TVIPs))
	var summariesMu sync.Mutex
	var wg sync.WaitGroup

	for _, ip := range e.cfg.TVIPs {
		// Respect shutdown context.
		select {
		case <-ctx.Done():
			e.logger.Info("sync cycle cancelled due to shutdown")
			return ctx.Err()
		default:
		}

		wg.Add(1)
		go func(tvIP string) {
			defer wg.Done()

			// Check backoff.
			if e.backoff.ShouldSkip(tvIP) {
				summariesMu.Lock()
				tvSummaries = append(tvSummaries, tvSyncSummary{
					IP:     tvIP,
					Status: "backoff",
				})
				summariesMu.Unlock()
				return
			}

			summary, err := e.syncTV(ctx, tvIP, localFiles, cycleLog)

			summariesMu.Lock()
			defer summariesMu.Unlock()

			if err != nil {
				e.logger.Error("TV sync failed", "tv", tvIP, "error", err)
				syncErrors = append(syncErrors, fmt.Errorf("tv %s: %w", tvIP, err))
				e.backoff.RecordFailure(tvIP, time.Duration(e.cfg.SyncIntervalMin)*time.Minute)
				if e.health != nil {
					e.health.SetTVStatus(tvIP, health.TVStatus{
						IP:     tvIP,
						Status: "unreachable",
					})
				}
				tvSummaries = append(tvSummaries, tvSyncSummary{
					IP:           tvIP,
					Status:       "failed",
					ErrorMessage: err.Error(),
				})
			} else {
				e.backoff.RecordSuccess(tvIP)
				if e.health != nil {
					e.health.SetTVStatus(tvIP, health.TVStatus{
						IP:         tvIP,
						LastSeen:   time.Now().Format(time.RFC3339),
						ImageCount: summary.TotalImages,
						ArtMode:    summary.ArtMode,
						Status:     "ok",
					})
				}
				tvSummaries = append(tvSummaries, summary)
			}
		}(ip)
	}

	wg.Wait()

	// Print summary.
	e.printSummary(startTime, len(localFiles), srcDownloaded, optimized, tvSummaries, cycleWarnings)

	if len(syncErrors) > 0 {
		return errors.Join(syncErrors...)
	}
	return nil
}

// Status string constants for TV summary logging.
const (
	statusBackoff = "backoff"
	statusError   = "error"
)

// tvSyncSummary captures results for summary logging.
type tvSyncSummary struct {
	IP           string
	Model        string
	Status       string // "ok", "skipped", "backoff", "error"
	ArtMode      bool
	Uploaded     int
	Deleted      int
	TotalImages  int
	Brightness   string
	Slideshow    string
	ErrorMessage string
}

// syncTV performs the full sync for a single TV.
//
//nolint:gocyclo // Core sync loop requires complex flow control
func (e *Engine) syncTV(ctx context.Context, ip string, localFiles map[string]struct{}, cycleLog *slog.Logger) (tvSyncSummary, error) {
	log := cycleLog.With("tv", ip)
	summary := tvSyncSummary{IP: ip}

	// Connect to TV.
	client := samsung.NewClient(ip, e.cfg, e.logger)
	if err := client.Connect(ctx); err != nil {
		if errors.Is(err, samsung.ErrGateFailed) {
			log.Info("skipping — REST gate says TV is busy")
			summary.Status = "skipped (gate)"
			return summary, nil
		}
		summary.Status = statusError
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

	// Save detailed metadata for auditing in the background.
	// Throttled to once per hour per TV to reduce redundant network/disk ops.
	e.mapMu.Lock()
	lastSave := e.lastMetadataSaves[ip]
	shouldSave := time.Since(lastSave) > 1*time.Hour
	if shouldSave {
		e.lastMetadataSaves[ip] = time.Now()
	}
	e.mapMu.Unlock()

	if shouldSave {
		go func(bgCtx context.Context) {
			// Use a sub-timeout to ensure it doesn't leak if the TV stalls.
			ctx, cancel := context.WithTimeout(bgCtx, 1*time.Minute)
			defer cancel()
			if err := client.SaveMetadata(ctx); err != nil {
				log.Debug("could not save metadata", "error", err)
			}
		}(ctx)
	}

	// Load filename→content_id mapping from cache or disk.
	mapping, err := e.getMapping(ip)
	if err != nil {
		summary.Status = statusError
		return summary, fmt.Errorf("load mapping: %w", err)
	}

	// Load per-image matte config.
	matteConfig := LoadMatteConfig(e.cfg.ArtworkDir)

	// Get list of images currently on the TV.
	tvContent, err := client.GetUploadedImages(ctx)
	if err != nil {
		summary.Status = statusError
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

	// Reconciliation: Purge database entries for images no longer on the TV.
	// This ensures the mapping always reflects the actual state of the TV.
	liveIDs := make(map[string]bool)
	for _, item := range tvContent {
		liveIDs[item.ContentID] = true
	}

	purgedCount := 0
	for filename, contentID := range mapping.AllContentIDs() {
		if !liveIDs[contentID] {
			log.Debug("purging stale mapping (not on TV)", "file", filename, "id", contentID)
			mapping.Delete(filename)
			purgedCount++
		}
	}

	if purgedCount > 0 {
		log.Info("reconciled database with TV memory", "purged_stale_entries", purgedCount)
		if err := mapping.Save(); err != nil {
			log.Error("failed to save mapping after reconciliation", "error", err)
		}
		// Refresh reverse map after purge.
		reverseMap = mapping.AllContentIDs()
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
		ssType := ssTypeShuffle
		if e.cfg.SlideshowType == "sequential" || e.cfg.SlideshowType == "order" {
			ssType = ssTypeSequential
		}

		interval := fmt.Sprintf("%d", e.cfg.SlideshowInterval)

		// Validate against known supported values for 2024 models
		// Supported: 3, 15, 60 (1h), 720 (12h), 1440 (1d), 10080 (7d)
		isValid := false
		supported := []string{"3", "15", "60", "720", "1440", "10080"}
		for _, s := range supported {
			if interval == s {
				isValid = true
				break
			}
		}

		if !isValid {
			log.Warn("invalid slideshow interval detected for 2024 model, defaulting to 3m shuffle",
				"requested", interval,
				"supported", supported)
			interval = "3"
			ssType = ssTypeShuffle
		}

		desiredSlideshow = &samsung.SlideshowStatus{
			Value:      interval,
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
					var filesToDelete []string
					for filename := range toDelete {
						filesToDelete = append(filesToDelete, filename)
					}
					mapping.DeleteBatch(filesToDelete)

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
				selectedID = values[rand.IntN(len(values))] //nolint:gosec // Not used for crypto
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
	brightnessVal := e.determineBrightness(log)
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
func (e *Engine) determineBrightness(log *slog.Logger) *int {
	if e.cfg.SolarEnabled {
		b, err := brightness.Calculate(
			e.cfg.Latitude, e.cfg.Longitude,
			e.cfg.Timezone,
			e.cfg.BrightnessMin, e.cfg.BrightnessMax,
		)
		if err != nil {
			log.Warn("solar brightness calculation failed", "error", err)
		}
		if b != nil {
			log.Info("solar brightness", "value", *b)
			return b
		}
	}

	if e.cfg.ManualBrightness != nil {
		log.Info("manual brightness", "value", *e.cfg.ManualBrightness)
		return e.cfg.ManualBrightness
	}

	return nil
}

// printSummary outputs a formatted sync cycle summary.
func (e *Engine) printSummary(startTime time.Time, totalLocal, fromSources, optimized int, tvs []tvSyncSummary, warnings []string) {
	elapsed := time.Since(startTime).Round(time.Millisecond)
	nextSync := time.Now().Add(time.Duration(e.cfg.SyncIntervalMin) * time.Minute)

	const boxWidth = 50 // total interior width between borders

	padLine := func(content string) string {
		runes := []rune(content)
		if len(runes) > boxWidth {
			runes = runes[:boxWidth]
		}
		padding := boxWidth - len(runes)
		return "║" + string(runes) + strings.Repeat(" ", padding) + "║\n"
	}

	var sb strings.Builder
	sb.WriteString("\n╔══════════════════════════════════════════════════╗\n")

	header := fmt.Sprintf("  Sync Cycle #%d - %s", e.cycleNum, time.Now().Format("2006-01-02 15:04:05"))
	sb.WriteString(padLine(header))

	sb.WriteString("╠══════════════════════════════════════════════════╣\n")

	for _, tv := range tvs {
		name := tv.IP
		if tv.Model != "" {
			name = fmt.Sprintf("%s (%s)", tv.IP, tv.Model)
		}
		sb.WriteString(padLine("  TV: " + name))

		switch tv.Status {
		case "ok":
			sb.WriteString(padLine("    Status:     ✔ Art Mode"))
			sb.WriteString(padLine(fmt.Sprintf("    Uploaded:   %d new  │  Deleted: %d", tv.Uploaded, tv.Deleted)))
			sb.WriteString(padLine(fmt.Sprintf("    Total:      %d images on TV", tv.TotalImages)))
			if tv.Brightness != "" {
				sb.WriteString(padLine("    Brightness: " + tv.Brightness))
			}
			if tv.Slideshow != "" {
				sb.WriteString(padLine("    Slideshow:  " + tv.Slideshow))
			}
		case "backoff":
			sb.WriteString(padLine("    Status:     ⏸ Backing off (unreachable)"))
		default:
			sb.WriteString(padLine("    Status:     ✘ " + tv.Status))
			if tv.ErrorMessage != "" {
				// Truncate error if too long for the box
				errMsg := tv.ErrorMessage
				if len(errMsg) > 35 {
					errMsg = errMsg[:32] + "..."
				}
				sb.WriteString(padLine("    Error:      " + errMsg))
			}
		}
		sb.WriteString("╠══════════════════════════════════════════════════╣\n")
	}

	// Local collection summary.
	localSummary := fmt.Sprintf("  Local:  %d files", totalLocal)
	if fromSources > 0 {
		localSummary += fmt.Sprintf(" │ %d from URLs", fromSources)
	}
	if optimized > 0 {
		localSummary += fmt.Sprintf(" │ %d optimized", optimized)
	}
	sb.WriteString(padLine(localSummary))

	// Warnings Section if any
	if len(warnings) > 0 {
		sb.WriteString("╠══════════════════════════════════════════════════╣\n")
		sb.WriteString(padLine("  ⚠ Warnings during this cycle:"))
		for _, w := range warnings {
			if len(w) > 44 {
				w = w[:41] + "..."
			}
			sb.WriteString(padLine("  - " + w))
		}
	}

	sb.WriteString("╠══════════════════════════════════════════════════╣\n")
	sb.WriteString(padLine("  Took:   " + elapsed.String()))
	sb.WriteString(padLine("  Next:   " + nextSync.Format("15:04:05")))
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

func (e *Engine) optimizeLocalArtwork(localFiles map[string]struct{}, cycleLog *slog.Logger) int {
	var optimizedCount int64

	optCfg := optimize.Config{
		Enabled:             e.cfg.OptimizeEnabled,
		MaxWidth:            e.cfg.OptimizeMaxWidth,
		MaxHeight:           e.cfg.OptimizeMaxHeight,
		OptimizeJPEGQuality: e.cfg.OptimizeJPEGQuality,
		MuseumModeEnabled:   e.cfg.MuseumModeEnabled,
		MuseumModeIntensity: e.cfg.MuseumModeIntensity,
	}

	// 1. Collect all filenames to process.
	type job struct {
		filename string
	}
	jobs := make(chan job, len(localFiles))
	for filename := range localFiles {
		// Ignore hidden Mac metadata files (AppleDouble).
		if strings.HasPrefix(filename, "._") {
			delete(localFiles, filename)
			continue
		}
		jobs <- job{filename: filename}
	}
	close(jobs)

	// 2. Spawn workers based on CPU core count (minimum 4, max 16).
	numWorkers := runtime.NumCPU()
	if numWorkers < 4 {
		numWorkers = 4
	}
	if numWorkers > 16 {
		numWorkers = 16
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				wasModified, ok := e.handleSingleOptimization(j.filename, localFiles, optCfg, &mu, cycleLog)
				if ok && wasModified {
					atomic.AddInt64(&optimizedCount, 1)
				}
			}
		}()
	}

	wg.Wait()
	return int(optimizedCount)
}

func (e *Engine) handleSingleOptimization(filename string, localFiles map[string]struct{}, optCfg optimize.Config, mu *sync.Mutex, log *slog.Logger) (bool, bool) {
	path := filepath.Join(e.cfg.ArtworkDir, filename)

	// 1. Skip check based on filename metadata (Avoid heavy computing).
	if optCfg.Enabled && strings.Contains(filename, "_opt.h_") {
		w, h, ok := parseDimensions(filename)
		if ok && w <= optCfg.MaxWidth && h <= optCfg.MaxHeight {
			log.Debug("skipping already optimized file", "file", filename, "dims", fmt.Sprintf("%dx%d", w, h))
			return false, true
		}
	}

	// 2. Perform optimization/validation if needed.
	if !optCfg.Enabled {
		if err := optimize.ValidateImage(path); err != nil {
			log.Warn("skipping corrupt image", "file", filename, "error", err)
			mu.Lock()
			delete(localFiles, filename)
			mu.Unlock()
			return false, false
		}
		return false, true
	}

	newW, newH, modified, err := optimize.OptimizeFile(path, optCfg, log)
	if err != nil {
		log.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		mu.Lock()
		delete(localFiles, filename)
		mu.Unlock()
		return false, false
	}

	// 3. Handle renaming if modified or if filename metadata is missing/stale.
	e.ensureCorrectFilename(filename, newW, newH, modified, localFiles, mu)
	return modified, true
}

func (e *Engine) ensureCorrectFilename(filename string, newW, newH int, modified bool, localFiles map[string]struct{}, mu *sync.Mutex) {
	currentW, currentH, _ := parseDimensions(filename)
	isOpt := strings.Contains(filename, "_opt.h_")

	if !modified && isOpt && currentW == newW && currentH == newH {
		return
	}

	ext := filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)
	var hash string

	// Extract identity and hash using both possible separators.
	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
		hash = parts[1]
	} else if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		hash = parts[len(parts)-1]
		identity = strings.Join(parts[:len(parts)-1], "__")
	} else {
		// If no hash/separator found, it is a manually added file.
		// Use a default hash so it gets the canonical format and isn't re-processed on every restart.
		hash = "local"
	}

	// Clean identity by removing existing metadata tags.
	// Strip existing dimensions (e.g., _3840x2160).
	if lastUnderscore := strings.LastIndex(identity, "_"); lastUnderscore != -1 {
		suffix := identity[lastUnderscore+1:]
		if strings.Contains(suffix, "x") {
			var w, h int
			if n, _ := fmt.Sscanf(suffix, "%dx%d", &w, &h); n == 2 {
				identity = identity[:lastUnderscore]
			}
		}
	}
	// Strip _opt if it exists.
	identity = strings.Split(identity, "_opt")[0]

	// Construct canonical optimized filename.
	newFilename := fmt.Sprintf("%s_%dx%d_opt.h_%s%s", identity, newW, newH, hash, ext)
	if newFilename == filename {
		return
	}

	path := filepath.Join(e.cfg.ArtworkDir, filename)
	newPath := filepath.Join(e.cfg.ArtworkDir, newFilename)

	if err := os.Rename(path, newPath); err == nil {
		e.logger.Info("updated optimized filename", "old", filename, "new", newFilename)
		e.updateMappings(filename, newFilename)
		mu.Lock()
		delete(localFiles, filename)
		localFiles[newFilename] = struct{}{}
		mu.Unlock()
	}
}

func (e *Engine) updateMappings(oldName, newName string) {
	for _, ip := range e.cfg.TVIPs {
		m, err := e.getMapping(ip)
		if err != nil {
			continue
		}
		if m.Rename(oldName, newName) {
			if err := m.Save(); err != nil {
				e.logger.Warn("failed to save migrated mapping", "tv", ip, "error", err)
			}
		}
	}
}

// parseDimensions extracts width and height from a filename like "..._3840x2160_opt.h_...".
func parseDimensions(filename string) (int, int, bool) {
	ext := filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)

	// Handle both possible separators to extract clean identity.
	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
	} else if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		identity = strings.Join(parts[:len(parts)-1], "__")
	}

	// Look for [WxH] pattern in the remaining identity.
	parts := strings.Split(identity, "_")
	for _, p := range parts {
		if strings.Contains(p, "x") {
			var w, h int
			if n, _ := fmt.Sscanf(p, "%dx%d", &w, &h); n == 2 {
				return w, h, true
			}
		}
	}
	return 0, 0, false
}

// getMapping returns a cached or newly loaded mapping for a TV.
func (e *Engine) getMapping(ip string) (*Mapping, error) {
	e.mapMu.Lock()
	defer e.mapMu.Unlock()

	if m, ok := e.mappings[ip]; ok {
		return m, nil
	}

	m, err := LoadMapping(e.cfg.TokenDir, ip)
	if err != nil {
		return nil, err
	}

	e.mappings[ip] = m
	return m, nil
}

func (e *Engine) downloadSources(cycleLog *slog.Logger) (int, []string) {
	if e.health != nil {
		e.health.SetStage("downloading sources")
	}
	srcDownloaded, srcErr := e.srcLoader.Sync()
	var cycleWarnings []string
	if srcErr != nil {
		cycleLog.Warn("source download error", "error", srcErr)
		cycleWarnings = append(cycleWarnings, fmt.Sprintf("Source download issue: %v", srcErr))
	}
	return srcDownloaded, cycleWarnings
}

func (e *Engine) scanAndOptimize(cycleLog *slog.Logger) (map[string]struct{}, int, error) {
	if e.health != nil {
		e.health.SetStage("scanning local artwork")
	}

	// Optimization: Skip full disk scan if directory modification time hasn't changed.
	// This saves significant I/O for large collections.
	info, statErr := os.Stat(e.cfg.ArtworkDir)
	if statErr == nil {
		if info.ModTime().Equal(e.lastDirModTime) && e.lastLocalFiles != nil {
			cycleLog.Debug("skipping disk scan — directory ModTime unchanged")
			// We return a copy to prevent concurrent modification of the cache.
			localFiles := make(map[string]struct{}, len(e.lastLocalFiles))
			for k := range e.lastLocalFiles {
				localFiles[k] = struct{}{}
			}
			return localFiles, 0, nil
		}
		e.lastDirModTime = info.ModTime()
	}

	localFiles, err := ScanArtworkDir(e.cfg.ArtworkDir)
	if err != nil {
		return nil, 0, fmt.Errorf("scan artwork: %w", err)
	}
	e.lastLocalFiles = localFiles

	if e.health != nil {
		e.health.SetStage("optimizing artwork")
	}
	optimized := e.optimizeLocalArtwork(localFiles, cycleLog)

	cycleLog.Info("local artwork ready",
		"total", len(localFiles),
		"optimized", optimized,
	)
	return localFiles, optimized, nil
}
