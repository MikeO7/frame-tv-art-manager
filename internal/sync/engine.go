package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/brightness"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
)

const (
	ssTypeShuffle    = "shuffleslideshow"
	ssTypeSequential = "slideshow"
)

// Engine orchestrates artwork synchronization across all configured TVs.
type Engine struct {
	cfg               *config.Config
	logger            *slog.Logger
	backoff           *Backoff
	health            *health.Status
	srcLoader         *sources.Loader
	collection        *Collection
	cycleNum          int
	lastMetadataSaves map[string]time.Time
	newClient         func(ip string, cfg *config.Config, logger *slog.Logger) TVClient
}

// TVClient defines the interface for interacting with a Samsung TV.
type TVClient interface {
	Sync(ctx context.Context, req samsung.SyncRequest) (samsung.SyncResult, error)
	Close() error
}

func defaultNewClient(ip string, cfg *config.Config, logger *slog.Logger) TVClient {
	return samsung.NewClient(ip, cfg, logger)
}

// NewEngine creates a sync engine with the given configuration.
func NewEngine(cfg *config.Config, logger *slog.Logger, healthStatus *health.Status) *Engine {
	return &Engine{
		cfg:               cfg,
		logger:            logger,
		backoff:           NewBackoff(logger),
		health:            healthStatus,
		srcLoader:         sources.NewLoader(cfg, logger),
		collection:        NewCollection(cfg, logger),
		lastMetadataSaves: make(map[string]time.Time),
		newClient:         defaultNewClient,
	}
}

