package artwork

import "testing"

func TestBuildHashName(t *testing.T) {
	got := BuildHashName("001__unsplash__photo", "abc123def456", ".jpg")
	want := "001__unsplash__photo.h_abc123def456.jpg"
	if got != want {
		t.Errorf("BuildHashName() = %q, want %q", got, want)
	}
}

func TestFileTypeFromExt(t *testing.T) {
	tests := []struct{ file, want string }{
		{"photo.jpg", FileTypeJPEG},
		{"photo.JPEG", FileTypeJPEG},
		{"photo.png", FileTypePNG},
		{"photo.PNG", FileTypePNG},
		{"photo", FileTypeJPEG},
	}
	for _, tc := range tests {
		got := FileTypeFromExt(tc.file)
		if got != tc.want {
			t.Errorf("FileTypeFromExt(%q) = %q, want %q", tc.file, got, tc.want)
		}
	}
}

func TestIsSupportedExtension(t *testing.T) {
	tests := []struct {
		ext  string
		want bool
	}{
		{".jpg", true},
		{".jpeg", true},
		{".JPEG", true},
		{".png", true},
		{".gif", false},
		{".txt", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := IsSupportedExtension(tc.ext); got != tc.want {
			t.Errorf("IsSupportedExtension(%q) = %v, want %v", tc.ext, got, tc.want)
		}
	}
}

func TestParseDimensions(t *testing.T) {
	tests := []struct {
		filename   string
		expW, expH int
		expOk      bool
	}{
		{"art_3840x2160_opt.h_abc.jpg", 3840, 2160, true},
		{"001__source__slug_1920x1080.h_hash.jpg", 1920, 1080, true},
		{"001__source__slug__hash.jpg", 0, 0, false},
		{"simple.jpg", 0, 0, false},
		{"invalid_100x_opt.jpg", 0, 0, false},
		{"prefix_1920x1080.jpg", 1920, 1080, true},
	}
	for _, tt := range tests {
		w, h, ok := ParseDimensions(tt.filename)
		if ok != tt.expOk || w != tt.expW || h != tt.expH {
			t.Errorf("ParseDimensions(%q) = %d,%d,%v; want %d,%d,%v",
				tt.filename, w, h, ok, tt.expW, tt.expH, tt.expOk)
		}
	}
}

func TestParseIdentity(t *testing.T) {
	identity, clean, hash := ParseIdentity("001__unsplash__photo_3840x2160_opt.h_abc123def456.jpg")
	if identity != "001__unsplash__photo_3840x2160_opt" {
		t.Errorf("identity = %q", identity)
	}
	if clean != "001__unsplash__photo" {
		t.Errorf("cleanIdentity = %q", clean)
	}
	if hash != "abc123def456" {
		t.Errorf("hash = %q", hash)
	}

	identity, clean, hash = ParseIdentity("001__nasa__slug__deadbeef.jpg")
	if hash != "deadbeef" {
		t.Errorf("hash from __ suffix = %q", hash)
	}
	if clean != "001__nasa__slug" {
		t.Errorf("clean from __ suffix = %q", clean)
	}
	if identity != "001__nasa__slug" {
		t.Errorf("identity from __ suffix = %q", identity)
	}

	identity, clean, hash = ParseIdentity("plain.jpg")
	if hash != "" {
		t.Errorf("expected empty hash for plain name, got %q", hash)
	}
	if identity != "plain" || clean != "plain" {
		t.Errorf("plain identity = %q / %q", identity, clean)
	}
}

func TestStripDimensionSuffix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"photo_3840x2160", "photo"},
		{"photo_100x200_extra", "photo_100x200_extra"},
		{"photo", "photo"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := StripDimensionSuffix(tc.in); got != tc.want {
			t.Errorf("StripDimensionSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripIndexPrefix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"001__unsplash__photo", "unsplash__photo"},
		{"12__short", "short"},
		{"1234__too-long-prefix", "1234__too-long-prefix"},
		{"no-prefix", "no-prefix"},
	}
	for _, tc := range tests {
		if got := StripIndexPrefix(tc.in); got != tc.want {
			t.Errorf("StripIndexPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExtractStemAndHash(t *testing.T) {
	stem, hash, ext := ExtractStemAndHash("001__direct__source.h_abc123.jpg")
	if stem != "001__direct__source" || hash != "abc123" || ext != ".jpg" {
		t.Errorf("hash name = %q / %q / %q", stem, hash, ext)
	}

	stem, hash, ext = ExtractStemAndHash("001__nasa__apod__deadbeef.png")
	if stem != "001__nasa__apod" || hash != "deadbeef" || ext != ".png" {
		t.Errorf("__ suffix = %q / %q / %q", stem, hash, ext)
	}

	stem, hash, ext = ExtractStemAndHash("local-only.jpg")
	if stem != "local-only" || hash != "local" || ext != ".jpg" {
		t.Errorf("local fallback = %q / %q / %q", stem, hash, ext)
	}
}

func TestBuildOptimizedName(t *testing.T) {
	got := BuildOptimizedName("001__photo_3840x2160", 3840, 2160, "abc123", ".jpg")
	want := "001__photo_3840x2160_opt.h_abc123.jpg"
	if got != want {
		t.Errorf("BuildOptimizedName() = %q, want %q", got, want)
	}
}

func TestBuildOptimizedNameFromFile(t *testing.T) {
	name, changed := BuildOptimizedNameFromFile("001__photo.h_abc123.jpg", 3840, 2160)
	if !changed {
		t.Error("expected change when dimensions added")
	}
	if name != "001__photo_3840x2160_opt.h_abc123.jpg" {
		t.Errorf("optimized name = %q", name)
	}

	same, changed := BuildOptimizedNameFromFile("001__photo_3840x2160_opt.h_abc123.jpg", 3840, 2160)
	if changed {
		t.Error("expected no change when already optimized")
	}
	if same != "001__photo_3840x2160_opt.h_abc123.jpg" {
		t.Errorf("same name = %q", same)
	}
}
