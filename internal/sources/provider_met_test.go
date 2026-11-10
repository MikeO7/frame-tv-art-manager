package sources

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestMetProvider returns a Met provider against server with request
// pacing disabled so tests run quickly.
func newTestMetProvider(baseURL string) *metProvider {
	provider := newMetProvider(discardLogger())
	provider.BaseURL, provider.gate = baseURL, newRateGate()
	return provider
}

func metObjectsHandler(t *testing.T, objectRequests *atomic.Int32) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.1/search":
			query := r.URL.Query()
			if query.Get("hasImages") != "true" || query.Get("limit") == "" {
				t.Errorf("met search did not ask for paged works with images: %q", r.URL.RawQuery)
			}
			if query.Get("q") == "flowers" && query.Get("departmentId") != "11" {
				t.Errorf("met search dropped the department filter: %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"total":5,"objectIDs":[1,2,3,4,5]}`))
		case "/v1/objects/1":
			objectRequests.Add(1)
			_, _ = w.Write([]byte(`{"objectID":1,"isPublicDomain":true,"primaryImage":"https://images.example/ep/original/one.jpg",
				"title":"Sunflowers","artistDisplayName":"Vincent van Gogh","objectDate":"1887","objectURL":"https://www.metmuseum.org/art/collection/search/1"}`))
		case "/v1/objects/2":
			objectRequests.Add(1)
			_, _ = w.Write([]byte(`{"objectID":2,"isPublicDomain":false,"primaryImage":"","title":"Recent"}`))
		case "/v1/objects/3":
			objectRequests.Add(1)
			http.NotFound(w, r)
		case "/v1/objects/4":
			objectRequests.Add(1)
			_, _ = w.Write([]byte(`{"objectID":4,"isPublicDomain":true,"primaryImage":"https://images.example/ep/original/four.tif","title":"Scan"}`))
		case "/v1/objects/5":
			objectRequests.Add(1)
			_, _ = w.Write([]byte(`{"objectID":5,"isPublicDomain":true,"primaryImage":"https://images.example/ep/original/five.jpg","title":"Quail and Millet"}`))
		default:
			http.NotFound(w, r)
		}
	}
}

func TestMetSearchKeepsPublicDomainImagesAndCachesObjects(t *testing.T) {
	t.Parallel()
	var objectRequests atomic.Int32
	server := newIPv4TestServer(t, metObjectsHandler(t, &objectRequests))
	provider := newTestMetProvider(server.URL)

	images, err := provider.Resolve(context.Background(), "met:search:flowers?departmentId=11")
	if err != nil || len(images) != 2 {
		t.Fatalf("Resolve(search) = (%+v, %v), want the two public-domain JPEG works", images, err)
	}
	if images[0].URL != "https://images.example/ep/original/one.jpg" || len(images[0].Fallbacks) != 0 ||
		images[0].Identity != museumObjectIdentity(providerMet, "1") {
		t.Errorf("first image = %+v", images[0])
	}
	want := attribution{
		Provider: providerMet, Title: "Sunflowers", Artist: "Vincent van Gogh",
		SourceURL: "https://www.metmuseum.org/art/collection/search/1", License: publicDomainCC0,
	}
	if images[0].Attribution != want {
		t.Errorf("attribution = %+v, want %+v", images[0].Attribution, want)
	}
	if images[1].Attribution.SourceURL != "https://www.metmuseum.org/art/collection/search/5" {
		t.Errorf("fallback object link = %q", images[1].Attribution.SourceURL)
	}

	before := objectRequests.Load()
	single, err := provider.Resolve(context.Background(), "met:object:1")
	if err != nil || len(single) != 1 || single[0].Identity != images[0].Identity {
		t.Fatalf("Resolve(object) = (%+v, %v), want the cached search work", single, err)
	}
	if objectRequests.Load() != before {
		t.Errorf("object line refetched a cached object")
	}
	if _, err := provider.Resolve(context.Background(), "met:object:2"); err == nil || !strings.Contains(err.Error(), "not public domain") {
		t.Errorf("Resolve(non-public-domain object) error = %v", err)
	}
	if _, err := provider.Resolve(context.Background(), "met:object:3"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("Resolve(missing object) error = %v", err)
	}

	candidates, err := provider.searchCandidates(context.Background(), "sunflowers", 1)
	if err != nil || len(candidates) != 1 || candidates[0].SourceLine != "met:object:1" || candidates[0].Date != "1887" {
		t.Fatalf("searchCandidates(limit 1) = (%+v, %v)", candidates, err)
	}
}

