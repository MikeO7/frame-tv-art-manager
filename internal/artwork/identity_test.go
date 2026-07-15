package artwork

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestBuildContentNameUsesDigestAndBoundsComponent(t *testing.T) {
	t.Parallel()
	digest := sha256.Sum256([]byte("encoded artwork"))
	name := BuildContentName(strings.Repeat("Vacation / café ", 30)+".JPEG", digest, ".JPEG", 8)
	if len(name) > MaxNameBytes {
		t.Fatalf("name length = %d, want <= %d: %q", len(name), MaxNameBytes, name)
	}
	if !strings.HasSuffix(name, "--"+hex.EncodeToString(digest[:8])+".jpeg") {
		t.Fatalf("content name = %q", name)
	}
	if strings.ContainsAny(name, `/\\`) {
		t.Fatalf("content name contains a path separator: %q", name)
	}
}

func TestBuildContentNameClampsDigestLengthAndExtension(t *testing.T) {
	t.Parallel()
	digest := sha256.Sum256([]byte("x"))
	short := BuildContentName("", digest, ".gif", 1)
	if short != "art--2d711642b726.jpg" {
		t.Fatalf("short content name = %q", short)
	}
	full := BuildContentName("art.png", digest, ".png", 99)
	if !strings.HasSuffix(full, "--"+hex.EncodeToString(digest[:])+".png") || len(full) > MaxNameBytes {
		t.Fatalf("full content name = %q", full)
	}
}

func TestIsSupportedExtension(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		extension string
		want      bool
	}{
		{extension: ".jpg", want: true},
		{extension: ".JPEG", want: true},
		{extension: ".png", want: true},
		{extension: ".gif"},
		{extension: ""},
	} {
		if got := IsSupportedExtension(test.extension); got != test.want {
			t.Errorf("IsSupportedExtension(%q) = %v, want %v", test.extension, got, test.want)
		}
	}
}
