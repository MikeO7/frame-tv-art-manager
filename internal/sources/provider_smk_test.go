package sources

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const smkSearchBody = `{"items":[
	{"object_number":"KMS1","public_domain":true,"has_image":true,"image_iiif_id":"https://iiif.example/jp2/kms1.jp2",
	 "image_native":"https://api.example/download/x/KMS1.jpg","image_width":7174,"image_height":5536,
	 "frontend_url":"https://open.smk.dk/artwork/image/KMS1","titles":[{"title":"The Fall of the Titans"}],
	 "artist":["Cornelis Cornelisz. van Haarlem"],"production_date":[{"period":"1588-1590"}]},
	{"object_number":"KMS2","public_domain":true,"image_iiif_id":"https://iiif.example/jp2/kms2.jp2/","image_width":12000,"image_height":3000},
	{"object_number":"KMS3","public_domain":true,"image_iiif_id":"https://iiif.example/jp2/kms3.jp2","image_width":2400,"image_height":3000},
	{"object_number":"KMS4","public_domain":false,"image_iiif_id":"https://iiif.example/jp2/kms4.jp2","image_width":8000,"image_height":6000},
	{"object_number":"KMS5","public_domain":true,"image_iiif_id":"https://iiif.example/jp2/kms5.jp2","image_width":1000,"image_height":800},
	{"object_number":"KMS6","public_domain":true}
]}`

func TestSMKResolveRequestsPanelSizedIIIFRenditions(t *testing.T) {
	t.Parallel()
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch r.URL.Path {
		case "/art/search/":
			if query.Get("keys") != "hammershøi" || query.Get("filters") != "[public_domain:true],[has_image:true]" || query.Get("lang") != "en" {
				t.Errorf("smk search is not limited to public-domain works with images: %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(smkSearchBody))
		case "/art/":
			if query.Get("object_number") == "KMS1" {
				_, _ = w.Write([]byte(`{"items":[{"object_number":"KMS1","public_domain":true,
					"image_iiif_id":"https://iiif.example/jp2/kms1.jp2","image_width":7174,"image_height":5536}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	provider := newSMKProvider(discardLogger())
	provider.BaseURL, provider.gate = server.URL, newRateGate()

	images, err := provider.Resolve(context.Background(), "smk:search:hammershøi")
	if err != nil || len(images) != 3 {
		t.Fatalf("Resolve(search) = (%+v, %v), want three public-domain works large enough for the panel", images, err)
	}
	tests := []struct {
		url       string
		fallbacks []string
	}{
		{"https://iiif.example/jp2/kms1.jp2/full/3840,/0/default.jpg", []string{
			"https://api.example/download/x/KMS1.jpg", "https://iiif.example/jp2/kms1.jp2/full/1920,/0/default.jpg",
		}},
		{"https://iiif.example/jp2/kms2.jp2/full/,2160/0/default.jpg", []string{"https://iiif.example/jp2/kms2.jp2/full/,1080/0/default.jpg"}},
		{"https://iiif.example/jp2/kms3.jp2/full/2400,/0/default.jpg", nil},
	}
	for index, test := range tests {
		if images[index].URL != test.url || strings.Join(images[index].Fallbacks, " ") != strings.Join(test.fallbacks, " ") {
			t.Errorf("image %d = %s %v, want %s %v", index, images[index].URL, images[index].Fallbacks, test.url, test.fallbacks)
		}
	}
	want := attribution{
		Provider: providerSMK, Title: "The Fall of the Titans", Artist: "Cornelis Cornelisz. van Haarlem",
		SourceURL: "https://open.smk.dk/artwork/image/KMS1", License: smkLicense,
	}
	if images[0].Attribution != want || images[0].Identity != museumObjectIdentity(providerSMK, "KMS1") {
		t.Errorf("image = %+v", images[0])
	}
	if images[1].Attribution.SourceURL != "https://open.smk.dk/artwork/image/KMS2" {
		t.Errorf("default link = %q", images[1].Attribution.SourceURL)
	}

	single, err := provider.Resolve(context.Background(), "smk:object:KMS1")
	if err != nil || len(single) != 1 || single[0].Identity != images[0].Identity {
		t.Fatalf("Resolve(object) = (%+v, %v)", single, err)
	}
	if _, err := provider.Resolve(context.Background(), "smk:object:MISSING"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Resolve(missing object) error = %v", err)
	}

	candidates, err := provider.searchCandidates(context.Background(), "hammershøi", 1)
	if err != nil || len(candidates) != 1 || candidates[0].Width != 3840 || candidates[0].Height != 2963 ||
		candidates[0].Date != "1588-1590" || candidates[0].SourceLine != "smk:object:KMS1" {
		t.Fatalf("searchCandidates(limit 1) = (%+v, %v)", candidates, err)
	}
}

func TestSMKResolveRejectsMalformedLines(t *testing.T) {
	t.Parallel()
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/art/" {
			_, _ = w.Write([]byte(`{"items":[{"object_number":"KMS4","public_domain":false}]}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	provider := newSMKProvider(discardLogger())
	provider.BaseURL, provider.gate = server.URL, newRateGate()
	for line, want := range map[string]string{
		"smk:search":             "invalid smk format",
		"smk:search:x?creator=a": "takes no filters",
		"smk:other:x":            "unknown smk type",
		"smk:search:hammershoi":  "smk api error: 503",
		"smk:object:KMS4":        "not public domain",
		"smk:search:   ":         "query or at least one filter",
	} {
		if _, err := provider.Resolve(context.Background(), line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want %q", line, err, want)
		}
	}
}
