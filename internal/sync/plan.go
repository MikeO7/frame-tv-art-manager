package sync

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/brightness"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// PlanInput bundles the inputs required to plan a TV sync. Grouping them into
// a struct keeps the pure planner free of positional-argument ambiguity.
type PlanInput struct {
	IP                string
	Cfg               *config.Config
	MatteConfig       *config.MatteConfig
	MappingData       map[string]string
	TVContent         []samsung.ArtContent
	LocalFiles        map[string]struct{}
	PreserveSlideshow *samsung.SlideshowStatus
	Logger            *slog.Logger
}

// PlanSync evaluates current states and plans synchronization purely in memory.
func PlanSync(in PlanInput) *Plan {
	policy := in.Cfg.SyncPolicy()

	trackedFiles, unknownIDs, staleFiles := reconcileInventory(in.MappingData, in.TVContent, in.Logger)
	toUploadSet := diffSets(in.LocalFiles, trackedFiles)
	toDeleteSet := diffSets(trackedFiles, in.LocalFiles)

	toUpload := buildUploadJobs(policy, in.MatteConfig, toUploadSet)
	toDeleteIDs, toDeleteFiles := buildDeleteJobs(in.MappingData, toDeleteSet)

	var toDeleteUnknownIDs []string
	if policy.RemoveUnknownImages && len(unknownIDs) > 0 {
		toDeleteUnknownIDs = setToSlice(unknownIDs)
	}

	hasChanges := len(toUpload) > 0 || len(toDeleteIDs) > 0 || len(toDeleteUnknownIDs) > 0

	slideshow := determineSlideshowSettings(in.Cfg, in.Logger)

	return &Plan{
		IP:                 in.IP,
		ToUpload:           toUpload,
		ToDeleteIDs:        toDeleteIDs,
		ToDeleteFiles:      toDeleteFiles,
		ToDeleteUnknownIDs: toDeleteUnknownIDs,
		Slideshow:          slideshow,
		Brightness:         determineBrightness(in.Cfg, in.Logger),
		TurnOff:            isWithinAutoOffWindow(in.Cfg.AutoOffTime, in.Cfg.AutoOffGraceHours, in.Cfg.Timezone),
		TrackedFilesCount:  len(trackedFiles),
		StaleFiles:         staleFiles,
		PreserveSlideshow:  in.PreserveSlideshow,
		HasChanges:         hasChanges,
		LocalFiles:         in.LocalFiles,
	}
}

func buildUploadJobs(
	policy config.SyncPolicy,
	matteConfig *config.MatteConfig,
	toUploadSet map[string]struct{},
) []UploadJob {
	toUpload := make([]UploadJob, 0, len(toUploadSet))
	for filename := range toUploadSet {
		filePath := filepath.Join(policy.ArtworkDir, filename)
		matte := policy.MatteStyle
		if matteConfig != nil {
			matte = matteConfig.GetMatte(filename, matte)
		}
		toUpload = append(toUpload, UploadJob{
			Filename: filename,
			FilePath: filePath,
			FileType: artwork.FileTypeFromExt(filename),
			Matte:    matte,
		})
	}
	return toUpload
}

func buildDeleteJobs(
	mappingData map[string]string,
	toDeleteSet map[string]struct{},
) (ids []string, files []string) {
	for filename := range toDeleteSet {
		if cid, ok := mappingData[filename]; ok {
			ids = append(ids, cid)
			files = append(files, filename)
		}
	}
	return ids, files
}

