package sources

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeSearcher returns fixed candidates, or blocks until its context ends
// when block is set.
type fakeSearcher struct {
	name       string
	candidates []Candidate
	err        error
	block      bool
	gotQuery   string
	gotLimit   int
}

func (s *fakeSearcher) Name() string { return s.name }

func (s *fakeSearcher) searchCandidates(ctx context.Context, query string, limit int) ([]Candidate, error) {
	s.gotQuery, s.gotLimit = query, limit
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.candidates[:min(limit, len(s.candidates))], s.err
}

func fakeCandidate(provider, id, title, artist string) Candidate {
	return Candidate{
		Provider: provider, ID: id, Title: title, Artist: artist, License: publicDomainCC0,
		SourceLine: provider + ":object:" + id,
		image:      sourceImage{URL: "https://images.example/" + provider + "/" + id + ".jpg", Identity: museumObjectIdentity(provider, id)},
	}
}

func candidateIDs(candidates []Candidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.Provider+"/"+candidate.ID)
	}
	return ids
}

func TestArtSearcherMergesRoundRobinAndDedupes(t *testing.T) {
	t.Parallel()
	first := &fakeSearcher{name: "a", candidates: []Candidate{
		fakeCandidate("a", "1", "The Great Wave", "Hokusai"),
		fakeCandidate("a", "2", "Snow", "Buson"),
		fakeCandidate("a", "1", "The Great Wave", "Hokusai"),
		fakeCandidate("a", "3", "Untitled", ""),
	}}
	second := &fakeSearcher{name: "b", candidates: []Candidate{
		fakeCandidate("b", "9", "the great  WAVE", "hokusai"),
		fakeCandidate("b", "8", "Untitled", ""),
	}}
	searcher := newArtSearcher(discardLogger(), first, second)

	tests := []struct {
		name  string
		query SearchQuery
		want  []string
	}{
		{
			name:  "duplicate works and repeated IDs are dropped; untitled works without an artist are kept",
			query: SearchQuery{Text: " waves "},
			want:  []string{"a/1", "a/2", "b/8", "a/3"},
		},
		{name: "limit bounds the merged list", query: SearchQuery{Text: "waves", Limit: 2}, want: []string{"a/1", "a/2"}},
		{name: "providers restrict the search", query: SearchQuery{Text: "waves", Providers: []string{"b"}}, want: []string{"b/9", "b/8"}},
	}
	for _, test := range tests {
		result, err := searcher.Search(context.Background(), test.query)
		if err != nil || len(result.Failures) != 0 || !slices.Equal(candidateIDs(result.Candidates), test.want) {
			t.Errorf("%s: Search() = (%v, %+v, %v), want %v", test.name, candidateIDs(result.Candidates), result.Failures, err, test.want)
		}
	}
	if _, err := searcher.Search(context.Background(), SearchQuery{Text: " waves "}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if first.gotQuery != "waves" || first.gotLimit != defaultSearchLimit {
		t.Errorf("provider saw query %q limit %d, want trimmed text and the default limit", first.gotQuery, first.gotLimit)
	}
	if _, err := searcher.Search(context.Background(), SearchQuery{Text: "x", Limit: 1000}); err != nil || first.gotLimit != maxSearchLimit {
		t.Errorf("Search(limit 1000) passed limit %d, want %d", first.gotLimit, maxSearchLimit)
	}
}

func TestArtSearcherReportsPartialAndTotalFailures(t *testing.T) {
	t.Parallel()
	healthy := &fakeSearcher{name: "healthy", candidates: []Candidate{fakeCandidate("healthy", "1", "Snow", "Buson")}}
	broken := &fakeSearcher{name: "broken", err: errors.New("museum api error: 503")}
	slow := &fakeSearcher{name: "slow", block: true}
	searcher := newArtSearcher(discardLogger(), healthy, broken, slow)

	result, err := searcher.Search(context.Background(), SearchQuery{Text: "snow", ProviderTimeout: 20 * time.Millisecond})
	if err != nil || !slices.Equal(candidateIDs(result.Candidates), []string{"healthy/1"}) {
		t.Fatalf("Search() = (%+v, %v), want the healthy provider's works", result, err)
	}
	if len(result.Failures) != 2 || result.Failures[0].Provider != "broken" || !strings.Contains(result.Failures[0].Error, "503") ||
		result.Failures[1].Provider != "slow" || !strings.Contains(result.Failures[1].Error, "deadline") {
		t.Fatalf("failures = %+v, want the broken and timed-out providers", result.Failures)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "identity") || !strings.Contains(string(encoded), `"source_line":"healthy:object:1"`) {
		t.Errorf("JSON = %s (%v), want public candidate fields only", encoded, err)
	}

	result, err = searcher.Search(context.Background(), SearchQuery{Text: "snow", Providers: []string{"broken"}})
	if err == nil || !strings.Contains(err.Error(), "every museum search provider failed") || len(result.Failures) != 1 {
		t.Fatalf("Search(only failing provider) = (%+v, %v), want an error with the failure", result, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := searcher.Search(ctx, SearchQuery{Text: "snow"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search(cancelled) error = %v, want context.Canceled", err)
	}
	for _, query := range []SearchQuery{{Text: "  "}, {Text: "snow", Providers: []string{"louvre"}}} {
		if _, err := searcher.Search(context.Background(), query); err == nil {
			t.Errorf("Search(%+v) accepted an invalid query", query)
		}
	}
}

func TestNewArtSearcherSearchesEveryPublicDomainMuseum(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: errorRoundTripper{err: errors.New("offline")}}
	searcher := NewArtSearcher(client, discardLogger())
	if got := searcher.Providers(); !slices.Equal(got, []string{"artic", providerMet, providerCleveland, providerSMK}) {
		t.Fatalf("Providers() = %v", got)
	}
	result, err := searcher.Search(context.Background(), SearchQuery{Text: "snow"})
	if err == nil || len(result.Failures) != 4 {
		t.Fatalf("Search(offline) = (%+v, %v), want every provider to fail through the supplied client", result, err)
	}
	if NewArtSearcher(nil, discardLogger()) == nil {
		t.Fatal("NewArtSearcher(nil) returned nil")
	}
}

func TestMuseumProviderResolvesSearchLines(t *testing.T) {
	t.Parallel()
	healthy := &fakeSearcher{name: "healthy", candidates: []Candidate{fakeCandidate("healthy", "1", "Snow", "Buson")}}
	broken := &fakeSearcher{name: "broken", err: errors.New("down")}
	provider := newMuseumProvider(newArtSearcher(discardLogger(), healthy, broken), discardLogger())
	if !provider.CanHandle("museum:search:snow") || provider.CanHandle("met:search:snow") {
		t.Fatal("museum provider claims the wrong lines")
	}

	images, err := provider.Resolve(context.Background(), "museum:search:snow")
	if err != nil || len(images) != 1 || images[0].Identity != museumObjectIdentity("healthy", "1") {
		t.Fatalf("Resolve() = (%+v, %v), want the healthy provider's work despite the failure", images, err)
	}
	failing := newMuseumProvider(newArtSearcher(discardLogger(), broken), discardLogger())
	if _, err := failing.Resolve(context.Background(), "museum:search:snow"); err == nil {
		t.Fatal("Resolve() succeeded although every provider failed, which would replace the last-known-good resolution")
	}
	for line, want := range map[string]string{"museum": "unknown museum type", "museum:search": "requires a query", "museum:search:\t": "requires a query"} {
		if _, err := provider.Resolve(context.Background(), line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want %q", line, err, want)
		}
	}
}
