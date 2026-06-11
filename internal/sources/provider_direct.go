package sources

import (
	"context"
	"fmt"
	neturl "net/url"
	"strings"
	"sync/atomic"
)

type directProvider struct{}

func newDirectProvider() *directProvider {
	return &directProvider{}
}

func (p *directProvider) Name() string {
	return "direct"
}

func (p *directProvider) CanHandle(line string) bool {
	// Direct provider is the fallback, so it matches everything not matched by others.
	// But it can also match lines starting with "direct:" explicitly.
	return true
}

func (p *directProvider) Resolve(ctx context.Context, line string, globalIndex *int32) ([]SourceImage, error) {
	urlStr := strings.TrimPrefix(line, "direct:")

	u, err := neturl.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("invalid direct URL format: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported direct URL scheme: %s", u.Scheme)
	}

	idx := atomic.AddInt32(globalIndex, 1) - 1
	identity := fmt.Sprintf("%03d__direct__%s", idx, URLToSlug(urlStr))

	return []SourceImage{
		{
			URL:      urlStr,
			Identity: identity,
		},
	}, nil
}
