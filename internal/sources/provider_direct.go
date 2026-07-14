package sources

import (
	"context"
	"errors"
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
		return nil, errors.New("invalid direct URL format")
	}
	if u.Scheme != schemeHTTP && u.Scheme != schemeHTTPS {
		return nil, errors.New("unsupported direct URL scheme")
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
