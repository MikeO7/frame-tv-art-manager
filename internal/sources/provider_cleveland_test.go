package sources

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const clevelandSearchBody = `{"data":[
	{"id":10,"title":"Snow Landscape","creation_date":"mid-1770s","url":"https://clevelandart.org/art/1997.111","share_license_status":"CC0",
	 "creators":[{"description":"Yosa Buson (Japanese, 1716–1783)"}],
	 "images":{"web":{"url":"https://cdn.example/10_web.jpg","width":"362","height":"900"},
	           "print":{"url":"https://cdn.example/10_print.jpg","width":"1366","height":"3400"},
	           "full":{"url":"https://cdn.example/10_full.tif","width":"6133","height":"15267"}}},
	{"id":11,"title":"Wide","share_license_status":"CC0",
	 "images":{"print":{"url":"https://cdn.example/11_print.jpg","width":"3400","height":"1900"},
	           "full":{"url":"https://cdn.example/11_full.jpg","width":"6000","height":"3400"}}},
	{"id":12,"title":"Copyrighted","share_license_status":"Copyrighted",
	 "images":{"print":{"url":"https://cdn.example/12_print.jpg","width":"3400","height":"2000"}}},
	{"id":13,"title":"Tiny","share_license_status":"CC0",
	 "images":{"web":{"url":"https://cdn.example/13_web.jpg","width":"900","height":"600"}}},
	{"id":14,"title":"Malformed","share_license_status":"CC0",
	 "images":{"print":{"url":"https://cdn.example/14_print.jpg","width":"wide","height":""}}}
]}`

func TestClevelandResolvePrefersPanelSizedCC0Renditions(t *testing.T) {
	t.Parallel()
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/artworks/":
			query := r.URL.Query()
			if query.Get("cc0") != "1" || query.Get("has_image") != "1" || query.Get("q") != "snow" {
				t.Errorf("cleveland search is not limited to CC0 works with images: %q", r.URL.RawQuery)
			}
			// The source line asks for twice the ten works it resolves.
			if query.Get("limit") == "20" && query.Get("type") != "Painting" {
				t.Errorf("cleveland search dropped the type filter: %q", r.URL.RawQuery)
			}
			if !strings.Contains(query.Get("fields"), "images") {
				t.Errorf("cleveland search does not request image fields: %q", query.Get("fields"))
			}
			_, _ = w.Write([]byte(clevelandSearchBody))
		case "/api/artworks/1997.111":
			_, _ = w.Write([]byte(`{"data":{"id":10,"title":"Snow Landscape","share_license_status":"CC0",
				"images":{"print":{"url":"https://cdn.example/10_print.jpg","width":"1366","height":"3400"}}}}`))
		case "/api/artworks/12":
			_, _ = w.Write([]byte(`{"data":{"id":12,"share_license_status":"Copyrighted"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	provider := newClevelandProvider(discardLogger())
	provider.BaseURL = server.URL

	images, err := provider.Resolve(context.Background(), "cleveland:search:snow?type=Painting")
	if err != nil || len(images) != 2 {
		t.Fatalf("Resolve(search) = (%+v, %v), want the two CC0 works with usable renditions", images, err)
	}
	if images[0].URL != "https://cdn.example/10_print.jpg" || len(images[0].Fallbacks) != 0 {
		t.Errorf("portrait renditions = %s %v, want the print JPEG only (TIFF and small web skipped)", images[0].URL, images[0].Fallbacks)
	}
	if images[1].URL != "https://cdn.example/11_full.jpg" || strings.Join(images[1].Fallbacks, ",") != "https://cdn.example/11_print.jpg" {
		t.Errorf("wide renditions = %s %v, want the covering full JPEG then print", images[1].URL, images[1].Fallbacks)
	}
	want := attribution{
		Provider: providerCleveland, Title: "Snow Landscape", Artist: "Yosa Buson (Japanese, 1716–1783)",
		SourceURL: "https://clevelandart.org/art/1997.111", License: publicDomainCC0,
	}
	if images[0].Attribution != want || images[0].Identity != museumObjectIdentity(providerCleveland, "10") {
		t.Errorf("image = %+v", images[0])
	}
	if images[1].Attribution.SourceURL != "https://clevelandart.org/art/11" {
		t.Errorf("default link = %q", images[1].Attribution.SourceURL)
	}

	single, err := provider.Resolve(context.Background(), "cleveland:object:1997.111")
	if err != nil || len(single) != 1 || single[0].Identity != images[0].Identity {
		t.Fatalf("Resolve(object by accession number) = (%+v, %v)", single, err)
	}
	if _, err := provider.Resolve(context.Background(), "cleveland:object:12"); err == nil || !strings.Contains(err.Error(), "not CC0") {
		t.Errorf("Resolve(copyrighted object) error = %v", err)
	}

	candidates, err := provider.searchCandidates(context.Background(), "snow", 1)
	if err != nil || len(candidates) != 1 || candidates[0].Width != 1366 || candidates[0].Height != 3400 ||
		candidates[0].Date != "mid-1770s" || candidates[0].SourceLine != "cleveland:object:10" {
		t.Fatalf("searchCandidates(limit 1) = (%+v, %v)", candidates, err)
	}
}

func TestClevelandResolveRejectsMalformedLines(t *testing.T) {
	t.Parallel()
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	provider := newClevelandProvider(discardLogger())
	provider.BaseURL = server.URL
	for line, want := range map[string]string{
		"cleveland:search":            "invalid cleveland format",
		"cleveland:search:x?cc0=0":    `filter "cc0" is not supported`,
		"cleveland:other:x":           "unknown cleveland type",
		"cleveland:search:snow":       "cleveland api error: 502",
		"cleveland:object:1997.111":   "cleveland api error: 502",
		"cleveland:search:x?type=%zz": "parse search filters",
	} {
		if _, err := provider.Resolve(context.Background(), line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want %q", line, err, want)
		}
	}
}
