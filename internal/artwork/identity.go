// Package artwork defines the filename identity convention shared across
// download, optimize, and TV sync paths.
package artwork

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	FileTypeJPEG = "jpg"
	FileTypePNG  = "png"
	extJPEG      = ".jpg"
	extJPEG2     = ".jpeg"
	extPNG       = ".png"
)

// IsSupportedExtension reports whether ext is a supported artwork extension.
func IsSupportedExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case extJPEG, extJPEG2, extPNG:
		return true
	default:
		return false
	}
}

// ParseDimensions extracts width and height from a filename like "..._3840x2160_opt.h_...".
func ParseDimensions(filename string) (int, int, bool) {
	ext := filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)

	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
	} else if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		identity = strings.Join(parts[:len(parts)-1], "__")
	}

	for _, p := range strings.Split(identity, "_") {
		if strings.Contains(p, "x") {
			var w, h int
			if n, _ := fmt.Sscanf(p, "%dx%d", &w, &h); n == 2 {
				return w, h, true
			}
		}
	}
	return 0, 0, false
}

// ParseIdentity extracts stem, clean stem, and hash suffix from a filename.
func ParseIdentity(filename string) (identity, cleanIdentity, hash string) {
	ext := filepath.Ext(filename)
	identity = strings.TrimSuffix(filename, ext)

	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		identity = parts[0]
		hash = parts[1]
	} else if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		hash = parts[len(parts)-1]
		identity = strings.Join(parts[:len(parts)-1], "__")
	}

	cleanIdentity = StripDimensionSuffix(strings.Split(identity, "_opt")[0])
	return identity, cleanIdentity, hash
}

// StripDimensionSuffix removes a trailing _WxH dimension segment from a stem.
func StripDimensionSuffix(stem string) string {
	lastUnderscore := strings.LastIndex(stem, "_")
	if lastUnderscore == -1 {
		return stem
	}

	suffix := stem[lastUnderscore+1:]
	if !strings.Contains(suffix, "x") {
		return stem
	}

	var w, h int
	if n, _ := fmt.Sscanf(suffix, "%dx%d", &w, &h); n == 2 {
		return stem[:lastUnderscore]
	}
	return stem
}

// StripIndexPrefix removes a numeric index prefix (e.g. "001__") for idempotency keys.
func StripIndexPrefix(identity string) string {
	if idx := strings.Index(identity, "__"); idx != -1 && idx <= 3 {
		return identity[idx+2:]
	}
	return identity
}

// ExtractStemAndHash splits a filename into display stem and content hash.
func ExtractStemAndHash(filename string) (stem, hash, ext string) {
	ext = filepath.Ext(filename)
	identity := strings.TrimSuffix(filename, ext)

	if parts := strings.Split(identity, ".h_"); len(parts) == 2 {
		stem = parts[0]
		hash = parts[1]
		return stem, hash, ext
	}
	if parts := strings.Split(identity, "__"); len(parts) >= 2 {
		hash = parts[len(parts)-1]
		stem = strings.Join(parts[:len(parts)-1], "__")
		return stem, hash, ext
	}

	stem = identity
	hash = "local"
	return stem, hash, ext
}

// BuildHashName formats the canonical hash-suffixed filename (identity.h_HASH.ext).
func BuildHashName(identity, hashPrefix, ext string) string {
	return fmt.Sprintf("%s.h_%s%s", identity, hashPrefix, ext)
}

// BuildOptimizedName formats the canonical optimized filename.
func BuildOptimizedName(stem string, w, h int, hash, ext string) string {
	stem = StripDimensionSuffix(strings.Split(stem, "_opt")[0])
	return fmt.Sprintf("%s_%dx%d_opt.h_%s%s", stem, w, h, hash, ext)
}

// BuildOptimizedNameFromFile derives an optimized filename from an existing one.
func BuildOptimizedNameFromFile(filename string, w, h int) (string, bool) {
	stem, hash, ext := ExtractStemAndHash(filename)
	newName := BuildOptimizedName(stem, w, h, hash, ext)
	if newName == filename {
		return filename, false
	}
	return newName, true
}

// FileTypeFromExt returns the TV-compatible file type for a filename.
func FileTypeFromExt(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == extPNG {
		return FileTypePNG
	}
	return FileTypeJPEG
}
