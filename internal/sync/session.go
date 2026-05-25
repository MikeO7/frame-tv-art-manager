package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
	"github.com/MikeO7/frame-tv-art-manager/internal/brightness"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const (
	statusBackoff = "backoff"
	statusError   = "error"
)

// TVTransport is the seam for Samsung TV I/O used during reconciliation.
//
//nolint:interfacebloat // TVTransport requires these Samsung operations
type TVTransport interface {
	ShouldSkip() bool
	Connect(ctx context.Context) error
	Close() error
	RecordFailure(baseInterval time.Duration)
	RecordSuccess()

	ListUploaded(ctx context.Context) ([]samsung.ArtContent, error)
	Upload(ctx context.Context, filePath, fileType, matte string) (string, error)
	DeleteImages(ctx context.Context, ids []string) error

	Model() string
	IsInArtMode(ctx context.Context) bool
	SaveMetadata(ctx context.Context) error
	SelectImage(ctx context.Context, contentID string) error
	SlideshowStatus(ctx context.Context) (*samsung.SlideshowStatus, error)
	SetSlideshow(ctx context.Context, status samsung.SlideshowStatus) error
	SetBrightness(ctx context.Context, val int) error
	TurnOff(ctx context.Context) error
}

// TVSyncResult is the outcome of a single TV reconciliation run.
type TVSyncResult struct {
	IP           string
	Model        string
	Status       string
	ArtMode      bool
	Uploaded     int
	Deleted      int
	TotalImages  int
	Brightness   string
	Slideshow    string
	ErrorMessage string

	NewUploads   map[string]string
	DeletedFiles []string
}

// TVReconciler orchestrates a single synchronization cycle for one TV.
type TVReconciler struct {
	ip          string
	cfg         *config.Config
	logger      *slog.Logger
	mapping     *Mapping
	matteConfig *config.MatteConfig
}

// NewTVReconciler instantiates a new TV synchronization session.
func NewTVReconciler(
	ip string,
	cfg *config.Config,
	matteConfig *config.MatteConfig,
	logger *slog.Logger,
) (*TVReconciler, error) {
	mapping, err := LoadMapping(cfg.TokenDir, ip)
	if err != nil {
		return nil, fmt.Errorf("load mapping: %w", err)
	}

	return &TVReconciler{
		ip:          ip,
		cfg:         cfg,
		logger:      logger.With("tv", ip),
		mapping:     mapping,
		matteConfig: matteConfig,
	}, nil
}

// ArtInventory represents the planned inventory delta between local files and the TV.
type ArtInventory struct {
	TrackedFiles map[string]struct{}
	UnknownIDs   map[string]struct{}
	StaleFiles   []string
	ToUpload     map[string]struct{}
	ToDelete     map[string]struct{}
}

// ReconcileArtInventory compares local art against mapping and TV content.
func ReconcileArtInventory(
	localFiles map[string]struct{},
	mapping map[string]string,
	tvContent []samsung.ArtContent,
	logger *slog.Logger,
) *ArtInventory {
	trackedFiles, unknownIDs, staleFiles := reconcileInventory(mapping, tvContent, logger)
	toUpload := diffSets(localFiles, trackedFiles)
	toDelete := diffSets(trackedFiles, localFiles)
	return &ArtInventory{
		TrackedFiles: trackedFiles,
		UnknownIDs:   unknownIDs,
		StaleFiles:   staleFiles,
		ToUpload:     toUpload,
		ToDelete:     toDelete,
	}
}

