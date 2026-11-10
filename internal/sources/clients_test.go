package sources

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

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

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestNASAResolveAPODSkipsNonImageDaysAndPrefersHD(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		status        int
		response      apodResponse
		wantURL       string
		wantFallbacks []string
		wantLicense   string
		wantNoImages  bool
		wantErr       string
	}{
		{
			name: "HD image with standard fallback", status: http.StatusOK,
			response: apodResponse{Date: "2024-01-02", Title: "Nebula", URL: "https://apod.example/a.jpg", HDURL: "https://apod.example/hd.jpg", Type: mediaTypeImage},
			wantURL:  "https://apod.example/hd.jpg", wantFallbacks: []string{"https://apod.example/a.jpg"}, wantLicense: nasaPublicDomain,
		},
		{
			name: "standard image without HD", status: http.StatusOK,
			response: apodResponse{URL: "https://apod.example/a.png", Type: mediaTypeImage, Copyright: "\nJane  Doe\n"},
			wantURL:  "https://apod.example/a.png", wantLicense: "Copyright Jane Doe",
		},
		{name: "video day is skipped", status: http.StatusOK, response: apodResponse{URL: "https://apod.example/v.mp4", Type: "video"}, wantNoImages: true},
		{name: "other media is skipped", status: http.StatusOK, response: apodResponse{URL: "https://apod.example/page.html", Type: "other"}, wantNoImages: true},
		{
			name: "image type without a JPEG or PNG is skipped", status: http.StatusOK,
			response: apodResponse{URL: "https://www.youtube.com/embed/x", Type: mediaTypeImage}, wantNoImages: true,
		},
		{name: "API failure", status: http.StatusInternalServerError, wantErr: "nasa apod api error"},
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
			provider := newNasaProvider("test_key_1", discardLogger())
			provider.BaseURL = server.URL

			images, err := provider.Resolve(context.Background(), "nasa:apod")
			switch {
			case test.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Resolve(nasa:apod) error = %v, want %q", err, test.wantErr)
				}
			case test.wantNoImages:
				if err != nil || images == nil || len(images) != 0 {
					t.Fatalf("Resolve(nasa:apod) = (%v, %v), want an empty successful resolution", images, err)
				}
			default:
				if err != nil || len(images) != 1 || images[0].URL != test.wantURL ||
					strings.Join(images[0].Fallbacks, ",") != strings.Join(test.wantFallbacks, ",") ||
					!strings.HasPrefix(images[0].Identity, "nasa--") || images[0].Attribution.License != test.wantLicense {
					t.Fatalf("Resolve(nasa:apod) = %+v, %v; want %s", images, err, test.wantURL)
				}
			}
		})
	}
}

func TestAPODPageURL(t *testing.T) {
	t.Parallel()
	if got := apodPageURL("2024-01-02"); got != "https://apod.nasa.gov/apod/ap240102.html" {
		t.Fatalf("apodPageURL() = %q", got)
	}
	if got := apodPageURL("not-a-date"); got != "" {
		t.Fatalf("apodPageURL(malformed) = %q, want empty", got)
	}
}

