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
	SelectedID         string
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

	var preserveSlideshow *samsung.SlideshowStatus
	if !policy.SlideshowOverride {
		preserveSlideshow, _ = transport.SlideshowStatus(ctx)
	}

	plan := PlanSync(
		s.ip,
		s.cfg,
		s.matteConfig,
		s.mapping.AllContentIDs(),
		tvContent,
		localFiles,
		preserveSlideshow,
		s.logger,
	)

	s.logPlan(plan, policy)

	execResult, err := s.ExecuteSyncPlan(ctx, plan, transport, s.mapping, policy)
	if err != nil {
		transport.RecordFailure(time.Duration(policy.SyncIntervalMin) * time.Minute)
		execResult.Status = statusError
		return execResult, err
	}

	transport.RecordSuccess()
	s.logger.Info("sync completed")
	return execResult, nil
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
