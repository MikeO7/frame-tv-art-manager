// Package artwork defines bounded display-name rules for artwork files.
package artwork

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

const (
	extJPEG  = ".jpg"
	extJPEG2 = ".jpeg"
	extPNG   = ".png"
	// MaxNameBytes leaves ample headroom for temporary-file and network-share
	// suffixes while remaining below the common 255-byte component limit.
	MaxNameBytes = 180
)

// BuildContentName returns a bounded, readable filename whose suffix is
// derived from the bytes it names. digestBytes may be increased to resolve a
// collision; values outside the SHA-256 range are clamped.
func BuildContentName(label string, digest [sha256.Size]byte, extension string, digestBytes int) string {
	extension = strings.ToLower(extension)
	if !IsSupportedExtension(extension) {
		extension = extJPEG
	}
	if digestBytes < 6 {
		digestBytes = 6
	}
	if digestBytes > sha256.Size {
		digestBytes = sha256.Size
	}
	stem := safeStem(strings.TrimSuffix(filepath.Base(label), filepath.Ext(label)))
	suffix := "--" + hex.EncodeToString(digest[:digestBytes])
	budget := MaxNameBytes - len(extension) - len(suffix)
	if budget < 1 {
		budget = 1
	}
	if len(stem) > budget {
		stem = strings.Trim(stem[:budget], ".-_")
	}
	if stem == "" {
		stem = "art"
	}
	return stem + suffix + extension
}

func safeStem(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	separator := false
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
			result.WriteRune(character)
			separator = false
		case character == '.', character == '-', character == '_', character == ' ':
			if result.Len() > 0 && !separator {
				result.WriteByte('-')
				separator = true
			}
		}
	}
	return strings.Trim(result.String(), "-")
}

// IsSupportedExtension reports whether ext is a supported artwork extension.
func IsSupportedExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case extJPEG, extJPEG2, extPNG:
		return true
	default:
		return false
	}
}
