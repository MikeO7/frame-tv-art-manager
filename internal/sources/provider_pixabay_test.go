package sources

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func newPixabayTestProvider(t *testing.T, logger *slog.Logger, handler http.HandlerFunc) *pixabayProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider := newPixabayProvider("pixabay-key", logger)
	provider.client, provider.BaseURL = server.Client(), server.URL
	return provider
}

func writePixabayHits(t *testing.T, w http.ResponseWriter, hits []pixabayPhoto) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(struct {
		Hits []pixabayPhoto `json:"hits"`
	}{Hits: hits}); err != nil {
		t.Errorf("encode Pixabay response: %v", err)
	}
}

func TestPixabayResolveRoutesCommandsToAPIQueries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line       string
		wantQuery  map[string]string
		paginated  bool
		wantImages int
	}{
		{line: "pixabay:search:red fox", wantQuery: map[string]string{"q": "red fox", "image_type": "photo"}, paginated: true, wantImages: 1},
		{line: "pixabay:editors_choice", wantQuery: map[string]string{"editors_choice": "true", "image_type": "photo"}, paginated: true, wantImages: 1},
		{line: "pixabay:photo:123", wantQuery: map[string]string{"id": "123"}, wantImages: 1},
		{line: "pixabay:user:42", wantQuery: map[string]string{"user_id": "42", "image_type": "photo"}, paginated: true, wantImages: 1},
	}
	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			t.Parallel()
			provider := newPixabayTestProvider(t, slog.New(slog.DiscardHandler), func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				if query.Get("key") != "pixabay-key" {
					t.Errorf("API key = %q", query.Get("key"))
				}
				for name, want := range test.wantQuery {
					if got := query.Get(name); got != want {
						t.Errorf("query %s = %q, want %q", name, got, want)
					}
				}
				if got := query.Has("per_page"); got != test.paginated {
					t.Errorf("paginated request = %t, want %t (query %v)", got, test.paginated, query)
				}
				writePixabayHits(t, w, []pixabayPhoto{{ID: 7, LargeImageURL: "https://cdn.pixabay.example/photos/7/large.jpg"}})
			})

			images, err := provider.Resolve(context.Background(), test.line)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", test.line, err)
			}
			if len(images) != test.wantImages || images[0].URL != "https://cdn.pixabay.example/photos/7/large.jpg" ||
				!strings.HasPrefix(images[0].Identity, "pixabay--") {
				t.Fatalf("Resolve(%q) = %+v", test.line, images)
			}
		})
	}
}

func TestPixabayResolveRejectsMalformedLinesWithoutNetwork(t *testing.T) {
	t.Parallel()
	provider := newPixabayTestProvider(t, slog.New(slog.DiscardHandler), func(http.ResponseWriter, *http.Request) {
		t.Error("malformed source line reached the Pixabay API")
	})
	tests := map[string]string{
		"pixabay":               "invalid pixabay format",
		"pixabay:search":        "search requires a query",
		"pixabay:photo":         "photo requires an ID",
		"pixabay:user":          "user requires an ID",
		"pixabay:unknown_cmd:1": "unknown pixabay type",
	}
	for line, want := range tests {
		if _, err := provider.Resolve(context.Background(), line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want %q", line, err, want)
		}
	}
}

func TestPixabayPhotoListPrefersHighestResolution(t *testing.T) {
	t.Parallel()
	provider := newPixabayTestProvider(t, slog.New(slog.DiscardHandler), func(w http.ResponseWriter, _ *http.Request) {
		writePixabayHits(t, w, []pixabayPhoto{
			{ID: 1, ImageURL: "image1", FullHDURL: "full1", LargeImageURL: "large1"},
			{ID: 2, FullHDURL: "full2", LargeImageURL: "large2"},
			{ID: 3, LargeImageURL: "large3"},
			{ID: 4},
		})
	})
	urls, err := provider.fetchPhotoList(context.Background(), provider.BaseURL)
	if err != nil {
		t.Fatalf("fetchPhotoList() error = %v", err)
	}
	if strings.Join(urls, ",") != "image1,full2,large3" {
		t.Fatalf("fetchPhotoList() = %v, want original, Full HD, then large image fallbacks", urls)
	}
}

