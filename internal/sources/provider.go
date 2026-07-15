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

// slugFromArticURL derives a stable slug from an Art Institute IIIF URL.
func slugFromArticURL(u string) string {
	if !strings.Contains(u, "artic.edu") {
		return URLToSlug(u)
	}
	urlParts := strings.Split(u, "/")
	if len(urlParts) <= 5 {
		return URLToSlug(u)
	}
	return capSlug(Filename(urlParts[5]))
}

// slugFromNASAURL derives a stable slug from a NASA image URL.
func slugFromNASAURL(u string) string {
	if !strings.Contains(u, "nasa.gov") {
		return URLToSlug(u)
	}
	urlParts := strings.Split(u, "/")
	if len(urlParts) == 0 {
		return URLToSlug(u)
	}
	last := urlParts[len(urlParts)-1]
	id := strings.Split(last, "~")[0]
	slug := strings.ReplaceAll(Filename(id), " ", "-")
	return capSlug(slug)
}

// URLToSlug generates a deterministic slug from a URL.
func URLToSlug(url string) string {
	if u, err := neturl.Parse(url); err == nil && u.Host != "" {
		host := strings.TrimPrefix(u.Host, "www.")
		path := strings.Trim(u.Path, "/")
		if parts := strings.Split(path, "/"); len(parts) > 0 {
			path = parts[0]
		}
		slug := strings.ReplaceAll(Filename(host+"_"+path), " ", "-")
		return capSlug(slug)
	}
	return slugDirectSource
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
