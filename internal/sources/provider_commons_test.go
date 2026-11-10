package sources

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

const commonsPagesBody = `{"batchcomplete":true,"query":{"pages":[
	{"title":"File:Second.jpg","index":2,"imageinfo":[{"url":"https://upload.example/b/Second.jpg","descriptionurl":"https://commons.example/wiki/File:Second.jpg",
	  "thumburl":"https://upload.example/b/Second.jpg","thumbwidth":3000,"thumbheight":2000,"width":3000,"height":2000,"mime":"image/png",
	  "extmetadata":{"LicenseShortName":{"value":"Public domain"},"Credit":{"value":"<span>Own work</span>"}}}]},
	{"title":"File:Wombat.jpg","index":1,"imageinfo":[{"url":"https://upload.example/a/Wombat.jpg","descriptionurl":"https://commons.example/wiki/File:Wombat.jpg",
	  "thumburl":"https://thumb.example/a/Wombat.jpg/3840px-Wombat.jpg?utm_source=x","thumbwidth":3840,"thumbheight":3341,"width":4012,"height":3491,
	  "mime":"image/jpeg","extmetadata":{"ObjectName":{"value":"Common wombat"},
	  "Artist":{"value":"<bdi><a href=\"https://www.wikidata.org/wiki/Q1\">Charles J. Sharp</a> &amp; friends</bdi>"},
	  "LicenseShortName":{"value":"CC BY-SA 4.0"}}}]},
	{"title":"File:Unlicensed.jpg","index":3,"imageinfo":[{"url":"https://upload.example/c/Unlicensed.jpg","width":5000,"height":4000,"mime":"image/jpeg"}]},
	{"title":"File:Small.jpg","index":4,"imageinfo":[{"url":"https://upload.example/d/Small.jpg","width":1200,"height":800,"mime":"image/jpeg",
	  "extmetadata":{"LicenseShortName":{"value":"CC0"}}}]},
	{"title":"File:Diagram.svg","index":5,"imageinfo":[{"url":"https://upload.example/e/Diagram.svg","thumburl":"https://thumb.example/e/3840px-Diagram.svg.png",
	  "width":4000,"height":4000,"mime":"image/svg+xml","extmetadata":{"LicenseShortName":{"value":"CC0"}}}]},
	{"title":"File:Missing.jpg","index":6}
]}}`

func TestCommonsResolveRecordsLicenseAndRequestsStandardThumbnails(t *testing.T) {
	t.Parallel()
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("action") != "query" || query.Get("iiurlwidth") != "3840" || query.Get("formatversion") != "2" ||
			!strings.Contains(query.Get("iiprop"), "extmetadata") {
			t.Errorf("unexpected imageinfo query %q", r.URL.RawQuery)
		}
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("User-Agent = %q, want the descriptive manager agent", r.Header.Get("User-Agent"))
		}
		switch query.Get("generator") {
		case "images":
			if query.Get("titles") != "Template:Potd/2026-11-07" {
				t.Errorf("potd template = %q", query.Get("titles"))
			}
		case "categorymembers":
			if query.Get("gcmtitle") != "Category:"+commonsFeaturedCategory || query.Get("gcmsort") != "timestamp" || query.Get("gcmdir") != "desc" {
				t.Errorf("featured query = %q", r.URL.RawQuery)
			}
		case "search":
			if query.Get("gsrsearch") != "incategory:"+commonsFeaturedCategory+" snow" || query.Get("gsrnamespace") != "6" {
				t.Errorf("search query = %q", r.URL.RawQuery)
			}
		default:
			t.Errorf("unexpected generator %q", query.Get("generator"))
		}
		_, _ = w.Write([]byte(commonsPagesBody))
	}))
	provider := newCommonsProvider(discardLogger())
	provider.BaseURL, provider.gate = server.URL, newRateGate()
	// The local date is still November 6, but the Picture of the Day follows UTC.
	provider.now = func() time.Time { return time.Date(2026, 11, 6, 20, 0, 0, 0, time.FixedZone("UTC-7", -7*3600)) }

	for _, line := range []string{"commons:potd", "commons:featured", "commons:search: snow "} {
		images, err := provider.Resolve(context.Background(), line)
		if err != nil || len(images) != 2 {
			t.Fatalf("Resolve(%s) = (%+v, %v), want the two licensed, large JPEG or PNG files", line, images, err)
		}
		wombat := images[0]
		if wombat.URL != "https://thumb.example/a/Wombat.jpg/3840px-Wombat.jpg?utm_source=x" ||
			strings.Join(wombat.Fallbacks, ",") != "https://thumb.example/a/Wombat.jpg/1920px-Wombat.jpg?utm_source=x" {
			t.Errorf("%s: renditions = %s %v, want the 3840 then 1920 thumbnails", line, wombat.URL, wombat.Fallbacks)
		}
		want := Attribution{
			Provider: providerCommons, Title: "Common wombat", Artist: "Charles J. Sharp & friends",
			SourceURL: "https://commons.example/wiki/File:Wombat.jpg", License: "CC BY-SA 4.0",
		}
		if wombat.Attribution != want || wombat.Identity != sourceIdentity(providerCommons, "File:Wombat.jpg", "Wombat.jpg") {
			t.Errorf("%s: wombat = %+v", line, wombat)
		}
		second := images[1]
		if second.URL != "https://upload.example/b/Second.jpg" || len(second.Fallbacks) != 0 ||
			second.Attribution.Title != "Second" || second.Attribution.Artist != "Own work" {
			t.Errorf("%s: original-size file = %+v", line, second)
		}
	}
}

func TestCommonsResolveFailures(t *testing.T) {
	t.Parallel()
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("generator") == "search" {
			_, _ = w.Write([]byte(`{"error":{"code":"badvalue","info":"Unrecognized value"}}`))
			return
		}
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	provider := newCommonsProvider(discardLogger())
	provider.BaseURL, provider.gate = server.URL, newRateGate()
	// The rate-limited request goes last: it closes the provider's gate, which
	// then answers every later request without reaching the server.
	for _, test := range []struct{ line, want string }{
		{"commons", "invalid commons format"},
		{"commons:search", "requires a query"},
		{"commons:search:  ", "requires a query"},
		{"commons:other", "unknown commons type"},
		{"commons:search:snow", "commons api error badvalue: Unrecognized value"},
		{"commons:featured", "commons rate limit reached"},
	} {
		if _, err := provider.Resolve(context.Background(), test.line); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("Resolve(%q) error = %v, want %q", test.line, err, test.want)
		}
	}
}
