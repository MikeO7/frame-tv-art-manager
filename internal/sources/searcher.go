package sources

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// defaultSearchLimit is the number of candidates Search returns when
	// SearchQuery.Limit is zero.
	defaultSearchLimit = 20
	// maxSearchLimit is the most candidates one Search returns.
	maxSearchLimit = 100
	// defaultSearchProviderTimeout bounds each provider's share of a Search
	// when SearchQuery.ProviderTimeout is zero.
	defaultSearchProviderTimeout = 30 * time.Second

	museumProviderName = "museum"
)

// SearchQuery describes a free-text search across the public-domain museum
// collections.
type SearchQuery struct {
	// Text is the free-text query, for example "christmas" or "winter landscape".
	Text string
	// Limit is the most candidates to return; zero means DefaultSearchLimit
	// and values above MaxSearchLimit are capped.
	Limit int
	// Providers restricts the search to these provider names (see
	// ArtSearcher.Providers); empty means every provider.
	Providers []string
	// ProviderTimeout bounds each provider; zero means
	// DefaultSearchProviderTimeout.
	ProviderTimeout time.Duration
}

// Candidate is one public-domain artwork found by a search.
type Candidate struct {
	// Provider is the provider name, for example "met".
	Provider string `json:"provider"`
	// ID is the provider's identifier for the work.
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	// Date is the provider's display date, such as "1887" or "ca. 1665".
	Date string `json:"date,omitempty"`
	// ImageURL is the rendition the manager would download, sized for a
	// 3840×2160 panel where the provider allows it.
	ImageURL string `json:"image_url"`
	// Width and Height are ImageURL's pixel dimensions, or zero when the
	// dimensions are not published by the provider.
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	License string `json:"license"`
	// Link is the work's page on the museum's website.
	Link string `json:"link,omitempty"`
	// SourceLine is the ARTWORK_SOURCES_FILE line that adds exactly this
	// work, for example "met:object:436524".
	SourceLine string `json:"source_line"`

	image sourceImage
}

// ProviderFailure reports a provider that could not contribute to a search.
type ProviderFailure struct {
	Provider string `json:"provider"`
	Error    string `json:"error"`
}

// SearchResult holds the merged candidates and any provider failures. A
// result may be partial: candidates from healthy providers are returned
// alongside the failures of the others.
type SearchResult struct {
	Candidates []Candidate       `json:"candidates"`
	Failures   []ProviderFailure `json:"failures,omitempty"`
}

// candidateSearcher is implemented by providers that can contribute
// public-domain works to an ArtSearcher.
type candidateSearcher interface {
	Name() string
	searchCandidates(ctx context.Context, query string, limit int) ([]Candidate, error)
}

// artSearcher searches the public-domain museum collections (Art Institute
// of Chicago, The Met, Cleveland Museum of Art, and SMK) at once. It is safe
// for concurrent use.
type artSearcher struct {
	logger    *slog.Logger
	searchers []candidateSearcher
}

// NewArtSearcher returns a searcher whose providers send their API requests
// through client; a nil client gets a client with a 30-second timeout.
func NewArtSearcher(client *http.Client, logger *slog.Logger) *artSearcher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	artic := newArticProvider(logger)
	met := newMetProvider(logger)
	cleveland := newClevelandProvider(logger)
	smk := newSMKProvider(logger)
	artic.client, met.client, cleveland.client, smk.client = client, client, client, client
	return newArtSearcher(logger, artic, met, cleveland, smk)
}

func newArtSearcher(logger *slog.Logger, searchers ...candidateSearcher) *artSearcher {
	return &artSearcher{logger: logger, searchers: searchers}
}

// Providers returns the provider names Search can query, in merge order.
func (s *artSearcher) Providers() []string {
	names := make([]string, 0, len(s.searchers))
	for _, searcher := range s.searchers {
		names = append(names, searcher.Name())
	}
	return names
}