func determineSlideshowSettings(cfg *config.Config, logger *slog.Logger) *samsung.SlideshowStatus {
	if !cfg.SlideshowOverride || !cfg.SlideshowEnabled {
		return nil
	}

	ssType := ssTypeShuffle
	if cfg.SlideshowType == "sequential" || cfg.SlideshowType == "order" {
		ssType = ssTypeSequential
	}

	interval := fmt.Sprintf("%d", cfg.SlideshowInterval)

	isValid := false
	supported := []string{"3", "15", "60", "720", "1440", "10080"}
	for _, s := range supported {
		if interval == s {
			isValid = true
			break
		}
	}

	if !isValid {
		logger.Warn("invalid slideshow interval detected for 2024 model, defaulting to 3m shuffle",
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

func determineBrightness(cfg *config.Config, logger *slog.Logger) *int {
	var loc *brightness.SolarLocation
	if cfg.SolarEnabled {
		loc = &brightness.SolarLocation{
			Latitude:  cfg.Latitude,
			Longitude: cfg.Longitude,
			Timezone:  cfg.Timezone,
		}
	}
	return brightness.GetTargetValue(
		loc,
		cfg.BrightnessMin,
		cfg.BrightnessMax,
		cfg.ManualBrightness,
		logger,
	)
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

// isWithinAutoOffWindow returns true if the current time falls within the
// auto-off window: [offTime, offTime + graceHours). Handles midnight wrap.
func isWithinAutoOffWindow(autoOffTime string, graceHours float64, tz string) bool {
	return isWithinAutoOffWindowAt(autoOffTime, graceHours, tz, time.Now())
}

// isWithinAutoOffWindowAt accepts an explicit "now" time.
func isWithinAutoOffWindowAt(autoOffTime string, graceHours float64, tz string, now time.Time) bool {
	if autoOffTime == "" {
		return false
	}

	parts := strings.SplitN(autoOffTime, ":", 2)
	if len(parts) != 2 {
		return false
	}

	offHour, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	offMinute, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false
	}

	now = now.In(loc)
	graceDuration := time.Duration(graceHours * float64(time.Hour))

	// Build today's off time.
	todayOff := time.Date(now.Year(), now.Month(), now.Day(), offHour, offMinute, 0, 0, loc)
	todayGraceEnd := todayOff.Add(graceDuration)

	// Check today's window.
	if !now.Before(todayOff) && now.Before(todayGraceEnd) {
		return true
	}

	// Check yesterday's window (handles midnight wrap).
	yesterdayOff := todayOff.AddDate(0, 0, -1)
	yesterdayGraceEnd := yesterdayOff.Add(graceDuration)
	if !now.Before(yesterdayOff) && now.Before(yesterdayGraceEnd) {
		return true
	}

	return false
}

// formatGraceDisplay returns a human-readable string for the grace period.
func formatGraceDisplay(hours float64) string {
	if hours == float64(int(hours)) {
		return fmt.Sprintf("%d", int(hours))
	}
	return fmt.Sprintf("%.1f", hours)
}

func (s *TVReconciler) logPlan(plan *Plan, policy config.SyncPolicy) {
	if len(plan.ToDeleteUnknownIDs) > 0 {
		if policy.RemoveUnknownImages {
			s.logger.Info("will remove unknown images", "count", len(plan.ToDeleteUnknownIDs))
		} else {
			s.logger.Warn("unknown images on TV (set REMOVE_UNKNOWN_IMAGES=true to remove)",
				"count", len(plan.ToDeleteUnknownIDs))
		}
	}

	s.logger.Info("sync plan",
		"to_upload", len(plan.ToUpload),
		"to_delete", len(plan.ToDeleteIDs),
		"unknown_to_delete", len(plan.ToDeleteUnknownIDs),
	)
}

func (s *TVReconciler) getTVContent(
	ctx context.Context,
	transport TVTransport,
	result *TVSyncResult,
) ([]samsung.ArtContent, error) {
	result.Model = transport.Model()

	if !transport.IsInArtMode(ctx) {
		s.logger.Info("skipping — TV not in art mode")
		result.Status = statusSkippedNotArtMode
		return nil, nil
	}
	result.ArtMode = true

	if err := transport.SaveMetadata(ctx); err != nil {
		s.logger.Debug("could not save metadata", "error", err)
	}

	tvContent, err := transport.ListUploaded(ctx)
	if err != nil {
		return nil, fmt.Errorf("get TV images: %w", err)
	}

	return tvContent, nil
}