func TestPixabayPaginationStopsAtShortEmptyOrMaximumPage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		pageSizes []int
		wantURLs  int
		wantPages int32
	}{
		{name: "empty first page", pageSizes: []int{0}, wantURLs: 0, wantPages: 1},
		{name: "short page ends crawl", pageSizes: []int{pixabayPageSize, 3}, wantURLs: pixabayPageSize + 3, wantPages: 2},
		{name: "empty page after full page", pageSizes: []int{pixabayPageSize, 0}, wantURLs: pixabayPageSize, wantPages: 2},
		{
			name: "maximum pages", pageSizes: []int{pixabayPageSize, pixabayPageSize, pixabayPageSize, pixabayPageSize, pixabayPageSize, pixabayPageSize},
			wantURLs: pixabayPageSize * pixabayMaxPages, wantPages: pixabayMaxPages,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			provider := newPixabayTestProvider(t, slog.New(slog.DiscardHandler), func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				page, err := strconv.Atoi(r.URL.Query().Get("page"))
				if err != nil || page < 1 || page > len(test.pageSizes) {
					t.Errorf("unexpected page request %q", r.URL.RawQuery)
					return
				}
				if got := r.URL.Query().Get("per_page"); got != strconv.Itoa(pixabayPageSize) {
					t.Errorf("per_page = %q", got)
				}
				hits := make([]pixabayPhoto, test.pageSizes[page-1])
				for index := range hits {
					hits[index] = pixabayPhoto{ID: index, LargeImageURL: "https://cdn.pixabay.example/" + strconv.Itoa(index) + ".jpg"}
				}
				writePixabayHits(t, w, hits)
			})

			urls, err := provider.Search(context.Background(), "nature")
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			if len(urls) != test.wantURLs || requests.Load() != test.wantPages {
				t.Fatalf("Search() returned %d URLs over %d pages, want %d over %d", len(urls), requests.Load(), test.wantURLs, test.wantPages)
			}
		})
	}
}

func TestPixabayResolvePropagatesAPIFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		line    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "server error", line: "pixabay:search:mars",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			want:    "pixabay api error",
		},
		{
			name: "photo lookup server error", line: "pixabay:photo:9",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
			want:    "pixabay api error",
		},
		{
			name: "malformed JSON", line: "pixabay:user:42",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{invalid-json")) },
			want:    "decode pixabay response",
		},
		{
			name: "photo not found", line: "pixabay:photo:123",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"hits":[]}`)) },
			want:    "pixabay photo not found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := newPixabayTestProvider(t, slog.New(slog.DiscardHandler), test.handler)
			images, err := provider.Resolve(context.Background(), test.line)
			if err == nil || !strings.Contains(err.Error(), test.want) || images != nil {
				t.Fatalf("Resolve(%q) = %v, %v; want error containing %q", test.line, images, err, test.want)
			}
		})
	}
}

func TestPixabayRequestsDoNotLogAPIKey(t *testing.T) {
	t.Parallel()
	logger, logs := newTestLogger()
	provider := newPixabayTestProvider(t, logger, func(w http.ResponseWriter, _ *http.Request) {
		writePixabayHits(t, w, nil)
	})
	provider.apiKey = "codeql-secret-api-key"

	if _, err := provider.Search(context.Background(), "nature"); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !strings.Contains(logs.String(), "fetching pixabay page") {
		t.Fatalf("debug log omitted page fetch: %s", logs.String())
	}
	if strings.Contains(logs.String(), provider.apiKey) {
		t.Fatalf("debug log exposed Pixabay API key: %s", logs.String())
	}
}