// Reconcile executes the synchronization session against the TV transport.
//
//nolint:funlen // complexity justified for this domain-specific path
func (s *TVReconciler) Reconcile(
	ctx context.Context,
	transport TVTransport,
	localFiles map[string]struct{},
) (TVSyncResult, error) {
	result := TVSyncResult{
		IP:         s.ip,
		NewUploads: make(map[string]string),
	}

	policy := s.cfg.SyncPolicy()

	if transport.ShouldSkip() {
		result.Status = statusBackoff
		return result, nil
	}

	// Connect to the TV.
	if err := s.connectTV(ctx, transport, policy, &result); err != nil || result.Status != "" {
		return result, err
	}
	defer func() { _ = transport.Close() }()

	// Fetch uploaded images from TV.
	tvContent, err := s.getTVContent(ctx, transport, &result)
	if err != nil || result.Status != "" {
		if err != nil {
			transport.RecordFailure(time.Duration(policy.SyncIntervalMin) * time.Minute)
			result.Status = "error"
		}
		return result, err
	}

	// Inventory reconciliation.
	inv := ReconcileArtInventory(localFiles, s.mapping.AllContentIDs(), tvContent, s.logger)
	result.DeletedFiles = append(result.DeletedFiles, inv.StaleFiles...)
	s.logger.Info("TV inventory", "tracked", len(inv.TrackedFiles), "unknown", len(inv.UnknownIDs))

	s.logSyncPlan(inv.ToUpload, inv.ToDelete, inv.UnknownIDs, policy)

	hasChanges := len(inv.ToUpload) > 0 || len(inv.ToDelete) > 0 || (policy.RemoveUnknownImages && len(inv.UnknownIDs) > 0)
	var preserveSlideshow *samsung.SlideshowStatus
	if hasChanges && !policy.SlideshowOverride {
		preserveSlideshow, _ = transport.SlideshowStatus(ctx)
	}

	state := &sessionState{
		ctx:               ctx,
		transport:         transport,
		policy:            policy,
		result:            &result,
		inventory:         inv,
		preserveSlideshow: preserveSlideshow,
		hasChanges:        hasChanges,
		localFiles:        localFiles,
	}

	// Execute changes.
	s.processUploads(state)
	s.processDeletions(state)
	s.applySettings(state)

	result.TotalImages = len(inv.TrackedFiles) + result.Uploaded - result.Deleted
	result.Status = "ok"

	transport.RecordSuccess()
	s.logger.Info("sync completed")
	return result, nil
}

type sessionState struct {
	ctx               context.Context
	transport         TVTransport
	policy            config.SyncPolicy
	result            *TVSyncResult
	inventory         *ArtInventory
	preserveSlideshow *samsung.SlideshowStatus
	hasChanges        bool
	localFiles        map[string]struct{}
}

func (s *TVReconciler) connectTV(
	ctx context.Context,
	transport TVTransport,
	policy config.SyncPolicy,
	result *TVSyncResult,
) error {
	if err := transport.Connect(ctx); err != nil {
		if errors.Is(err, samsung.ErrGateFailed) {
			s.logger.Info("skipping — REST gate says TV is busy")
			result.Status = "skipped (gate)"
			return nil
		}
		transport.RecordFailure(time.Duration(policy.SyncIntervalMin) * time.Minute)
		result.Status = "error"
		return fmt.Errorf("connect: %w", err)
	}
	return nil
}

