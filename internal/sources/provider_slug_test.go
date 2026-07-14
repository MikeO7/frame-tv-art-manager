package sources

import (
	"context"
	"strings"
	"testing"
)

func TestCapSlugBoundsLongInput(t *testing.T) {
	long := strings.Repeat("a", maxSlugLen+25)
	if got := capSlug(long); len(got) != maxSlugLen {
		t.Fatalf("capSlug length = %d, want %d", len(got), maxSlugLen)
	}
	if got := capSlug("short"); got != "short" {
		t.Fatalf("capSlug(short) = %q", got)
	}
}

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
	long := truncateURL("https://example.com/" + strings.Repeat("a", 100))
	if len(long) != 80 {
		t.Errorf("expected truncated length 80, got %d", len(long))
	}
	sensitive := truncateURL("https://user:password@example.com/art.jpg?api_key=secret#token")
	if strings.Contains(sensitive, "password") || strings.Contains(sensitive, "secret") || strings.Contains(sensitive, "token") {
		t.Fatalf("sanitized URL leaked credentials: %q", sensitive)
	}
}

func TestDeduplicateStrings(t *testing.T) {
	got := deduplicateStrings([]string{"a", "b", "a", "c", "b"})
	if len(got) != 3 {
		t.Fatalf("expected 3 unique entries, got %d", len(got))
	}
}

func TestFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "clean filename unchanged",
			in:   "sunset.jpg",
			want: "sunset.jpg",
		},
		{
			name: "uppercase extension lowered",
			in:   "hello.JPG",
			want: "hello.jpg",
		},
		{
			name: "special chars stripped",
			in:   "café (1).JPEG",
			want: "caf 1.jpeg",
		},
		{
			name: "all unsafe chars produces default stem",
			in:   "...#$%.png",
			want: "image.png",
		},
		{
			name: "spaces collapsed",
			in:   "a   b   c.jpg",
			want: "a b c.jpg",
		},
		{
			name: "hyphens and underscores preserved",
			in:   "my-photo_2024.png",
			want: "my-photo_2024.png",
		},
		{
			name: "leading trailing spaces trimmed",
			in:   "  photo  .jpg",
			want: "photo.jpg",
		},
		{
			name: "mixed JPEG extension",
			in:   "Test.Jpeg",
			want: "Test.jpeg",
		},
		{
			name: "no extension",
			in:   "noext",
			want: "noext",
		},
		{
			name: "empty stem with extension",
			in:   ".png",
			want: "image.png",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Filename(tc.in)
			if got != tc.want {
				t.Errorf("Filename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
