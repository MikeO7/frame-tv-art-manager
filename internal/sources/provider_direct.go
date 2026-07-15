package sources

import (
	"context"
	"errors"
	neturl "net/url"
	"strings"
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

func (p *directProvider) Resolve(ctx context.Context, line string) ([]SourceImage, error) {
	urlStr := strings.TrimPrefix(line, "direct:")

	u, err := neturl.Parse(urlStr)
	if err != nil {
		return nil, errors.New("invalid direct URL format")
	}
	if u.Scheme != schemeHTTP && u.Scheme != schemeHTTPS {
		return nil, errors.New("unsupported direct URL scheme")
	}

	return []SourceImage{
		{
			URL:      urlStr,
			Identity: sourceURLIdentity(p.Name(), urlStr),
		},
	}, nil
}