func TestNASASearchSkipsFailedManifestsAndPrefersOriginal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		manifests map[string]string // path -> response body ("" means HTTP 500)
		wantURLs  []string
		wantErr   bool
	}{
		{
			name: "one manifest failure keeps the rest",
			manifests: map[string]string{
				"/m1": `["https://assets.example/a~thumb.jpg","https://assets.example/a~large.jpg","https://assets.example/a~orig.jpg"]`,
				"/m2": "",
				"/m3": `["https://assets.example/c~small.jpg","https://assets.example/c~large.jpg"]`,
			},
			wantURLs: []string{"https://assets.example/a~orig.jpg", "https://assets.example/c~large.jpg"},
		},
		{
			name:      "item without usable rendition is skipped",
			manifests: map[string]string{"/m1": `["https://assets.example/a~orig.tif","https://assets.example/metadata.json"]`},
			wantURLs:  []string{},
		},
		{name: "every manifest failing is an error", manifests: map[string]string{"/m1": "", "/m2": "{bad"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var server *httptest.Server
			server = newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/search" {
					if r.URL.Query().Get("q") != "mars rover" || r.URL.Query().Get("media_type") != mediaTypeImage {
						t.Errorf("unexpected search query %q", r.URL.RawQuery)
					}
					var items []map[string]any
					for _, path := range []string{"/m1", "/m2", "/m3"} {
						if _, ok := test.manifests[path]; ok {
							items = append(items, map[string]any{
								"href": server.URL + path,
								"data": []map[string]string{{"nasa_id": "id" + path[2:], "title": "Title " + path, "center": "JPL"}},
							})
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"collection": map[string]any{"items": items}})
					return
				}
				body := test.manifests[r.URL.Path]
				if body == "" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_, _ = w.Write([]byte(body))
			}))
			provider := newNasaProvider("key", discardLogger())
			provider.SearchURL = server.URL

			images, err := provider.Resolve(context.Background(), "nasa:search:mars rover")
			if test.wantErr {
				if err == nil {
					t.Fatalf("Resolve() = %v, want error when every manifest failed", images)
				}
				return
			}
			if err != nil || len(images) != len(test.wantURLs) {
				t.Fatalf("Resolve() = (%+v, %v), want %v", images, err, test.wantURLs)
			}
			for index, image := range images {
				if image.URL != test.wantURLs[index] {
					t.Errorf("image %d URL = %q, want %q", index, image.URL, test.wantURLs[index])
				}
				if image.Attribution.Artist != "JPL" || !strings.HasPrefix(image.Attribution.SourceURL, "https://images.nasa.gov/details/") {
					t.Errorf("image %d attribution = %+v", index, image.Attribution)
				}
			}
			if len(images) > 0 && strings.Join(images[0].Fallbacks, ",") != "https://assets.example/a~large.jpg" {
				t.Errorf("original rendition fallbacks = %v, want large rendition", images[0].Fallbacks)
			}
		})
	}
}

func TestNASAResolveRejectsMalformedLines(t *testing.T) {
	t.Parallel()
	provider := newNasaProvider("key", discardLogger())
	for line, want := range map[string]string{"nasa": "invalid nasa format", "nasa:search": "requires a query", "nasa:other": "unknown nasa type"} {
		if _, err := provider.Resolve(context.Background(), line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want %q", line, err, want)
		}
	}
}

