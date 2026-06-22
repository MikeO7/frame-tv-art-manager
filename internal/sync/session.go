package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

const (
	statusBackoff           = "backoff"
	statusError             = "error"
	statusSkippedNotArtMode = "skipped (not art mode)"
)

const (
	ssTypeShuffle    = "shuffleslideshow"
	ssTypeSequential = "slideshow"
)

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
	StorageFull  bool

	NewUploads   map[string]string
	DeletedFiles []string
}

// UploadJob describes a single file to upload and its target matte style.
type UploadJob struct {
	Filename string
	FilePath string
	FileType string
	Matte    string
}

// SyncPlan holds the pure decisions of a synchronization cycle.
type SyncPlan struct {
	IP                 string
	ToUpload           []UploadJob
	ToDeleteIDs        []string
	ToDeleteFiles      []string
	ToDeleteUnknownIDs []string
	Slideshow          *samsung.SlideshowStatus
	Brightness         *int
	TurnOff            bool

	// Stats & Metadata
	TrackedFilesCount int
	StaleFiles        []string

	// Preservation states
	PreserveSlideshow *samsung.SlideshowStatus
	HasChanges        bool
	LocalFiles        map[string]struct{}
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

// Reconcile executes the synchronization session against the TV transport.
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

	if err := s.connectTV(ctx, transport, policy, &result); err != nil || result.Status != "" {
		return result, err
	}
	defer func() { _ = transport.Close() }()

	tvContent, err := s.getTVContent(ctx, transport, &result)
	if err != nil || result.Status != "" {
		if err != nil {
			transport.RecordFailure(time.Duration(policy.SyncIntervalMin) * time.Minute)
			result.Status = statusError
		}
		return result, err
	}

	localFiles, capacityMgr := s.applyCapacityFilter(localFiles)

	plan := s.planSyncCycle(ctx, transport, policy, tvContent, localFiles)

	execResult, err := s.ExecuteSyncPlan(ctx, plan, transport, s.mapping, policy)
	if err != nil {
		transport.RecordFailure(time.Duration(policy.SyncIntervalMin) * time.Minute)
		execResult.Status = statusError
		return execResult, err
	}

	// Handle capacity detection
	s.handleCapacityError(&execResult, plan, capacityMgr)

	transport.RecordSuccess()
	if _, capErr := capacityMgr.RecordSuccess(); capErr != nil {
		s.logger.Warn("failed to update capacity success streak", "error", capErr)
	}
	s.logger.Info("sync completed")
	return execResult, nil
}

func (s *TVReconciler) planSyncCycle(
	ctx context.Context,
	transport TVTransport,
	policy config.SyncPolicy,
	tvContent []samsung.ArtContent,
	localFiles map[string]struct{},
) *SyncPlan {
	var preserveSlideshow *samsung.SlideshowStatus
	if !policy.SlideshowOverride {
		preserveSlideshow, _ = transport.SlideshowStatus(ctx)
	}

	plan := PlanSync(PlanInput{
		IP:                s.ip,
		Cfg:               s.cfg,
		MatteConfig:       s.matteConfig,
		MappingData:       s.mapping.AllContentIDs(),
		TVContent:         tvContent,
		LocalFiles:        localFiles,
		PreserveSlideshow: preserveSlideshow,
		Logger:            s.logger,
	})

	s.logPlan(plan, policy)
	return plan
}

func (s *TVReconciler) applyCapacityFilter(localFiles map[string]struct{}) (map[string]struct{}, *CapacityManager) {
	capacityMgr := NewCapacityManager(s.cfg.TokenDir, s.ip)
	capState, capErr := capacityMgr.Load()
	if capErr != nil {
		s.logger.Warn("could not load capacity state", "error", capErr)
	}

	if capState != nil && capState.IsFull {
		s.logger.Info("TV is full, filtering local sync collection", "limit", capState.MaxImages)
		localFiles = FilterLocalFiles(localFiles, capState.MaxImages)
	}
	return localFiles, capacityMgr
}

func (s *TVReconciler) handleCapacityError(execResult *TVSyncResult, plan *SyncPlan, capacityMgr *CapacityManager) {
	if !execResult.StorageFull {
		return
	}
	currentOnTV := plan.TrackedFilesCount - execResult.Deleted + execResult.Uploaded
	s.logger.Warn("sync stopped early due to upload failure (storage full); updating capacity limit",
		"current_images_on_tv", currentOnTV,
		"error", execResult.ErrorMessage)

	capState := &CapacityState{
		MaxImages:     currentOnTV,
		IsFull:        true,
		SuccessStreak: 0,
	}
	if saveErr := capacityMgr.Save(capState); saveErr != nil {
		s.logger.Error("failed to save capacity state", "error", saveErr)
	}
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
		result.Status = statusError
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

func (s *TVReconciler) logPlan(plan *SyncPlan, policy config.SyncPolicy) {
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
