package sync

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/samsung"
)

// tvConnection manages the lifecycle and health tracking of a TV connection.
type tvConnection interface {
	ShouldSkip() bool
	Connect(ctx context.Context) error
	Close() error
	RecordFailure(baseInterval time.Duration)
	RecordSuccess()
}

// tvArtStore manages the artwork content stored on the TV.
type tvArtStore interface {
	ListUploaded(ctx context.Context) ([]samsung.ArtContent, error)
	Upload(ctx context.Context, filePath, fileType, matte string) (string, error)
	DeleteImages(ctx context.Context, ids []string) error
}

// tvState exposes read-only device identity, mode, and metadata persistence.
type tvState interface {
	Model() string
	IsInArtMode(ctx context.Context) bool
	SaveMetadata(ctx context.Context) error
}

// tvDisplay controls how the TV presents artwork (selection, slideshow, brightness, power).
type tvDisplay interface {
	SelectImage(ctx context.Context, contentID string) error
	SlideshowStatus(ctx context.Context) (*samsung.SlideshowStatus, error)
	SetSlideshow(ctx context.Context, status samsung.SlideshowStatus) error
	SetBrightness(ctx context.Context, val int) error
	TurnOff(ctx context.Context) error
}

// TVTransport is the seam for Samsung TV I/O used during reconciliation,
// composed from the connection, art-store, state, and display role interfaces.
type TVTransport interface {
	tvConnection
	tvArtStore
	tvState
	tvDisplay
}

var _ TVTransport = (*samsung.Client)(nil)

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

// Plan holds the pure decisions of a synchronization cycle.
type Plan struct {
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

	execResult, err := s.ExecutePlan(ctx, plan, transport, s.mapping, policy)
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
) *Plan {
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

	logPlan(plan, policy, s.logger)
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

func (s *TVReconciler) handleCapacityError(execResult *TVSyncResult, plan *Plan, capacityMgr *CapacityManager) {
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
