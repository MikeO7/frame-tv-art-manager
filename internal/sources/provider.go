package sources

import (
	"context"
	neturl "net/url"
	"strings"
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
	return "direct-source"
}
