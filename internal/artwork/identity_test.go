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