func (s *TVReconciler) getTVContent(
	ctx context.Context,
	transport TVTransport,
	result *TVSyncResult,
) ([]samsung.ArtContent, error) {
	result.Model = transport.Model()

	if !transport.IsInArtMode(ctx) {
		s.logger.Info("skipping — TV not in art mode")
		result.Status = "skipped (not art mode)"
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

func (s *TVReconciler) logSyncPlan(
	toUpload, toDelete, unknownIDs map[string]struct{},
	policy config.SyncPolicy,
) {
	if len(unknownIDs) > 0 {
		if policy.RemoveUnknownImages {
			s.logger.Info("will remove unknown images", "count", len(unknownIDs))
		} else {
			s.logger.Warn("unknown images on TV (set REMOVE_UNKNOWN_IMAGES=true to remove)",
				"count", len(unknownIDs))
		}
	}

	s.logger.Info("sync plan",
		"to_upload", len(toUpload),
		"to_delete", len(toDelete),
		"unknown_to_delete", boolCount(policy.RemoveUnknownImages, len(unknownIDs)),
	)
}

func (s *TVReconciler) processUploads(state *sessionState) {
	uploadsDone := 0
	for filename := range state.inventory.ToUpload {
		if uploadsDone > 0 && state.policy.UploadDelay > 0 && !state.policy.DryRun {
			time.Sleep(state.policy.UploadDelay)
		}

		if state.policy.DryRun {
			s.logger.Info("[DRY RUN] would upload", "file", filename)
			state.result.Uploaded++
			continue
		}

		filePath := filepath.Join(state.policy.ArtworkDir, filename)
		fileType := artwork.FileTypeFromExt(filename)
		matte := state.policy.MatteStyle
		matte = s.matteConfig.GetMatte(filename, matte)

		contentID, uploadErr := s.uploadWithRetry(state, filePath, fileType, matte)
		if uploadErr != nil {
			s.logger.Error("upload failed", "file", filename, "error", uploadErr)
			continue
		}

		if !state.policy.DryRun {
			s.mapping.Set(filename, contentID)
		}
		state.result.NewUploads[filename] = contentID
		s.logger.Info("uploaded", "file", filename, "content_id", contentID, "matte", matte)
		state.result.Uploaded++
		uploadsDone++
	}
}

func (s *TVReconciler) uploadWithRetry(
	state *sessionState,
	filePath, fileType, matte string,
) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= state.policy.UploadAttempts; attempt++ {
		contentID, err := state.transport.Upload(state.ctx, filePath, fileType, matte)
		if err == nil {
			return contentID, nil
		}
		lastErr = err
		if attempt < state.policy.UploadAttempts {
			s.logger.Warn("upload retry", "attempt", attempt, "error", err)
			time.Sleep(state.policy.UploadDelay * 2)
		}
	}
	return "", lastErr
}

func (s *TVReconciler) processDeletions(state *sessionState) {
	s.deleteTrackedImages(state)
	s.deleteUnknownImages(state)
}

func (s *TVReconciler) deleteTrackedImages(state *sessionState) {
	if len(state.inventory.ToDelete) == 0 {
		return
	}
	var idsToDelete []string
	var filesToDelete []string
	for filename := range state.inventory.ToDelete {
		if cid, ok := s.mapping.GetContentID(filename); ok {
			idsToDelete = append(idsToDelete, cid)
			filesToDelete = append(filesToDelete, filename)
		}
	}

	if len(idsToDelete) == 0 {
		return
	}
	if state.policy.DryRun {
		s.logger.Info("[DRY RUN] would delete tracked images", "count", len(idsToDelete))
		state.result.Deleted = len(idsToDelete)
		return
	}

	s.logger.Info("deleting tracked images", "count", len(idsToDelete))
	if err := state.transport.DeleteImages(state.ctx, idsToDelete); err != nil {
		s.logger.Error("batch delete failed", "error", err)
		state.result.Deleted = len(idsToDelete)
		return
	}

	state.result.DeletedFiles = append(state.result.DeletedFiles, filesToDelete...)
	if !state.policy.DryRun {
		s.mapping.DeleteBatch(filesToDelete)
	}
	s.logger.Info("deleted tracked images", "count", len(idsToDelete))
	state.result.Deleted = len(idsToDelete)
}

func (s *TVReconciler) deleteUnknownImages(state *sessionState) {
	if !state.policy.RemoveUnknownImages || len(state.inventory.UnknownIDs) == 0 {
		return
	}
	ids := setToSlice(state.inventory.UnknownIDs)
	if state.policy.DryRun {
		s.logger.Info("[DRY RUN] would delete unknown images", "count", len(ids))
	} else {
		s.logger.Info("deleting unknown images", "count", len(ids))
		if err := state.transport.DeleteImages(state.ctx, ids); err != nil {
			s.logger.Error("delete unknown images failed", "error", err)
		}
	}
}

func (s *TVReconciler) applySettings(state *sessionState) {
	finalMapping := s.mapping.AllContentIDs()
	s.applySelectionAndSlideshow(state, finalMapping)

	s.updateSlideshow(state)
	s.updateBrightness(state)
	s.handleAutoOff(state)
}

//nolint:gocyclo // complexity justified for this domain-specific path
func (s *TVReconciler) applySelectionAndSlideshow(
	state *sessionState,
	finalMapping map[string]string,
) {
	if !state.hasChanges || len(state.localFiles) == 0 || len(finalMapping) == 0 {
		return
	}

	var selectedID string
	slideshow := determineSlideshowSettings(s.cfg, s.logger)
	settingsForMode := slideshow
	if settingsForMode == nil {
		settingsForMode = state.preserveSlideshow
	}

	if settingsForMode != nil && settingsForMode.Type == "shuffleslideshow" {
		values := mapValues(finalMapping)
		//nolint:gosec // weak random number generator is fine for selecting artwork shuffle order
		selectedID = values[rand.IntN(len(values))]
		s.logger.Info("selecting random image for shuffle mode")
	} else {
		for _, id := range finalMapping {
			selectedID = id
			break
		}
		s.logger.Info("selecting first image")
	}

	if selectedID != "" && !state.policy.DryRun {
		if err := state.transport.SelectImage(state.ctx, selectedID); err != nil {
			s.logger.Warn("failed to select image", "error", err)
		}
	}

	if state.preserveSlideshow != nil && !state.policy.DryRun {
		if err := state.transport.SetSlideshow(state.ctx, *state.preserveSlideshow); err != nil {
			s.logger.Warn("failed to restore slideshow", "error", err)
		}
	}
}

func (s *TVReconciler) updateSlideshow(state *sessionState) {
	slideshow := determineSlideshowSettings(s.cfg, s.logger)
	if slideshow == nil || state.policy.DryRun {
		return
	}
	current, _ := state.transport.SlideshowStatus(state.ctx)
	needsUpdate := current == nil ||
		current.Value != slideshow.Value ||
		current.Type != slideshow.Type

	if needsUpdate {
		s.logger.Info("updating slideshow settings",
			"interval", slideshow.Value,
			"type", slideshow.Type,
		)
		if err := state.transport.SetSlideshow(state.ctx, *slideshow); err != nil {
			s.logger.Warn("failed to set slideshow", "error", err)
		}
	}
	state.result.Slideshow = fmt.Sprintf("%s every %s min", slideshow.Type, slideshow.Value)
}

func (s *TVReconciler) updateBrightness(state *sessionState) {
	brightnessVal := determineBrightness(s.cfg, s.logger)
	if brightnessVal == nil || state.policy.DryRun {
		return
	}
	if err := state.transport.SetBrightness(state.ctx, *brightnessVal); err != nil {
		s.logger.Warn("failed to set brightness", "error", err)
	}
	state.result.Brightness = fmt.Sprintf("%d", *brightnessVal)
}

func (s *TVReconciler) handleAutoOff(state *sessionState) {
	trigger := isWithinAutoOffWindow(s.cfg.AutoOffTime, s.cfg.AutoOffGraceHours, s.cfg.Timezone)
	if !trigger {
		return
	}
	s.logger.Info("within auto-off window, turning off TV")
	if !state.policy.DryRun {
		if err := state.transport.TurnOff(state.ctx); err != nil {
			s.logger.Warn("failed to turn off TV", "error", err)
		} else {
			s.logger.Info("TV turned off")
		}
	}
}

const (
	ssTypeShuffle    = "shuffleslideshow"
	ssTypeSequential = "slideshow"
)

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

func boolCount(cond bool, count int) int {
	if cond {
		return count
	}
	return 0
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