// Search queries the selected providers concurrently, each bounded by the
// provider timeout, and merges their candidates round-robin so every
// collection is represented. Duplicates (the same work, or the same title by
// the same artist from another collection) are dropped. Provider failures are
// reported in the result; the error is non-nil only for an invalid query, a
// cancelled ctx, or when every selected provider failed.
func (s *artSearcher) Search(ctx context.Context, query SearchQuery) (SearchResult, error) {
	text := strings.TrimSpace(query.Text)
	if text == "" {
		return SearchResult{}, errors.New("search text is required")
	}
	selected, err := s.selected(query.Providers)
	if err != nil {
		return SearchResult{}, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	limit = min(limit, maxSearchLimit)
	timeout := query.ProviderTimeout
	if timeout <= 0 {
		timeout = defaultSearchProviderTimeout
	}

	lists := make([][]Candidate, len(selected))
	failures := make([]error, len(selected))
	var group sync.WaitGroup
	for index, searcher := range selected {
		group.Go(func() {
			providerCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			lists[index], failures[index] = searcher.searchCandidates(providerCtx, text, limit)
		})
	}
	group.Wait()
	if err := ctx.Err(); err != nil {
		return SearchResult{}, fmt.Errorf("search museum collections: %w", err)
	}

	result := SearchResult{Candidates: mergeCandidates(lists, limit)}
	var errs []error
	for index, failure := range failures {
		if failure == nil {
			continue
		}
		name := selected[index].Name()
		s.logger.Warn("museum search provider failed", "provider", name, "error", failure)
		result.Failures = append(result.Failures, ProviderFailure{Provider: name, Error: failure.Error()})
		errs = append(errs, fmt.Errorf("%s: %w", name, failure))
	}
	if len(errs) == len(selected) {
		return result, fmt.Errorf("every museum search provider failed: %w", errors.Join(errs...))
	}
	return result, nil
}

func (s *artSearcher) selected(names []string) ([]candidateSearcher, error) {
	if len(names) == 0 {
		return s.searchers, nil
	}
	selected := make([]candidateSearcher, 0, len(names))
	for _, searcher := range s.searchers {
		if slices.Contains(names, searcher.Name()) {
			selected = append(selected, searcher)
		}
	}
	for _, name := range names {
		if !slices.Contains(s.Providers(), name) {
			return nil, fmt.Errorf("unknown search provider %q; use one of %s", name, strings.Join(s.Providers(), ", "))
		}
	}
	return selected, nil
}

// mergeCandidates interleaves the providers' ranked lists, dropping
// duplicates, until limit candidates are collected.
func mergeCandidates(lists [][]Candidate, limit int) []Candidate {
	merged := make([]Candidate, 0, limit)
	seen := make(map[string]struct{})
	for depth := 0; len(merged) < limit; depth++ {
		progressed := false
		for _, list := range lists {
			if depth >= len(list) || len(merged) >= limit {
				continue
			}
			progressed = true
			candidate := list[depth]
			keys := []string{"id:" + candidate.Provider + ":" + candidate.ID}
			if title, artist := normalizedText(candidate.Title), normalizedText(candidate.Artist); title != "" && artist != "" {
				keys = append(keys, "work:"+title+"|"+artist)
			}
			if slices.ContainsFunc(keys, func(key string) bool { _, ok := seen[key]; return ok }) {
				continue
			}
			for _, key := range keys {
				seen[key] = struct{}{}
			}
			merged = append(merged, candidate)
		}
		if !progressed {
			break
		}
	}
	return merged
}

func normalizedText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

// museumProvider resolves "museum:search:<query>" lines by searching every
// public-domain museum collection at once.
type museumProvider struct {
	searcher *artSearcher
	logger   *slog.Logger
}

func newMuseumProvider(searcher *artSearcher, logger *slog.Logger) *museumProvider {
	return &museumProvider{searcher: searcher, logger: logger}
}

func (p *museumProvider) Name() string {
	return museumProviderName
}

func (p *museumProvider) CanHandle(line string) bool {
	return strings.HasPrefix(line, museumProviderName+":")
}

// Resolve keeps the works found by healthy providers when others fail; it
// fails only when every provider failed, so the loader keeps the line's
// last-known-good resolution.
func (p *museumProvider) Resolve(ctx context.Context, line string) ([]sourceImage, error) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 2 || parts[1] != cmdSearch {
		return nil, errors.New("unknown museum type; use museum:search:<query>")
	}
	if len(parts) < 3 || strings.TrimSpace(parts[2]) == "" {
		return nil, errors.New("museum search requires a query: museum:search:query")
	}
	result, err := p.searcher.Search(ctx, SearchQuery{Text: parts[2], Limit: defaultSearchLimit})
	if err != nil {
		return nil, err
	}
	for _, failure := range result.Failures {
		p.logger.Warn("museum search is missing a provider's works this time", "provider", failure.Provider)
	}
	return candidateImages(result.Candidates), nil
}
