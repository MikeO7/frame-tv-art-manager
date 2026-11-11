package sources

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	neturl "net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	providerNASA     = "nasa"
	providerPexels   = "pexels"
	providerPixabay  = "pixabay"
	mediaTypeImage   = "image"
	slugDirectSource = "direct-source"

	// maxSlugLen bounds derived filename slugs to keep paths and the TV's art
	// API comfortably within length limits.
	maxSlugLen = 100

	// targetArtworkWidth and targetArtworkHeight are the Frame TV panel size
	// that provider renditions are requested to cover.
	targetArtworkWidth  = 3840
	targetArtworkHeight = 2160
	// maxDownloadedPixels bounds decoded source downloads so a hostile or
	// oversized image cannot exhaust memory during validation.
	maxDownloadedPixels int64 = 40_000_000

	axisWidth  = "w"
	axisHeight = "h"
)

// capSlug truncates a slug to maxSlugLen runes' worth of bytes (slugs are ASCII).
func capSlug(slug string) string {
	if len(slug) > maxSlugLen {
		return slug[:maxSlugLen]
	}
	return slug
}

// sourceImage represents resolved metadata for a downloadable image source.
type sourceImage struct {
	// URL is the preferred download. Fallbacks are tried in order when it
	// cannot be downloaded or fails validation, so a provider can offer a
	// smaller rendition when the largest one exceeds the configured caps.
	URL       string   `json:"url"`
	Fallbacks []string `json:"fallbacks,omitempty"`
	// Identity is the stable provider-owned key. It never depends on the
	// rendition chosen for download.
	Identity string `json:"identity"`
	// TrackURL is a provider download-tracking endpoint that is called once
	// the artwork has been imported.
	TrackURL    string      `json:"track_url,omitempty"`
	Attribution attribution `json:"attribution"`
}

// attribution is the credit that a provider's terms require for its artwork.
type attribution struct {
	Provider  string `json:"provider"`
	Title     string `json:"title,omitempty"`
	Artist    string `json:"artist,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
	License   string `json:"license,omitempty"`
}

// sourceProvider defines the seam for translating configuration lines into raw downloadable metadata.
type sourceProvider interface {
	Name() string
	CanHandle(line string) bool
	Resolve(ctx context.Context, line string) ([]sourceImage, error)
}

// downloadTracker is implemented by providers whose terms require reporting
// each download of a resolved image.
type downloadTracker interface {
	TrackDownload(ctx context.Context, trackURL string) error
}

// sourceConfigError marks a failure the operator has to correct: a missing API
// key or a source line that cannot be parsed. Asking the provider again later
// cannot fix it, so it is never scheduled for a retry; it is reported every
// cycle until the configuration changes. Every other resolution failure is
// treated as a provider failure and backed off.
type sourceConfigError struct {
	message string
}

func (e *sourceConfigError) Error() string { return e.message }

func configError(format string, args ...any) error {
	return &sourceConfigError{message: fmt.Sprintf(format, args...)}
}

func isSourceConfigError(err error) bool {
	var configuration *sourceConfigError
	return errors.As(err, &configuration)
}

// sourceIdentity returns an order-independent provider identity. stableKey is
// hashed so signed URLs and other sensitive source material never enter the
// manifest or filename, while label keeps generated artwork recognizable.
func sourceIdentity(provider, stableKey, label string) string {
	digest := sha256.Sum256([]byte(stableKey))
	label = strings.TrimSuffix(filename(label), filepath.Ext(label))
	label = strings.ReplaceAll(strings.TrimSpace(label), " ", "-")
	if label == "" {
		label = mediaTypeImage
	}
	return fmt.Sprintf("%s--%s--%x", provider, capSlug(label), digest[:8])
}

// coverRendition returns the axis ("w" or "h") and edge length of the
// smallest rendition of a width×height image that covers the target panel.
// Images wider than 16:9 are limited by their height, all others by their
// width. The length shrinks when the covering rendition would exceed the
// download pixel cap, and unknown dimensions request the target width.
func coverRendition(width, height int) (string, int) {
	if width <= 0 || height <= 0 {
		return axisWidth, targetArtworkWidth
	}
	axis, size, ratio := axisWidth, float64(targetArtworkWidth), float64(height)/float64(width)
	if int64(width)*targetArtworkHeight > int64(height)*targetArtworkWidth {
		axis, size, ratio = axisHeight, float64(targetArtworkHeight), float64(width)/float64(height)
	}
	if pixels := size * size * ratio; pixels > float64(maxDownloadedPixels) {
		size *= math.Sqrt(float64(maxDownloadedPixels) / pixels)
	}
	return axis, int(size)
}

// withQuery returns rawURL with params merged into its query string.
func withQuery(rawURL string, params map[string]string) (string, error) {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse rendition URL: %w", err)
	}
	if parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS {
		return "", fmt.Errorf("unsupported rendition URL scheme %q", parsed.Scheme)
	}
	query := parsed.Query()
	for name, value := range params {
		query.Set(name, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// coverQuery returns the width or height query parameter that requests a
// covering rendition from an imgix-style image CDN.
func coverQuery(width, height int) map[string]string {
	axis, size := coverRendition(width, height)
	return map[string]string{axis: strconv.Itoa(size)}
}

// collectPages fetches pages 1..maxPages, stopping after the first page that
// is not full.
func collectPages[T any](maxPages int, fetch func(page int) (items []T, full bool, err error)) ([]T, error) {
	var all []T
	for page := 1; page <= maxPages; page++ {
		items, full, err := fetch(page)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if !full {
			break
		}
	}
	return all, nil
}

func sourceURLIdentity(provider, rawURL string) string {
	parsed, err := neturl.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return sourceIdentity(provider, rawURL, slugDirectSource)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	label := parsed.Host
	if len(parts) >= 2 {
		label = parts[len(parts)-2] + "-" + parts[len(parts)-1]
	} else if len(parts) == 1 && parts[0] != "" {
		label = parts[0]
	}
	return sourceIdentity(provider, parsed.String(), label)
}

var (
	// reSpaces collapses multiple consecutive spaces into one.
	reSpaces = regexp.MustCompile(` +`)
	// reUnsafeChars matches unsafe characters (allowing dots, hyphens, underscores).
	reUnsafeChars = regexp.MustCompile(`[^a-zA-Z0-9 \._\-]`)
	// reDots matches multiple dots.
	reDots = regexp.MustCompile(`\.+`)
)

// filename sanitizes a filename by stripping special characters from the stem,
// collapsing spaces, and lowercasing the extension. This prevents issues with
// the Samsung TV art API which can choke on certain characters.
//
// Examples:
//
//	"café (1).JPEG" → "caf 1.jpeg"
//	"...#$%.png"    → "image.png"
//	"hello.JPG"     → "hello.jpg"
func filename(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	stem := strings.TrimSuffix(name, filepath.Ext(name))

	// Remove unsafe characters (allow dots, hyphens, underscores).
	stem = reUnsafeChars.ReplaceAllString(stem, "")

	// Collapse multiple spaces and trim.
	stem = reSpaces.ReplaceAllString(strings.TrimSpace(stem), " ")

	// Collapse multiple dots to prevent ".." or similar.
	stem = reDots.ReplaceAllString(stem, ".")

	if stem == "" || stem == "." {
		stem = mediaTypeImage
	}

	return stem + ext
}
