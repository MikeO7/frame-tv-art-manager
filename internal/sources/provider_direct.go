package sources

import (
	"context"
	"fmt"
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
	idx := atomic.AddInt32(globalIndex, 1) - 1
	identity := fmt.Sprintf("%03d__direct__%s", idx, URLToSlug(urlStr))

	return []SourceImage{
		{
			URL:      urlStr,
			Identity: identity,
		},
	}, nil
}
