package sources

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

func TestSearchArgument(t *testing.T) {
	t.Parallel()
	tests := []struct {
		argument  string
		allowed   []string
		wantQuery string
		wantKey   string
		wantErr   string
	}{
		{argument: " sunflowers ", wantQuery: "sunflowers"},
		{argument: "cats?departmentId=6", allowed: []string{"departmentId"}, wantQuery: "cats", wantKey: "departmentId"},
		{argument: "?isHighlight=true", allowed: []string{"isHighlight"}, wantKey: "isHighlight"},
		{argument: "cats?isPublicDomain=false", allowed: []string{"departmentId"}, wantErr: "use one of departmentId"},
		{argument: "cats?creator=x", wantErr: "takes no filters"},
		{argument: "  ?", wantErr: "query or at least one filter"},
		{argument: "cats?a=%zz", allowed: []string{"a"}, wantErr: "parse search filters"},
	}
	for _, test := range tests {
		query, filters, err := searchArgument(test.argument, test.allowed...)
		if test.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Errorf("searchArgument(%q) error = %v, want %q", test.argument, err, test.wantErr)
			}
			continue
		}
		if err != nil || query != test.wantQuery || (test.wantKey != "" && !filters.Has(test.wantKey)) {
			t.Errorf("searchArgument(%q) = (%q, %v, %v)", test.argument, query, filters, err)
		}
	}
}

func TestRankRenditionsPrefersSmallestCoveringThenLargest(t *testing.T) {
	t.Parallel()
	ranked := renditionURLs(rankRenditions([]rendition{
		{url: "https://cdn.example/web.jpg", width: 900, height: 600},
		{url: "https://cdn.example/print.jpg", width: 3400, height: 2267},
		{url: "https://cdn.example/huge.jpg", width: 9000, height: 6000},
		{url: "https://cdn.example/4k.jpg", width: 4000, height: 2667},
		{url: "https://cdn.example/6k.jpg", width: 6000, height: 4000},
		{url: "https://cdn.example/full.tif", width: 4000, height: 2667},
		{url: "https://cdn.example/unknown.png"},
		{url: "https://cdn.example/4k.jpg", width: 4000, height: 2667},
	}))
	want := []string{
		"https://cdn.example/4k.jpg", "https://cdn.example/6k.jpg",
		"https://cdn.example/print.jpg", "https://cdn.example/unknown.png",
	}
	if !slices.Equal(ranked, want) {
		t.Fatalf("rankRenditions() = %v, want %v (TIFF, oversized, and too-small renditions dropped)", ranked, want)
	}
	if width, height := scaledSize(0, 100, axisWidth, 10); width != 0 || height != 0 {
		t.Errorf("scaledSize(unknown) = %dx%d", width, height)
	}
}

func TestPlainTextStripsMarkupAndEntities(t *testing.T) {
	t.Parallel()
	if got := plainText("<bdi><a href=\"x\">Jane</a>\n  Doe</bdi> &amp; <span>Co.</span>"); got != "Jane Doe & Co." {
		t.Fatalf("plainText() = %q", got)
	}
}

func TestPacedRateGateSpacesRequests(t *testing.T) {
	t.Parallel()
	gate := newPacedRateGate(30 * time.Millisecond)
	start := time.Now()
	for range 3 {
		if err := gate.wait(context.Background()); err != nil {
			t.Fatalf("wait() error = %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("three paced requests took %s, want at least two intervals", elapsed)
	}

	slow := newPacedRateGate(time.Hour)
	if err := slow.wait(context.Background()); err != nil {
		t.Fatalf("first wait() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := slow.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait() past the deadline = %v, want context.DeadlineExceeded", err)
	}
	if err := (*rateGate)(nil).wait(context.Background()); err != nil {
		t.Fatalf("nil gate wait() = %v", err)
	}
}

func TestNewLoaderRoutesMuseumLinesThroughSharedProviders(t *testing.T) {
	t.Parallel()
	loader := NewLoader(&config.Config{}, discardLogger(), nil)
	for line, want := range map[string]string{
		"met:object:436524": providerMet, "cleveland:search:snow": providerCleveland, "smk:object:KMS1": providerSMK,
		"commons:potd": providerCommons, "museum:search:winter": museumProviderName, "artic:photo:1": "artic",
		"https://example.com/art.jpg": directProviderName,
	} {
		if provider := loader.resolveProvider(line); provider == nil || provider.Name() != want {
			t.Errorf("resolveProvider(%q) = %v, want %s", line, provider, want)
		}
	}
	museum, ok := loader.resolveProvider("museum:search:winter").(*museumProvider)
	if !ok {
		t.Fatal("museum line is not handled by the museum provider")
	}
	for index, name := range museum.searcher.Providers() {
		if any(museum.searcher.searchers[index]) != any(loader.resolveProvider(name+":search:x")) {
			t.Errorf("museum search uses its own %s provider; it must share pacing and rate-limit state with %s lines", name, name)
		}
	}
}
