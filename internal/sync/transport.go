package sync

import (
	"context"
	"time"

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
