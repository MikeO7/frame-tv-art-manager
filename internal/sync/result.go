package sync

const (
	statusBackoff           = "backoff"
	statusError             = "error"
	statusSkippedNotArtMode = "skipped (not art mode)"
)

// TVSyncResult is the operator-facing projection of one TV reconciliation.
// Durable mutation and recovery details remain owned by package reconcile.
type TVSyncResult struct {
	IP               string
	Model            string
	Status           string
	ArtMode          bool
	Uploaded         int
	Deleted          int
	TotalImages      int
	Brightness       string
	Slideshow        string
	ErrorMessage     string
	StorageFull      bool
	StorageKnown     bool
	FreeSpaceBytes   int64
	FreeSpacePercent float64
}