func TestMetObjectCacheExpiresAndStaysBounded(t *testing.T) {
	t.Parallel()
	var objectRequests atomic.Int32
	server := newIPv4TestServer(t, metObjectsHandler(t, &objectRequests))
	provider := newTestMetProvider(server.URL)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	provider.now = func() time.Time { return now }

	for range 2 {
		if _, err := provider.object(context.Background(), 1); err != nil {
			t.Fatalf("object() error = %v", err)
		}
	}
	if objectRequests.Load() != 1 {
		t.Fatalf("object requests = %d, want one while the cache is fresh", objectRequests.Load())
	}
	now = now.Add(metObjectCacheTTL)
	if _, err := provider.object(context.Background(), 1); err != nil || objectRequests.Load() != 2 {
		t.Fatalf("object() after expiry = %v with %d requests, want a refetch", err, objectRequests.Load())
	}

	for id := range metObjectCacheSize {
		provider.objects[id+100] = cachedMetObject{expires: now.Add(time.Hour)}
	}
	if _, err := provider.object(context.Background(), 5); err != nil {
		t.Fatalf("object() into a full cache error = %v", err)
	}
	if len(provider.objects) > metObjectCacheSize {
		t.Errorf("cache holds %d objects, want at most %d", len(provider.objects), metObjectCacheSize)
	}
}

func TestMetSearchFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
		limited bool
	}{
		{
			name: "every object failing is an error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1.1/search" {
					_, _ = w.Write([]byte(`{"objectIDs":[1,2]}`))
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr: "met api error: 500",
		},
		{
			name: "a rate limit stops the scan",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1.1/search" {
					_, _ = w.Write([]byte(`{"objectIDs":[1,2,3]}`))
					return
				}
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(http.StatusTooManyRequests)
			},
			limited: true,
		},
		{
			name:    "search failure",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) },
			wantErr: "met api error: 403",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newIPv4TestServer(t, test.handler)
			provider := newTestMetProvider(server.URL)
			_, err := provider.Resolve(context.Background(), "met:search:cats")
			var limited *rateLimitError
			if test.limited {
				if !errors.As(err, &limited) {
					t.Fatalf("Resolve() error = %v, want a rate-limit error", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Resolve() error = %v, want %q", err, test.wantErr)
			}
		})
	}

	empty := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"total":0,"objectIDs":null}`))
	}))
	if images, err := newTestMetProvider(empty.URL).Resolve(context.Background(), "met:search:?isHighlight=true"); err != nil || len(images) != 0 {
		t.Fatalf("Resolve(no hits) = (%v, %v), want an empty successful resolution", images, err)
	}
}

func TestMetResolveRejectsMalformedLines(t *testing.T) {
	t.Parallel()
	provider := newMetProvider(discardLogger())
	for line, want := range map[string]string{
		"met:search":                 "invalid met format",
		"met:object:abc":             "numeric object ID",
		"met:object:-4":              "numeric object ID",
		"met:search:cats?isPublic=1": `filter "isPublic" is not supported`,
		"met:search:?":               "query or at least one filter",
		"met:other:x":                "unknown met type",
	} {
		if _, err := provider.Resolve(context.Background(), line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want %q", line, err, want)
		}
	}
}
