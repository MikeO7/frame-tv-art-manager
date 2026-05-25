package sync

import (
	"fmt"
	"log/slog"
	"math/rand/v2"
	"path/filepath"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/brightness"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// PlanSync evaluates current states and plans synchronization purely in memory.
//
//nolint:gocognit,nestif,funlen,gocyclo,revive // justified complexity and argument count for pure planning
func PlanSync(
	ip string,
	cfg *config.Config,
	matteConfig *config.MatteConfig,
	mappingData map[string]string,
	tvContent []samsung.ArtContent,
	localFiles map[string]struct{},
	preserveSlideshow *samsung.SlideshowStatus,
	logger *slog.Logger,
) *SyncPlan {
	policy := cfg.SyncPolicy()

	trackedFiles, unknownIDs, staleFiles := reconcileInventory(mappingData, tvContent, logger)
	toUploadSet := diffSets(localFiles, trackedFiles)
	toDeleteSet := diffSets(trackedFiles, localFiles)

	toUpload := make([]UploadJob, 0, len(toUploadSet))
	for filename := range toUploadSet {
		filePath := filepath.Join(policy.ArtworkDir, filename)
		fileType := artwork.FileTypeFromExt(filename)
		matte := policy.MatteStyle
		if matteConfig != nil {
			matte = matteConfig.GetMatte(filename, matte)
		}
		toUpload = append(toUpload, UploadJob{
			Filename: filename,
			FilePath: filePath,
			FileType: fileType,
			Matte:    matte,
		})
	}

	var toDeleteIDs []string
	var toDeleteFiles []string
	for filename := range toDeleteSet {
		if cid, ok := mappingData[filename]; ok {
			toDeleteIDs = append(toDeleteIDs, cid)
			toDeleteFiles = append(toDeleteFiles, filename)
		}
	}

	var toDeleteUnknownIDs []string
	if policy.RemoveUnknownImages && len(unknownIDs) > 0 {
		toDeleteUnknownIDs = setToSlice(unknownIDs)
	}

	hasChanges := len(toUpload) > 0 || len(toDeleteIDs) > 0 || len(toDeleteUnknownIDs) > 0

	var selectedID string
	slideshow := determineSlideshowSettings(cfg, logger)
	settingsForMode := slideshow
	if settingsForMode == nil {
		settingsForMode = preserveSlideshow
	}

	finalMapping := make(map[string]string)
	for k, v := range mappingData {
		finalMapping[k] = v
	}
	for i, u := range toUpload {
		finalMapping[u.Filename] = fmt.Sprintf("mock-id-%d", i)
	}
	for _, f := range toDeleteFiles {
		delete(finalMapping, f)
	}

	if hasChanges && len(localFiles) > 0 && len(finalMapping) > 0 {
		if settingsForMode != nil && settingsForMode.Type == "shuffleslideshow" {
			values := mapValues(finalMapping)
			if len(values) > 0 {
				//nolint:gosec // weak random number generator is fine for selecting artwork shuffle order
				selectedID = values[rand.IntN(len(values))]
			}
		} else {
			for _, id := range finalMapping {
				selectedID = id
				break
			}
		}
	}

	var targetSlideshow *samsung.SlideshowStatus
	if slideshow != nil {
		targetSlideshow = slideshow
	}

	brightnessVal := determineBrightness(cfg, logger)
	turnOff := isWithinAutoOffWindow(cfg.AutoOffTime, cfg.AutoOffGraceHours, cfg.Timezone)

	return &SyncPlan{
		IP:                 ip,
		ToUpload:           toUpload,
		ToDeleteIDs:        toDeleteIDs,
		ToDeleteFiles:      toDeleteFiles,
		ToDeleteUnknownIDs: toDeleteUnknownIDs,
		SelectedID:         selectedID,
		Slideshow:          targetSlideshow,
		Brightness:         brightnessVal,
		TurnOff:            turnOff,
		TrackedFilesCount:  len(trackedFiles),
		StaleFiles:         staleFiles,
		PreserveSlideshow:  preserveSlideshow,
		HasChanges:         hasChanges,
		LocalFiles:         localFiles,
	}
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
