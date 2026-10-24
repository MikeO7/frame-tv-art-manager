package sources

import (
	"context"
	"crypto/sha256"
	"fmt"
	neturl "net/url"
	"path/filepath"
	"regexp"
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
)

// capSlug truncates a slug to maxSlugLen runes' worth of bytes (slugs are ASCII).
func capSlug(slug string) string {
	if len(slug) > maxSlugLen {
		return slug[:maxSlugLen]
	}
	return slug
}

// SourceImage represents resolved metadata for a downloadable image source.
type SourceImage struct {
	URL        string
	Identity   string
	OnDownload func(ctx context.Context) error
}

// SourceProvider defines the seam for translating configuration lines into raw downloadable metadata.
type SourceProvider interface {
	Name() string
	CanHandle(line string) bool
	Resolve(ctx context.Context, line string) ([]SourceImage, error)
}

// sourceIdentity returns an order-independent provider identity. stableKey is
// hashed so signed URLs and other sensitive source material never enter the
// manifest or filename, while label keeps generated artwork recognizable.
func sourceIdentity(provider, stableKey, label string) string {
	digest := sha256.Sum256([]byte(stableKey))
	label = strings.TrimSuffix(Filename(label), filepath.Ext(label))
	label = strings.ReplaceAll(strings.TrimSpace(label), " ", "-")
	if label == "" {
		label = mediaTypeImage
	}
	return fmt.Sprintf("%s--%s--%x", provider, capSlug(label), digest[:8])
}

// urlSourceImages maps resolved image URLs to provider-scoped source images.
func urlSourceImages(provider string, urls []string) []SourceImage {
	images := make([]SourceImage, 0, len(urls))
	for _, u := range urls {
		images = append(images, SourceImage{URL: u, Identity: sourceURLIdentity(provider, u)})
	}
	return images
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

// Filename sanitizes a filename by stripping special characters from the stem,
// collapsing spaces, and lowercasing the extension. This prevents issues with
// the Samsung TV art API which can choke on certain characters.
//
// Examples:
//
//	"café (1).JPEG" → "caf 1.jpeg"
//	"...#$%.png"    → "image.png"
//	"hello.JPG"     → "hello.jpg"
func Filename(name string) string {
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
