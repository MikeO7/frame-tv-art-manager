package sync

import "time"

const (
	statusBackoff           = "backoff"
	statusError             = "error"
	statusSkippedNotArtMode = "skipped (not art mode)"
)

// TVSyncResult is the operator-facing projection of one TV reconciliation.
// Durable mutation and recovery details remain owned by package reconcile.
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
	// Rotating reports that rotation manages a collection larger than the TV.
	Rotating bool
	// ArtworkWaiting counts collection artwork not currently on the TV.
	ArtworkWaiting int
	// NextRotation is the earliest time another swap can become due.
	NextRotation time.Time
}
