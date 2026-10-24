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

func TestDirectProvider(t *testing.T) {
	p := newDirectProvider()
	if p.Name() != "direct" {
		t.Errorf("Name() = %q", p.Name())
	}
	if !p.CanHandle("anything") {
		t.Error("direct provider should handle any line")
	}

	images, err := p.Resolve(context.Background(), "direct:https://example.com/gallery/a.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].URL != "https://example.com/gallery/a.jpg" {
		t.Errorf("URL = %q", images[0].URL)
	}
	second, err := p.Resolve(context.Background(), "direct:https://example.com/gallery/b.jpg")
	if err != nil || len(second) != 1 {
		t.Fatalf("second Resolve() = (%v, %v)", second, err)
	}
	if images[0].Identity == second[0].Identity {
		t.Fatalf("distinct direct URLs share identity %q", images[0].Identity)
	}
	if strings.HasPrefix(images[0].Identity, "000__") || strings.HasPrefix(second[0].Identity, "001__") {
		t.Fatalf("source identities depend on list order: %q / %q", images[0].Identity, second[0].Identity)
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
