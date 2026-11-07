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

func writePixabayHits(t *testing.T, w http.ResponseWriter, totalHits int, hits []pixabayPhoto) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(pixabayPage{TotalHits: totalHits, Hits: hits}); err != nil {
		t.Errorf("encode Pixabay response: %v", err)
	}
}

func TestPixabayResolveRoutesCommandsToAPIQueries(t *testing.T) {
	t.Parallel()
	longQuery := strings.Repeat("é", pixabayMaxQueryRunes+20)
	tests := []struct {
		line      string
		wantQuery map[string]string
		paginated bool
	}{
		{line: "pixabay:search:red fox", wantQuery: map[string]string{"q": "red fox", "image_type": "photo", "min_width": "3840"}, paginated: true},
		{line: "pixabay:search:" + longQuery, wantQuery: map[string]string{"q": strings.Repeat("é", pixabayMaxQueryRunes)}, paginated: true},
		{line: "pixabay:editors_choice", wantQuery: map[string]string{"editors_choice": "true", "image_type": "photo", "min_width": "3840"}, paginated: true},
		{line: "pixabay:photo:123", wantQuery: map[string]string{"id": "123"}},
	}
	for _, test := range tests {
		t.Run(test.line[:min(len(test.line), 40)], func(t *testing.T) {
			t.Parallel()
			provider := newPixabayTestProvider(t, discardLogger(), func(w http.ResponseWriter, r *http.Request) {
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
				writePixabayHits(t, w, 1, []pixabayPhoto{{
					ID: 7, LargeImageURL: "https://cdn.pixabay.example/get/temp_1280.jpg", PageURL: "https://pixabay.com/photos/fox-7/",
					User: "artist", Tags: "fox, red",
				}})
			})

			images, err := provider.Resolve(context.Background(), test.line)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", test.line, err)
			}
			if len(images) != 1 || images[0].URL != "https://cdn.pixabay.example/get/temp_1280.jpg" ||
				images[0].Identity != sourceIdentity(providerPixabay, "photo:7", "photo-7") ||
				images[0].Attribution != (Attribution{
					Provider: providerPixabay, Title: "fox, red", Artist: "artist", SourceURL: "https://pixabay.com/photos/fox-7/", License: pixabayLicense,
				}) {
				t.Fatalf("Resolve(%q) = %+v", test.line, images)
			}
		})
	}
}

func TestPixabayResolveRejectsMalformedAndUnsupportedLinesWithoutNetwork(t *testing.T) {
	t.Parallel()
	provider := newPixabayTestProvider(t, discardLogger(), func(http.ResponseWriter, *http.Request) {
		t.Error("malformed source line reached the Pixabay API")
	})
	tests := map[string]string{
		"pixabay":               "invalid pixabay format",
		"pixabay:search":        "search requires a query",
		"pixabay:photo":         "photo requires an ID",
		"pixabay:user:42":       "cannot filter by user",
		"pixabay:unknown_cmd:1": "unknown pixabay type",
	}
	for line, want := range tests {
		if _, err := provider.Resolve(context.Background(), line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want %q", line, err, want)
		}
	}
}

func TestPixabayImagesPreferHighestResolutionWithFallbacks(t *testing.T) {
	t.Parallel()
	provider := newPixabayProvider("key", discardLogger())
	images := provider.images([]pixabayPhoto{
		{ID: 1, ImageURL: "image1", FullHDURL: "full1", LargeImageURL: "large1"},
		{ID: 2, FullHDURL: "full2", LargeImageURL: "large2"},
		{ID: 3, LargeImageURL: "large3"},
		{ID: 4},
		{LargeImageURL: "no-id"},
	})
	if len(images) != 3 || images[0].URL != "image1" || strings.Join(images[0].Fallbacks, ",") != "full1,large1" ||
		images[1].URL != "full2" || images[2].URL != "large3" || len(images[2].Fallbacks) != 0 {
		t.Fatalf("images() = %+v, want original, Full HD, then large image fallbacks", images)
	}
}

func TestPixabayPaginationFollowsTotalHits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		totalHits int
		pageSizes []int
		wantURLs  int
		wantPages int32
	}{
		{name: "empty first page", totalHits: 0, pageSizes: []int{0}, wantURLs: 0, wantPages: 1},
		{name: "short page ends crawl", totalHits: 9000, pageSizes: []int{pixabayPageSize, 3}, wantURLs: pixabayPageSize + 3, wantPages: 2},
		{name: "full pages exactly reaching totalHits stop", totalHits: 400, pageSizes: []int{pixabayPageSize, pixabayPageSize}, wantURLs: 400, wantPages: 2},
		{
			name: "accessible maximum is 500", totalHits: 9000, pageSizes: []int{pixabayPageSize, pixabayPageSize, 100},
			wantURLs: pixabayMaxResults, wantPages: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			provider := newPixabayTestProvider(t, discardLogger(), func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				page, err := strconv.Atoi(r.URL.Query().Get("page"))
				if err != nil || page < 1 || page > len(test.pageSizes) {
					t.Errorf("requested page %q beyond the accessible hits", r.URL.Query().Get("page"))
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if got := r.URL.Query().Get("per_page"); got != strconv.Itoa(pixabayPageSize) {
					t.Errorf("per_page = %q", got)
				}
				hits := make([]pixabayPhoto, test.pageSizes[page-1])
				for index := range hits {
					id := page*1000 + index + 1
					hits[index] = pixabayPhoto{ID: id, LargeImageURL: "https://cdn.pixabay.example/" + strconv.Itoa(id) + ".jpg"}
				}
				writePixabayHits(t, w, test.totalHits, hits)
			})

			images, err := provider.Resolve(context.Background(), "pixabay:search:nature")
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if len(images) != test.wantURLs || requests.Load() != test.wantPages {
				t.Fatalf("Resolve() returned %d images over %d pages, want %d over %d", len(images), requests.Load(), test.wantURLs, test.wantPages)
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
			name: "malformed JSON", line: "pixabay:editors_choice",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{invalid-json")) },
			want:    "decode pixabay response",
		},
		{
			name: "photo not found", line: "pixabay:photo:123",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"hits":[]}`)) },
			want:    "pixabay photo not found",
		},
		{
			name: "rate limited", line: "pixabay:search:mars",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", "42")
				w.WriteHeader(http.StatusTooManyRequests)
			},
			want: "pixabay rate limit reached",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := newPixabayTestProvider(t, discardLogger(), test.handler)
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
		writePixabayHits(t, w, 0, nil)
	})
	provider.apiKey = "codeql-secret-api-key"

	if _, err := provider.Resolve(context.Background(), "pixabay:search:nature"); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !strings.Contains(logs.String(), "fetching pixabay page") {
		t.Fatalf("debug log omitted page fetch: %s", logs.String())
	}
	if strings.Contains(logs.String(), provider.apiKey) {
		t.Fatalf("debug log exposed Pixabay API key: %s", logs.String())
	}
}