func TestArticResolveRequestsPublicDomainRenditionsSizedByAspect(t *testing.T) {
	t.Parallel()
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/artworks/search":
			if r.URL.Query().Get("query[term][is_public_domain]") != "true" || r.URL.Query().Get("q") != "monet" {
				t.Errorf("search is not limited to public-domain works: %q", r.URL.RawQuery)
			}
			if r.Header.Get("AIC-User-Agent") == "" {
				t.Error("missing AIC-User-Agent header")
			}
			_, _ = w.Write([]byte(`{"data":[
				{"id":1,"title":"Wide","image_id":"wide","is_public_domain":true,"artist_display":"Claude Monet\nFrench","thumbnail":{"width":8000,"height":3000}},
				{"id":2,"title":"Portrait","image_id":"tall","is_public_domain":true,"thumbnail":{"width":4000,"height":6000}},
				{"id":3,"title":"Small","image_id":"small","is_public_domain":true,"thumbnail":{"width":1500,"height":1000}},
				{"id":4,"title":"Restricted","image_id":"restricted","is_public_domain":false},
				{"id":5,"title":"No image","is_public_domain":true}
			]}`))
		case "/api/v1/artworks/7":
			_, _ = w.Write([]byte(`{"data":{"id":7,"title":"Recent","image_id":"img7","is_public_domain":false}}`))
		case "/api/v1/artworks/8":
			_, _ = w.Write([]byte(`{"data":{"id":8,"title":"Unknown size","image_id":"img8","is_public_domain":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	provider := newArticProvider(discardLogger())
	provider.BaseURL, provider.IIIFBaseURL = server.URL, "https://iiif.example/iiif/2"

	images, err := provider.Resolve(context.Background(), "artic:search:monet")
	if err != nil || len(images) != 3 {
		t.Fatalf("Resolve(search) = (%+v, %v), want three public-domain images", images, err)
	}
	want := []struct{ url, firstFallback string }{
		{"https://iiif.example/iiif/2/wide/full/,3000/0/default.jpg", "https://iiif.example/iiif/2/wide/full/,1686/0/default.jpg"},
		{"https://iiif.example/iiif/2/tall/full/3000,/0/default.jpg", "https://iiif.example/iiif/2/tall/full/1686,/0/default.jpg"},
		{"https://iiif.example/iiif/2/small/full/1200,/0/default.jpg", "https://iiif.example/iiif/2/small/full/843,/0/default.jpg"},
	}
	for index, image := range images {
		if image.URL != want[index].url || len(image.Fallbacks) == 0 || image.Fallbacks[0] != want[index].firstFallback {
			t.Errorf("image %d = %s %v, want %s then %s", index, image.URL, image.Fallbacks, want[index].url, want[index].firstFallback)
		}
	}
	legacy := sourceURLIdentity("artic", "https://iiif.example/iiif/2/wide/full/!3840,2160/0/default.jpg")
	if images[0].Identity != legacy {
		t.Errorf("identity = %q, want the stable pre-sizing identity %q", images[0].Identity, legacy)
	}
	if images[0].Attribution.Artist != "Claude Monet French" || images[0].Attribution.SourceURL != "https://www.artic.edu/artworks/1" {
		t.Errorf("attribution = %+v", images[0].Attribution)
	}

	if _, err := provider.Resolve(context.Background(), "artic:photo:7"); err == nil || !strings.Contains(err.Error(), "public domain") {
		t.Fatalf("Resolve(non-public-domain photo) error = %v", err)
	}
	unknown, err := provider.Resolve(context.Background(), "art_institute_of_chicago:photo:8")
	if err != nil || len(unknown) != 1 || !strings.HasSuffix(unknown[0].URL, "/img8/full/3000,/0/default.jpg") || len(unknown[0].Fallbacks) != 3 {
		t.Fatalf("Resolve(photo without dimensions) = (%+v, %v)", unknown, err)
	}
}

func TestArticImageCandidatesRespectPixelCap(t *testing.T) {
	t.Parallel()
	provider := newArticProvider(discardLogger())
	// A 10:1 panorama sized to 3000 tall would exceed the 40 MP download cap.
	candidates := provider.imageCandidates("pano", 40000, 4000)
	if len(candidates) == 0 || !strings.Contains(candidates[0], "/full/,1686/") {
		t.Fatalf("imageCandidates(panorama) = %v, want the largest size within the pixel cap", candidates)
	}
	if got := provider.imageCandidates("tiny", 500, 400); len(got) != 0 {
		t.Fatalf("imageCandidates(tiny) = %v, want none below the smallest allowed size", got)
	}
}

func TestPexelsResolveRequestsCoveringRenditionAndSkipsEmptySources(t *testing.T) {
	t.Parallel()
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(headerAuthorization) != "p-key" {
			t.Errorf("Pexels authorization = %q", r.Header.Get(headerAuthorization))
		}
		switch r.URL.Path {
		case "/v1/search":
			if r.URL.Query().Get("query") != "red fox" || r.URL.Query().Get("per_page") != "10" {
				t.Errorf("unexpected search query %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"photos":[
				{"id":1,"width":6000,"height":4000,"url":"https://www.pexels.com/photo/1/","photographer":"Ann","alt":"Fox",
				 "src":{"original":"https://images.pexels.com/photos/1/a.jpeg","large2x":"https://images.pexels.com/photos/1/a.jpeg?w=940"}},
				{"id":2,"width":8000,"height":2000,"src":{"original":"https://images.pexels.com/photos/2/b.jpeg"}},
				{"id":3,"src":{"original":""}}
			]}`))
		case "/v1/curated", "/v1/collections/c1":
			_, _ = w.Write([]byte(`{"photos":[],"media":[{"id":4,"type":"Photo","src":{"original":"https://images.pexels.com/photos/4/d.jpeg"}},{"id":5,"type":"Video"}]}`))
		case "/v1/photos/9":
			_, _ = w.Write([]byte(`{"id":9,"src":{"original":""}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	provider := newPexelsProvider("p-key", discardLogger())
	provider.BaseURL = server.URL

	images, err := provider.Resolve(context.Background(), "pexels:search:red fox")
	if err != nil || len(images) != 2 {
		t.Fatalf("Resolve(search) = (%+v, %v), want two photos with originals", images, err)
	}
	first, _ := url.Parse(images[0].URL)
	if first.Query().Get("w") != "3840" || first.Query().Get("cs") != "tinysrgb" || first.Query().Has("h") {
		t.Errorf("3:2 photo rendition = %s, want width 3840", images[0].URL)
	}
	if images[0].Identity != sourceURLIdentity(providerPexels, "https://images.pexels.com/photos/1/a.jpeg") {
		t.Errorf("identity %q is not derived from the original URL", images[0].Identity)
	}
	if len(images[0].Fallbacks) != 1 || images[0].Attribution != (attribution{
		Provider: providerPexels, Title: "Fox", Artist: "Ann", SourceURL: "https://www.pexels.com/photo/1/", License: pexelsLicense,
	}) {
		t.Errorf("image = %+v", images[0])
	}
	if wide, _ := url.Parse(images[1].URL); wide.Query().Get("h") != "2160" || wide.Query().Has("w") {
		t.Errorf("4:1 photo rendition = %s, want height 2160", images[1].URL)
	}

	collection, err := provider.Resolve(context.Background(), "pexels:collection:c1")
	if err != nil || len(collection) != 1 {
		t.Fatalf("Resolve(collection) = (%+v, %v), want videos skipped", collection, err)
	}
	if curated, err := provider.Resolve(context.Background(), "pexels:curated"); err != nil || len(curated) != 0 {
		t.Fatalf("Resolve(curated) = (%+v, %v)", curated, err)
	}
	if _, err := provider.Resolve(context.Background(), "pexels:photo:9"); err == nil || !strings.Contains(err.Error(), "no downloadable image") {
		t.Fatalf("Resolve(photo without original) error = %v", err)
	}
}

func TestPexelsResolveRejectsMalformedLines(t *testing.T) {
	t.Parallel()
	provider := newPexelsProvider("key", discardLogger())
	for line, want := range map[string]string{
		"pexels": "invalid pexels format", "pexels:search": "requires a query", "pexels:collection": "requires an ID",
		"pexels:photo": "requires an ID", "pexels:other": "unknown pexels type",
	} {
		if _, err := provider.Resolve(context.Background(), line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want %q", line, err, want)
		}
	}
}

func TestCoverRendition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, height int
		axis          string
		size          int
	}{
		{name: "unknown", axis: axisWidth, size: targetArtworkWidth},
		{name: "exact 16:9", width: 1920, height: 1080, axis: axisWidth, size: targetArtworkWidth},
		{name: "portrait", width: 2000, height: 3000, axis: axisWidth, size: targetArtworkWidth},
		{name: "panorama", width: 9000, height: 3000, axis: axisHeight, size: targetArtworkHeight},
		{name: "extreme portrait capped", width: 1000, height: 4000, axis: axisWidth, size: 3162},
	}
	for _, test := range tests {
		axis, size := coverRendition(test.width, test.height)
		if axis != test.axis || size != test.size {
			t.Errorf("%s: coverRendition(%d, %d) = (%s, %d), want (%s, %d)", test.name, test.width, test.height, axis, size, test.axis, test.size)
		}
	}
	if _, err := withQuery("ftp://example.com/a.jpg", nil); err == nil {
		t.Error("withQuery accepted a non-HTTP URL")
	}
	if _, err := withQuery("http://[::1", nil); err == nil {
		t.Error("withQuery accepted a malformed URL")
	}
}
