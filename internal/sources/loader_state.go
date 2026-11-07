package sources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

const (
	// sourceStateName is the control file holding provider resolutions and
	// attribution for downloaded artwork.
	sourceStateName     = "source-state.json"
	sourceStateVersion  = 1
	maxSourceStateBytes = 32 << 20

	// sourceResolutionTTL is how long a provider resolution is reused. Pixabay
	// requires API results to be cached for 24 hours, and the other providers'
	// searches and collections change slowly.
	sourceResolutionTTL = 24 * time.Hour
	// apodRefreshHourUTC is safely after APOD publishes a new picture at
	// midnight US Eastern time.
	apodRefreshHourUTC = 6
	// commonsPOTDRefreshHourUTC follows the Commons Picture of the Day, which
	// changes at midnight UTC.
	commonsPOTDRefreshHourUTC = 0
	// minFailureBackoff is the first retry delay after a failed resolution; it
	// doubles with each consecutive failure up to sourceResolutionTTL.
	minFailureBackoff   = 15 * time.Minute
	maxBackoffDoublings = 7

	// sourceDownloadPattern names private temporary downloads.
	sourceDownloadPattern = ".frame-tv-source-download-*.tmp"
	// staleDownloadAge is far beyond the download timeout, so an older
	// temporary download can only be left over from an interrupted process.
	staleDownloadAge = time.Hour
)

// sourceState is the durable per-source resolution cache plus the attribution
// of every source artwork still in the collection, keyed by Source Origin.
type sourceState struct {
	Version      int                         `json:"version"`
	Resolutions  map[string]cachedResolution `json:"resolutions"`
	Attributions map[string]Attribution      `json:"attributions"`
}

// cachedResolution is the last-known-good resolution of one source line and
// its retry schedule.
type cachedResolution struct {
	Provider    string        `json:"provider"`
	Resolved    bool          `json:"resolved"`
	ResolvedAt  time.Time     `json:"resolved_at"`
	ExpiresAt   time.Time     `json:"expires_at"`
	NextAttempt time.Time     `json:"next_attempt"`
	Failures    int           `json:"failures,omitempty"`
	Images      []SourceImage `json:"images"`
}

func newSourceState() *sourceState {
	return &sourceState{
		Version:      sourceStateVersion,
		Resolutions:  make(map[string]cachedResolution),
		Attributions: make(map[string]Attribution),
	}
}

func sourceStatePath(artworkDir string) string {
	if artworkDir == "" {
		return ""
	}
	return collection.ControlPath(artworkDir, sourceStateName)
}

// ReadAttributions returns the recorded credits for source artwork in the
// collection rooted at artworkDir, ordered by Source Origin.
func ReadAttributions(ctx context.Context, artworkDir string) ([]Attribution, error) {
	state, err := readSourceState(ctx, sourceStatePath(artworkDir))
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(state.Attributions))
	for key := range state.Attributions {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	attributions := make([]Attribution, 0, len(keys))
	for _, key := range keys {
		attributions = append(attributions, state.Attributions[key])
	}
	return attributions, nil
}

// readSourceState loads the control file. A missing file is an empty state,
// and an undecodable one is reported so the caller can start afresh; an
// unsafe file (symlink, wrong mode, oversized) is an error.
func readSourceState(ctx context.Context, path string) (*sourceState, error) {
	if path == "" {
		return newSourceState(), nil
	}
	data, err := durablefs.ReadStable(ctx, path, durablefs.StableReadOptions{MaxBytes: maxSourceStateBytes, RequiredMode: 0o600})
	if errors.Is(err, os.ErrNotExist) {
		return newSourceState(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read source state: %w", err)
	}
	state := newSourceState()
	if err := json.Unmarshal(data, state); err != nil {
		return newSourceState(), fmt.Errorf("%w: %w", errSourceStateUnreadable, err)
	}
	if state.Version != sourceStateVersion {
		return newSourceState(), fmt.Errorf("%w: unsupported version %d", errSourceStateUnreadable, state.Version)
	}
	if state.Resolutions == nil {
		state.Resolutions = make(map[string]cachedResolution)
	}
	if state.Attributions == nil {
		state.Attributions = make(map[string]Attribution)
	}
	return state, nil
}

var errSourceStateUnreadable = errors.New("source state is unreadable")

// ensureState loads the durable state once per process and removes
// temporary downloads abandoned by an interrupted earlier run.
func (l *Loader) ensureState(ctx context.Context) error {
	if l.state != nil {
		return nil
	}
	l.removeStaleDownloads()
	state, err := readSourceState(ctx, l.statePath)
	if errors.Is(err, errSourceStateUnreadable) {
		l.logger.Warn("discarding unreadable source cache; providers will be queried again", "error", err)
		l.state, l.stateDirty = state, true
		return nil
	}
	if err != nil {
		return err
	}
	l.state = state
	return nil
}

func (l *Loader) removeStaleDownloads() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), sourceDownloadPattern))
	if err != nil {
		l.logger.Warn("list stale source downloads failed", "error", err)
		return
	}
	removed := 0
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil || !info.Mode().IsRegular() || l.now().Sub(info.ModTime()) < staleDownloadAge {
			continue
		}
		if err := os.Remove(match); err != nil {
			l.logger.Warn("remove stale source download failed", "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		l.logger.Info("removed stale source downloads", "count", removed)
	}
}

