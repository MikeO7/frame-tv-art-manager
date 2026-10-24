package sources

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testImageURL = "http://x.com/orig.jpg"

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create test listener: %v", err)
	}
	ts := httptest.NewUnstartedServer(handler)
	ts.Listener = ln
	ts.Start()
	t.Cleanup(func() { ts.Close() })
	return ts
}

func TestNASAResolveAPODPrefersHDImageAndRejectsVideo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   int
		response apodResponse
		wantURL  string
		wantErr  string
	}{
		{name: "HD image", status: http.StatusOK, response: apodResponse{URL: "https://apod.example/a.jpg", HDURL: "https://apod.example/hd.jpg", Type: mediaTypeImage}, wantURL: "https://apod.example/hd.jpg"},
		{name: "standard image fallback", status: http.StatusOK, response: apodResponse{URL: "https://apod.example/a.jpg", Type: mediaTypeImage}, wantURL: "https://apod.example/a.jpg"},
		{name: "video", status: http.StatusOK, response: apodResponse{URL: "https://apod.example/v.mp4", Type: "video"}, wantErr: "not an image"},
		{name: "API failure", status: http.StatusTooManyRequests, wantErr: "nasa apod api error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/planetary/apod" || r.URL.Query().Get("api_key") != "test_key_1" {
					t.Errorf("unexpected APOD request %s", r.URL.String())
				}
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(test.response)
			}))
			provider := newNasaProvider("test_key_1", slog.New(slog.DiscardHandler))
			provider.BaseURL = server.URL

			images, err := provider.Resolve(context.Background(), "nasa:apod")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Resolve(nasa:apod) error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || len(images) != 1 || images[0].URL != test.wantURL || !strings.HasPrefix(images[0].Identity, "nasa--") {
				t.Fatalf("Resolve(nasa:apod) = %+v, %v; want %s", images, err, test.wantURL)
			}
		})
	}
}

func TestNASAClient_Search(t *testing.T) {
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		if strings.Contains(r.URL.Path, "/search") {
			result := struct {
				Collection struct {
					Items []struct {
						Href string `json:"href"`
					} `json:"items"`
				} `json:"collection"`
			}{}
			result.Collection.Items = append(result.Collection.Items, struct {
				Href string `json:"href"`
			}{Href: "http://" + r.Host + "/manifest.json"})
			_ = json.NewEncoder(w).Encode(result)
		} else {
			manifest := []string{"http://x.com/image~orig.jpg"}
			_ = json.NewEncoder(w).Encode(manifest)
		}
	}))
	defer server.Close()

	c := newNasaProvider("some_other_key", slog.Default())
	c.SearchURL = server.URL

	urls, err := c.SearchNASAImageLibrary(context.Background(), "mars")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(urls) != 1 || urls[0] != "http://x.com/image~orig.jpg" {
		t.Errorf("expected 1 URL, got %v", urls)
	}
}

func TestArticClient_Search(t *testing.T) {
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		result := struct {
			Data []articArtwork `json:"data"`
		}{
			Data: []articArtwork{{ID: 456, ImageID: "a1"}},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := newArticProvider()
	c.BaseURL = server.URL
	urls, err := c.Search(context.Background(), "monet")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("expected 1 URL, got %d", len(urls))
	}
}

func TestArticClient_FetchPhoto(t *testing.T) {
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		result := struct {
			Data articArtwork `json:"data"`
		}{
			Data: articArtwork{ID: 1, Title: "Art", ImageID: "img123"},
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := newArticProvider()
	c.BaseURL = server.URL

	url, err := c.FetchPhoto(context.Background(), "1")
	if err != nil {
		t.Fatalf("FetchPhoto failed: %v", err)
	}

	if url == "" || !strings.Contains(url, "img123") {
		t.Errorf("expected URL containing img123, got %s", url)
	}
}

func TestPexelsClient_FetchPhoto(t *testing.T) {
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		photo := pexelsPhoto{ID: 123}
		photo.Src.Original = testImageURL
		_ = json.NewEncoder(w).Encode(photo)
	}))
	defer server.Close()

	c := newPexelsProvider("key", slog.Default())
	c.BaseURL = server.URL

	url, err := c.FetchPhoto(context.Background(), "123")
	if err != nil {
		t.Fatalf("FetchPhoto failed: %v", err)
	}
	if url != testImageURL {
		t.Errorf("expected http://x.com/orig.jpg, got %s", url)
	}
}

