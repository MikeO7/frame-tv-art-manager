package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// supportedExtensions lists the image formats the Samsung Frame TV accepts.
var supportedExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
}

// ScanArtworkDir reads a directory and returns the set of image filenames
// (not full paths) that have supported extensions (.jpg, .jpeg, .png).
// Only regular files are included — subdirectories are not traversed.
func ScanArtworkDir(dir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("artwork directory does not exist: %s", dir)
		}
		return nil, fmt.Errorf("read artwork dir %s: %w", dir, err)
	}

	files := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if supportedExtensions[ext] {
			files[entry.Name()] = struct{}{}
		}
	}

	return files, nil
}

// FileTypeFromExt returns the Samsung-compatible file type string
// ("jpg" or "png") for a given filename.
func FileTypeFromExt(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png":
		return "png"
	default:
		return "jpg" // .jpg and .jpeg both → "jpg"
	}
}