// resolve returns a source line's images, reusing a fresh cached resolution
// and keeping the last-known-good resolution when the provider fails or is
// backing off. Lines that need no network (direct URLs) are never cached.
func (l *Loader) resolve(ctx context.Context, provider SourceProvider, line string) ([]SourceImage, error) {
	now := l.now()
	expiry := resolutionExpiry(provider.Name(), line, now)
	if expiry.IsZero() || l.state == nil {
		return provider.Resolve(ctx, line)
	}
	key := sourceLineKey(line)
	entry, cached := l.state.Resolutions[key]
	if cached && entry.Resolved && now.Before(entry.ExpiresAt) {
		return entry.Images, nil
	}
	if cached && now.Before(entry.NextAttempt) {
		if entry.Resolved {
			return entry.Images, nil
		}
		return nil, fmt.Errorf("%s source is backing off after an earlier failure until %s",
			provider.Name(), entry.NextAttempt.UTC().Format(time.RFC3339))
	}

	images, err := provider.Resolve(ctx, line)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		entry.Provider = provider.Name()
		entry.Failures++
		entry.NextAttempt = failureRetryAt(err, now, entry.Failures)
		l.state.Resolutions[key] = entry
		l.stateDirty = true
		if !entry.Resolved {
			return nil, err
		}
		l.logger.Warn("provider resolution failed; using last-known-good resolution",
			"provider", provider.Name(), "resolved_at", entry.ResolvedAt, "next_attempt", entry.NextAttempt, "error", err)
		return entry.Images, nil
	}
	l.state.Resolutions[key] = cachedResolution{
		Provider: provider.Name(), Resolved: true, ResolvedAt: now, ExpiresAt: expiry, Images: images,
	}
	l.stateDirty = true
	return images, nil
}

// resolutionExpiry returns when a resolution made at now goes stale, or the
// zero time for sources that are resolved locally.
func resolutionExpiry(provider, line string, now time.Time) time.Time {
	if provider == directProviderName {
		return time.Time{}
	}
	if provider == providerNASA && strings.HasPrefix(line, "nasa:"+apodCommand) {
		return nextDailyRefresh(now, apodRefreshHourUTC)
	}
	if provider == providerCommons && strings.HasPrefix(line, providerCommons+":"+commonsPOTDCommand) {
		return nextDailyRefresh(now, commonsPOTDRefreshHourUTC)
	}
	return now.Add(sourceResolutionTTL)
}

// nextDailyRefresh returns the first time after now at hourUTC o'clock UTC.
func nextDailyRefresh(now time.Time, hourUTC int) time.Time {
	utc := now.UTC()
	refresh := time.Date(utc.Year(), utc.Month(), utc.Day(), hourUTC, 0, 0, 0, time.UTC)
	if !refresh.After(utc) {
		refresh = refresh.AddDate(0, 0, 1)
	}
	return refresh
}

// failureRetryAt schedules the next attempt after consecutive failures,
// honoring a provider's rate-limit reset when it is later.
func failureRetryAt(err error, now time.Time, failures int) time.Time {
	backoff := min(minFailureBackoff<<min(max(failures-1, 0), maxBackoffDoublings), sourceResolutionTTL)
	retryAt := now.Add(backoff)
	var limited *rateLimitError
	if errors.As(err, &limited) && limited.RetryAt.After(retryAt) {
		retryAt = limited.RetryAt
	}
	return retryAt
}

func sourceLineKey(line string) string {
	digest := sha256.Sum256([]byte(line))
	return hex.EncodeToString(digest[:])
}

func (l *Loader) recordAttribution(originKey string, attribution Attribution) {
	if l.state == nil || attribution.Provider == "" {
		return
	}
	if existing, ok := l.state.Attributions[originKey]; ok && existing == attribution {
		return
	}
	l.state.Attributions[originKey] = attribution
	l.stateDirty = true
}

// persistState drops resolutions of lines no longer configured and credits
// for artwork no longer in the collection, then publishes the control file
// transactionally when anything changed.
func (l *Loader) persistState(ctx context.Context, lines []string) error {
	if l.state == nil || l.statePath == "" {
		return nil
	}
	configured := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		configured[sourceLineKey(line)] = struct{}{}
	}
	for key := range l.state.Resolutions {
		if _, ok := configured[key]; !ok {
			delete(l.state.Resolutions, key)
			l.stateDirty = true
		}
	}
	for key := range l.state.Attributions {
		if _, ok := l.sourceOrigins[key]; !ok {
			delete(l.state.Attributions, key)
			l.stateDirty = true
		}
	}
	if !l.stateDirty {
		return nil
	}
	if err := ensureControlDirectory(filepath.Dir(l.statePath)); err != nil {
		return fmt.Errorf("persist source state: %w", err)
	}
	if err := durablefs.Replace(ctx, l.statePath, 0o600, func(writer io.Writer) error {
		return json.NewEncoder(writer).Encode(l.state)
	}); err != nil {
		return fmt.Errorf("persist source state: %w", err)
	}
	l.stateDirty = false
	return nil
}

func ensureControlDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("create control directory: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect control directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("control directory must be a non-symlink directory")
	}
	return nil
}
