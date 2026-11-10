package assistant

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	gosync "sync"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/sources"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

// fakeSearcher answers with a fixed result and records each query.
type fakeSearcher struct {
	mu      gosync.Mutex
	result  sources.SearchResult
	err     error
	queries []sources.SearchQuery
}

func (searcher *fakeSearcher) Search(_ context.Context, query sources.SearchQuery) (sources.SearchResult, error) {
	searcher.mu.Lock()
	defer searcher.mu.Unlock()
	searcher.queries = append(searcher.queries, query)
	return searcher.result, searcher.err
}

func (*fakeSearcher) Providers() []string {
	return []string{"artic", "met", "cleveland", "smk"}
}

func (searcher *fakeSearcher) recorded() []sources.SearchQuery {
	searcher.mu.Lock()
	defer searcher.mu.Unlock()
	return slices.Clone(searcher.queries)
}

func TestSearchFindsArtThatAddArtworkKeepsWithCredit(t *testing.T) {
	t.Parallel()
	painting := paintedJPEG(t, 160, 90, 5)
	server := newImageServer(t, map[string]http.HandlerFunc{
		"/iiif/winter-landscape.jpg": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(painting)
		},
	})
	searcher := &fakeSearcher{result: sources.SearchResult{
		Candidates: []sources.Candidate{{
			Provider: "met", ID: "436524", Title: "Winter Landscape with Skaters", Artist: "Hendrick Avercamp",
			Date: "ca. 1608", ImageURL: server.link("/iiif/winter-landscape.jpg"), Width: 3840, Height: 2160,
			License: "CC0", Link: "https://www.metmuseum.org/art/collection/search/436524", SourceLine: "met:object:436524",
		}, {
			Provider: "smk", ID: "KMS1", Title: "Portrait", ImageURL: "https://example.com/portrait.jpg",
			Width: 1000, Height: 2000, License: "Public Domain", SourceLine: "smk:object:KMS1",
		}},
		Failures: []sources.ProviderFailure{{Provider: "cleveland", Error: "HTTP 503"}},
	}}
	resolver := &mapResolver{answers: map[string][]string{"example.com": {"127.0.0.1"}}}
	fixture := newFixture(t, fixtureOptions{
		searcher: searcher,
		fetcher:  newFetcher(fetcherOptions{Resolver: resolver, Allow: loopbackOrPublic, RootCAs: server.pool}),
	})

	var found searchArtOutput
	fixture.call(t, "search_public_domain_art", map[string]any{"query": "  winter  ", "providers": []string{"met", "smk"}}, &found)
	queries := searcher.recorded()
	if len(queries) != 1 || queries[0].Text != "winter" || queries[0].Limit != defaultSearchLimit ||
		!slices.Equal(queries[0].Providers, []string{"met", "smk"}) || queries[0].ProviderTimeout != searchProviderTimeout {
		t.Fatalf("queries = %+v, want the trimmed text, default limit, providers, and timeout", queries)
	}
	if len(found.Candidates) != 2 || found.Candidates[0].Fit != "exact" || found.Candidates[1].Fit != "mat" ||
		found.Candidates[0].SourceLine != "met:object:436524" || len(found.Failures) != 1 ||
		found.Failures[0].Provider != "cleveland" || !slices.Contains(found.Providers, "met") {
		t.Fatalf("search = %+v, want both candidates with fit predictions and the failed museum", found)
	}

	chosen := found.Candidates[0]
	var added addArtworkOutput
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Dutch Winter", "image_url": chosen.ImageURL, "title": chosen.Title,
		"artist": chosen.Artist, "license": chosen.License, "link": chosen.Link,
	}, &added)
	if added.Width != 160 || !strings.HasPrefix(added.Filename, "winter-landscape-with-skaters") {
		t.Fatalf("add_artwork(candidate) = %+v", added)
	}
	file, err := fixture.themes.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	index, err := file.Find("dutch-winter")
	if err != nil {
		t.Fatal(err)
	}
	member := file.Collections[index].Members[0]
	if member.Title != chosen.Title || member.Artist != chosen.Artist || member.License != "CC0" || member.Link != chosen.Link {
		t.Fatalf("member = %+v, want the candidate's credit recorded", member)
	}
	var listed listArtworkOutput
	fixture.call(t, "list_artwork", map[string]any{"collection": "dutch-winter"}, &listed)
	if len(listed.Artwork) != 1 || listed.Artwork[0].License != "CC0" || listed.Artwork[0].Link != chosen.Link ||
		listed.Artwork[0].Artist != "Hendrick Avercamp" {
		t.Fatalf("list_artwork = %+v, want the credit shown", listed)
	}
}

func TestSearchRejectsBadRequestsAndReportsTotalFailure(t *testing.T) {
	t.Parallel()
	searcher := &fakeSearcher{
		result: sources.SearchResult{Failures: []sources.ProviderFailure{{Provider: "met", Error: "timeout"}}},
		err:    errors.New("every museum search provider failed: met: timeout"),
	}
	fixture := newFixture(t, fixtureOptions{searcher: searcher})
	for name, test := range map[string]struct {
		arguments map[string]any
		want      string
	}{
		"empty query":        {map[string]any{"query": "  "}, "query is required"},
		"limit too high":     {map[string]any{"query": "snow", "limit": maximumSearchLimit + 1}, "limit must be between"},
		"negative limit":     {map[string]any{"query": "snow", "limit": -1}, "limit must be between"},
		"all museums failed": {map[string]any{"query": "snow", "limit": 3}, "every museum search provider failed"},
	} {
		if text := fixture.callError(t, "search_public_domain_art", test.arguments); !strings.Contains(text, test.want) {
			t.Fatalf("%s: error = %q, want %q", name, text, test.want)
		}
	}
	if queries := searcher.recorded(); len(queries) != 1 || queries[0].Limit != 3 {
		t.Fatalf("queries = %+v, want only the valid request to reach the searcher", queries)
	}
}

func TestAddArtworkRejectsAnUnsafeCreditLinkBeforeStoringAnything(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	text := fixture.callError(t, "add_artwork", map[string]any{
		"collection": "Christmas", "image_base64": base64Of(paintedJPEG(t, 32, 18, 6)), "link": "javascript:alert(1)",
	})
	if !strings.Contains(text, "http or https") {
		t.Fatalf("add_artwork error = %q", text)
	}
	file, err := fixture.themes.Load(context.Background())
	if err != nil || len(file.Collections) != 0 {
		t.Fatalf("collections after rejected add = %+v, %v; want none", file.Collections, err)
	}
}

func TestSearchToolIsOmittedWithoutASearcher(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{noSearcher: true})
	listed, err := fixture.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "search_public_domain_art" {
			t.Fatal("search tool offered without a searcher")
		}
	}
}

func TestValidateLink(t *testing.T) {
	t.Parallel()
	for link, valid := range map[string]bool{
		"": true, "https://www.metmuseum.org/art/1": true, "http://example.org/work": true,
		"ftp://example.org/x": false, "javascript:alert(1)": false, "https://": false,
		"https://user:pass@example.org/": false, "/relative/page": false,
	} {
		if err := themes.ValidateLink(link); (err == nil) != valid {
			t.Fatalf("ValidateLink(%q) error = %v, want valid %t", link, err, valid)
		}
	}
}