func TestPexelsClient_Search(t *testing.T) {
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		result := struct {
			Photos []pexelsPhoto `json:"photos"`
		}{
			Photos: []pexelsPhoto{{ID: 1}},
		}
		result.Photos[0].Src.Original = testImageURL
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := newPexelsProvider("key", slog.Default())
	c.BaseURL = server.URL

	urls, err := c.Search(context.Background(), "nature")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(urls) != 1 || urls[0] != testImageURL {
		t.Errorf("expected 1 URL, got %v", urls)
	}
}

func TestPexelsClient_Curated(t *testing.T) {
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		result := struct {
			Photos []pexelsPhoto `json:"photos"`
		}{
			Photos: []pexelsPhoto{{ID: 1}},
		}
		result.Photos[0].Src.Original = testImageURL
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := newPexelsProvider("key", slog.Default())
	c.BaseURL = server.URL

	urls, err := c.Curated(context.Background())
	if err != nil {
		t.Fatalf("Curated failed: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("expected 1 URL, got %d", len(urls))
	}
}

func TestPexelsClient_FetchCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = w
		_ = r
		result := struct {
			Media []pexelsPhoto `json:"media"`
		}{
			Media: []pexelsPhoto{{ID: 1}},
		}
		result.Media[0].Src.Original = testImageURL
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := newPexelsProvider("key", slog.Default())
	c.BaseURL = server.URL

	urls, err := c.FetchCollection(context.Background(), "abc")
	if err != nil {
		t.Fatalf("FetchCollection failed: %v", err)
	}
	if len(urls) != 1 {
		t.Errorf("expected 1 URL, got %d", len(urls))
	}
}

func TestNASAClient_FetchNASAAssetManifest_Errors(t *testing.T) {
	// Test HTTP non-200
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server500.Close()

	c := newNasaProvider("some_other_key", slog.Default())
	_, err := c.fetchNASAAssetManifest(context.Background(), server500.URL)
	if err == nil || !strings.Contains(err.Error(), "nasa asset manifest api error") {
		t.Errorf("expected 500 error, got %v", err)
	}

	// Test Malformed JSON
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{bad-json}"))
	}))
	defer serverBadJSON.Close()

	_, err = c.fetchNASAAssetManifest(context.Background(), serverBadJSON.URL)
	if err == nil {
		t.Error("expected json decode error, got nil")
	}

	// Test Large.jpg fallback
	serverLarge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifest := []string{"http://x.com/image~small.jpg", "http://x.com/image~large.jpg"}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer serverLarge.Close()

	url, err := c.fetchNASAAssetManifest(context.Background(), serverLarge.URL)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if url != "http://x.com/image~large.jpg" {
		t.Errorf("expected large image fallback, got %s", url)
	}

	// Test client Do error
	// Use an invalid URL protocol to force http.Client.Do to fail
	_, err = c.fetchNASAAssetManifest(context.Background(), "invalid-url://foo")
	if err == nil {
		t.Error("expected client do error, got nil")
	}

	// Test request context error
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.fetchNASAAssetManifest(ctx, serverLarge.URL)
	if err == nil {
		t.Error("expected context canceled error, got nil")
	}

	// Test bad URL error
	_, err = c.fetchNASAAssetManifest(context.Background(), "://foo")
	if err == nil {
		t.Error("expected new request error, got nil")
	}
}

func TestNASAClient_SearchNASAImageLibrary_Errors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			Collection struct {
				Items []struct {
					Href string `json:"href"`
				} `json:"items"`
			} `json:"collection"`
		}{}
		result.Collection.Items = append(result.Collection.Items, struct {
			Href string `json:"href"`
		}{Href: "invalid-url://foo"}) // Will cause fetchNASAAssetManifest to return an error, hitting the `if err != nil { p.logger.Warn(...); continue }` block
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	c := newNasaProvider("some_other_key", slog.Default())
	c.SearchURL = server.URL

	urls, err := c.SearchNASAImageLibrary(context.Background(), "mars")
	if err == nil {
		t.Fatal("expected incomplete NASA resolution to return an error")
	}
	if len(urls) != 0 {
		t.Errorf("expected 0 URLs, got %v", urls)
	}
}
