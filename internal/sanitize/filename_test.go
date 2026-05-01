package sanitize

import "testing"

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
