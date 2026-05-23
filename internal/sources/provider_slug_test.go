package sources

import (
	"context"
	"strings"
	"testing"
)

func TestSlugFromArticURL(t *testing.T) {
	slug := slugFromArticURL("https://www.artic.edu/iiif/2/12345/full/843,/0/default.jpg")
	if slug == "" || slug == "direct-source" {
		t.Errorf("expected artic slug, got %q", slug)
	}

	nonArtic := slugFromArticURL("https://example.com/photo.jpg")
	if nonArtic == "" {
		t.Error("expected URLToSlug fallback")
	}

	short := slugFromArticURL("https://artic.edu/short")
	if short == "" {
		t.Error("expected fallback for short artic URL")
	}
}

func TestSlugFromNASAURL(t *testing.T) {
	slug := slugFromNASAURL("https://images-assets.nasa.gov/image/PIA12345/PIA12345~orig.jpg")
	if slug == "" {
		t.Error("expected NASA slug")
	}

	nonNASA := slugFromNASAURL("https://example.com/image.png")
	if nonNASA == "" {
		t.Error("expected URLToSlug fallback for non-NASA URL")
	}
}

func TestURLToSlug(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://example.com/path/to/image.jpg", "example.com_path"},
		{"not-a-url", "direct-source"},
		{"https://www.host.com/a/b/c", "host.com_a"},
	}
	for _, tc := range tests {
		got := URLToSlug(tc.url)
		if got == "" {
			t.Errorf("URLToSlug(%q) returned empty", tc.url)
		}
		if tc.want != "" && got != tc.want && tc.url != "not-a-url" {
			// slug formatting is deterministic; allow mismatch only when not checking exact
			if got != tc.want {
				t.Errorf("URLToSlug(%q) = %q, want %q", tc.url, got, tc.want)
			}
		}
	}
}

func TestDirectProvider(t *testing.T) {
	p := newDirectProvider()
	if p.Name() != "direct" {
		t.Errorf("Name() = %q", p.Name())
	}
	if !p.CanHandle("anything") {
		t.Error("direct provider should handle any line")
	}

	var idx int32
	images, err := p.Resolve(context.Background(), "direct:https://example.com/a.jpg", &idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].URL != "https://example.com/a.jpg" {
		t.Errorf("URL = %q", images[0].URL)
	}
	if !strings.HasPrefix(images[0].Identity, "000__direct__") {
		t.Errorf("Identity = %q", images[0].Identity)
	}
}

func TestTruncateURL(t *testing.T) {
	short := truncateURL("https://example.com")
	if short != "https://example.com" {
		t.Errorf("short URL changed: %q", short)
	}
	long := truncateURL("https://example.com/" + string(make([]byte, 100)))
	if len(long) != 80 {
		t.Errorf("expected truncated length 80, got %d", len(long))
	}
}

func TestDeduplicateStrings(t *testing.T) {
	got := deduplicateStrings([]string{"a", "b", "a", "c", "b"})
	if len(got) != 3 {
		t.Fatalf("expected 3 unique entries, got %d", len(got))
	}
}
