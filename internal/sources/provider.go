package sources

import (
	"context"
	neturl "net/url"
	"strings"
)

const (
	providerNASA     = "nasa"
	providerPexels   = "pexels"
	providerPixabay  = "pixabay"
	mediaTypeImage   = "image"
	slugDirectSource = "direct-source"
)

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
	Resolve(ctx context.Context, line string, globalIndex *int32) ([]SourceImage, error)
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
	slug := Filename(urlParts[5])
	if len(slug) > 100 {
		slug = slug[:100]
	}
	return slug
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
	slug := Filename(id)
	slug = strings.ReplaceAll(slug, " ", "-")
	if len(slug) > 100 {
		slug = slug[:100]
	}
	return slug
}

// URLToSlug generates a deterministic slug from a URL.
func URLToSlug(url string) string {
	if u, err := neturl.Parse(url); err == nil && u.Host != "" {
		host := strings.TrimPrefix(u.Host, "www.")
		path := strings.Trim(u.Path, "/")
		if parts := strings.Split(path, "/"); len(parts) > 0 {
			path = parts[0]
		}
		slug := Filename(host + "_" + path)
		slug = strings.ReplaceAll(slug, " ", "-")
		if len(slug) > 100 {
			slug = slug[:100]
		}
		return slug
	}
	return slugDirectSource
}
