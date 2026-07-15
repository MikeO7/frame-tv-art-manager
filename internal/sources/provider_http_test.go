package sources

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceProviderResolutionContract(t *testing.T) {
	t.Parallel()

	providers := []SourceProvider{
		newUnsplashProvider("", "", "", slog.Default()),
		newNasaProvider("", slog.Default()),
		newArticProvider(slog.Default()),
		newPexelsProvider("", slog.Default()),
		newPixabayProvider("", slog.Default()),
	}
	for _, provider := range providers {
		t.Run(provider.Name(), func(t *testing.T) {
			t.Parallel()
			line := provider.Name() + ":unknown"
			if !provider.CanHandle(line) {
				t.Fatalf("%s adapter rejected its source expression", provider.Name())
			}
			if _, err := provider.Resolve(context.Background(), line); err == nil {
				t.Fatalf("%s Resolve() accepted an unknown provider operation", provider.Name())
			}
		})
	}

	images, err := newDirectProvider().Resolve(context.Background(), "https://example.com/art.jpg")
	if err != nil || len(images) != 1 || images[0].URL != "https://example.com/art.jpg" {
		t.Fatalf("direct Resolve() = (%v, %v)", images, err)
	}
}

func TestSourceProvidersResolveSuccessfulResponses(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/photos/u1":
			if got := r.Header.Get(headerAuthorization); got != "Client-ID u-key" {
				t.Errorf("Unsplash authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"id":"u1","urls":{"raw":"https://images.example/u1?raw=1"},"links":{"download_location":"https://api.example/u1/download"}}`))
		case "/planetary/apod":
			_, _ = w.Write([]byte(`{"title":"APOD","hdurl":"https://images.nasa.gov/apod.jpg","media_type":"image"}`))
		case "/api/v1/artworks/a1":
			_, _ = w.Write([]byte(`{"data":{"id":1,"title":"Art","image_id":"iiif-1"}}`))
		case "/v1/photos/p1":
			if got := r.Header.Get(headerAuthorization); got != "p-key" {
				t.Errorf("Pexels authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"id":1,"src":{"original":"https://images.pexels.com/p1.jpg"}}`))
		case "/":
			_, _ = w.Write([]byte(`{"hits":[{"id":1,"largeImageURL":"https://pixabay.com/p1.jpg"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	unsplash := newUnsplashProvider("", "u-key", "", slog.Default())
	unsplash.client, unsplash.BaseURL = server.Client(), server.URL
	nasa := newNasaProvider("n-key", slog.Default())
	nasa.client, nasa.BaseURL = server.Client(), server.URL
	artic := newArticProvider(slog.Default())
	artic.client, artic.BaseURL, artic.IIIFBaseURL = server.Client(), server.URL, server.URL+"/iiif"
	pexels := newPexelsProvider("p-key", slog.Default())
	pexels.client, pexels.BaseURL = server.Client(), server.URL
	pixabay := newPixabayProvider("x-key", slog.Default())
	pixabay.client, pixabay.BaseURL = server.Client(), server.URL

	tests := []struct {
		provider SourceProvider
		line     string
	}{
		{unsplash, "unsplash:photo:u1"},
		{nasa, "nasa:apod"},
		{artic, "artic:photo:a1"},
		{pexels, "pexels:photo:p1"},
		{pixabay, "pixabay:photo:x1"},
	}
	for _, tc := range tests {
		t.Run(tc.provider.Name(), func(t *testing.T) {
			images, err := tc.provider.Resolve(context.Background(), tc.line)
			if err != nil || len(images) != 1 || images[0].URL == "" ||
				!strings.HasPrefix(images[0].Identity, tc.provider.Name()+"--") {
				t.Fatalf("Resolve(%q) = (%v, %v)", tc.line, images, err)
			}
		})
	}
}

func TestPexelsResolutionUsesProviderPhotoIDs(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/curated" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"photos":[
			{"id":101,"src":{"original":"https://images.pexels.com/photos/101/image.jpg"}},
			{"id":202,"src":{"original":"https://images.pexels.com/photos/202/image.jpg"}}
		]}`))
	}))
	defer server.Close()

	provider := newPexelsProvider("key", slog.Default())
	provider.client, provider.BaseURL = server.Client(), server.URL
	images, err := provider.Resolve(context.Background(), "pexels:curated")
	if err != nil || len(images) != 2 {
		t.Fatalf("Resolve() = (%v, %v)", images, err)
	}
	if images[0].Identity == images[1].Identity ||
		!strings.Contains(images[0].Identity, "101") || !strings.Contains(images[1].Identity, "202") {
		t.Fatalf("Pexels identities are not provider-owned IDs: %q / %q", images[0].Identity, images[1].Identity)
	}
}

func TestFetchProviderJSONContract(t *testing.T) {
	t.Parallel()

	t.Run("decodes bounded response with headers", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get(headerAuthorization); got != "token" {
				t.Errorf("Authorization = %q, want token", got)
			}
			_, _ = w.Write([]byte(`{"name":"art"}`))
		}))
		defer server.Close()

		var got struct {
			Name string `json:"name"`
		}
		err := fetchProviderJSON(context.Background(), providerJSONRequest{
			client: server.Client(), url: server.URL,
			headers: map[string]string{headerAuthorization: "token"}, maxBytes: 1024,
			statusLabel: "provider", decodeLabel: "provider",
		}, &got)
		if err != nil || got.Name != "art" {
			t.Fatalf("fetchProviderJSON() = (%+v, %v), want art", got, err)
		}
	})

	t.Run("returns status error", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		err := fetchProviderJSON(context.Background(), providerJSONRequest{
			client: server.Client(), url: server.URL, maxBytes: 1024,
			statusLabel: "provider", decodeLabel: "provider",
		}, &struct{}{})
		if err == nil || !strings.Contains(err.Error(), "provider error: 429") {
			t.Fatalf("fetchProviderJSON() error = %v, want status error", err)
		}
	})

	t.Run("returns decode error", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not-json"))
		}))
		defer server.Close()

		err := fetchProviderJSON(context.Background(), providerJSONRequest{
			client: server.Client(), url: server.URL, maxBytes: 1024,
			statusLabel: "provider", decodeLabel: "provider",
		}, &struct{}{})
		if err == nil || !strings.Contains(err.Error(), "decode provider response") {
			t.Fatalf("fetchProviderJSON() error = %v, want decode error", err)
		}
	})

	t.Run("wraps transport error", func(t *testing.T) {
		t.Parallel()
		wantErr := errors.New("offline")
		client := &http.Client{Transport: errorRoundTripper{err: wantErr}}
		err := fetchProviderJSON(context.Background(), providerJSONRequest{
			client: client, url: "https://provider.invalid", maxBytes: 1024,
			statusLabel: "provider", decodeLabel: "provider",
		}, &struct{}{})
		if err == nil || errors.Is(err, wantErr) {
			t.Fatalf("fetchProviderJSON() error = %v, want sanitized transport failure", err)
		}
	})
}

type errorRoundTripper struct {
	err error
}

func (r errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, r.err
}