// RunLoop executes RunOnce on a repeating interval until the context is cancelled.
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
	localFiles, optimized, err = e.collection.ScanAndOptimize(cycleLog)
	if err != nil {
		return err
	}

	if e.health != nil {
		e.health.SetStage("syncing TVs")
	}
	tvSummaries := make([]tvSyncSummary, 0, len(e.cfg.TVIPs))
	var summariesMu sync.Mutex
	var wg sync.WaitGroup

	matteConfig := LoadMatteConfig(e.cfg.ArtworkDir)

	for _, ip := range e.cfg.TVIPs {
		select {
		case <-ctx.Done():
			e.logger.Info("sync cycle cancelled due to shutdown")
			return ctx.Err()
		default:
		}

		wg.Add(1)
		go func(tvIP string) {
			defer wg.Done()

			if e.backoff.ShouldSkip(tvIP) {
				summariesMu.Lock()
				tvSummaries = append(tvSummaries, tvSyncSummary{
					IP:     tvIP,
					Status: "backoff",
				})
				summariesMu.Unlock()
				return
			}

			summary, err := e.syncTV(ctx, tvIP, localFiles, matteConfig, cycleLog)

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

	e.printSummary(startTime, len(localFiles), srcDownloaded, optimized, tvSummaries, cycleWarnings)

	if len(syncErrors) > 0 {
		return errors.Join(syncErrors...)
	}
	return nil
}

const (
	statusBackoff = "backoff"
	statusError   = "error"
)

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

func (e *Engine) syncTV(ctx context.Context, ip string, localFiles map[string]struct{}, matteConfig *MatteConfig, cycleLog *slog.Logger) (tvSyncSummary, error) {
	log := cycleLog.With("tv", ip)
	summary := tvSyncSummary{IP: ip}

	client := e.newClient(ip, e.cfg, e.logger)
	defer func() { _ = client.Close() }()

	mapping, err := e.collection.GetMapping(ip)
	if err != nil {
		summary.Status = statusError
		return summary, fmt.Errorf("load mapping: %w", err)
	}

	desiredSlideshow := e.determineSlideshowSettings(log)
	brightnessVal := e.determineBrightness(log)

	triggerAutoOff := IsWithinAutoOffWindow(e.cfg.AutoOffTime, e.cfg.AutoOffGraceHours, e.cfg.Timezone)
	if triggerAutoOff {
		log.Info("within auto-off window, preparing TV power-off trigger",
			"off_time", e.cfg.AutoOffTime,
			"grace_hours", FormatGraceDisplay(e.cfg.AutoOffGraceHours),
		)
	}

	matteOverrides := make(map[string]string)
	for filename := range localFiles {
		matteOverrides[filename] = matteConfig.GetMatte(filename, e.cfg.MatteStyle)
	}

	syncRequest := samsung.SyncRequest{
		LocalFiles:        localFiles,
		Mapping:           mapping.AllContentIDs(),
		MatteOverrides:    matteOverrides,
		DesiredBrightness: brightnessVal,
		Slideshow:         desiredSlideshow,
		TriggerAutoOff:    triggerAutoOff,
	}

	result, err := client.Sync(ctx, syncRequest)
	if err != nil {
		summary.Status = statusError
		return summary, err
	}

	mappingUpdated := false
	if len(result.NewUploads) > 0 {
		for f, id := range result.NewUploads {
			mapping.Set(f, id)
		}
		mappingUpdated = true
	}
	if len(result.DeletedFiles) > 0 {
		mapping.DeleteBatch(result.DeletedFiles)
		mappingUpdated = true
	}

	if mappingUpdated && !e.cfg.DryRun {
		if err := mapping.Save(); err != nil {
			log.Error("failed to save mapping", "error", err)
		}
	}

	summary.Model = result.Model
	summary.Status = result.Status
	summary.ArtMode = result.ArtMode
	summary.Uploaded = result.Uploaded
	summary.Deleted = result.Deleted
	summary.TotalImages = result.TotalImages
	summary.Brightness = result.Brightness
	summary.Slideshow = result.Slideshow
	summary.ErrorMessage = result.ErrorMessage

	return summary, nil
}

func (e *Engine) determineSlideshowSettings(log *slog.Logger) *samsung.SlideshowStatus {
	if !e.cfg.SlideshowOverride || !e.cfg.SlideshowEnabled {
		return nil
	}

	ssType := ssTypeShuffle
	if e.cfg.SlideshowType == "sequential" || e.cfg.SlideshowType == "order" {
		ssType = ssTypeSequential
	}

	interval := fmt.Sprintf("%d", e.cfg.SlideshowInterval)

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

	return &samsung.SlideshowStatus{
		Value:      interval,
		Type:       ssType,
		CategoryID: "MY-C0002",
	}
}

func (e *Engine) determineBrightness(log *slog.Logger) *int {
	return brightness.GetTargetValue(brightness.Config{
		SolarEnabled:     e.cfg.SolarEnabled,
		Latitude:         e.cfg.Latitude,
		Longitude:        e.cfg.Longitude,
		Timezone:         e.cfg.Timezone,
		BrightnessMin:    e.cfg.BrightnessMin,
		BrightnessMax:    e.cfg.BrightnessMax,
		ManualBrightness: e.cfg.ManualBrightness,
	}, log)
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

func (e *Engine) printSummary(startTime time.Time, totalLocal, fromSources, optimized int, tvs []tvSyncSummary, warnings []string) {
	elapsed := time.Since(startTime).Round(time.Millisecond)
	nextSync := time.Now().Add(time.Duration(e.cfg.SyncIntervalMin) * time.Minute)

	const boxWidth = 50

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
				errMsg := tv.ErrorMessage
				if len(errMsg) > 35 {
					errMsg = errMsg[:32] + "..."
				}
				sb.WriteString(padLine("    Error:      " + errMsg))
			}
		}
		sb.WriteString("╠══════════════════════════════════════════════════╣\n")
	}

	localSummary := fmt.Sprintf("  Local:  %d files", totalLocal)
	if fromSources > 0 {
		localSummary += fmt.Sprintf(" │ %d from URLs", fromSources)
	}
	if optimized > 0 {
		localSummary += fmt.Sprintf(" │ %d optimized", optimized)
	}
	sb.WriteString(padLine(localSummary))

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

// --- Backwards Compatible Delegation Wrappers for Testing ---

func (e *Engine) optimizeLocalArtwork(localFiles map[string]struct{}, cycleLog *slog.Logger) int {
	return e.collection.OptimizeLocalArtwork(localFiles, cycleLog)
}

func (e *Engine) ensureCorrectFilename(filename string, newW, newH int, modified bool, localFiles map[string]struct{}, mu *sync.Mutex) {
	e.collection.EnsureCorrectFilename(filename, newW, newH, modified, localFiles, mu)
}

func (e *Engine) updateMappings(oldName, newName string) {
	e.collection.UpdateMappings(oldName, newName)
}
