// Package sanitize provides filename cleaning utilities.
package sanitize

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// reUnsafe matches any character that is NOT alphanumeric, space, hyphen, or underscore.
	reUnsafe = regexp.MustCompile(`[^a-zA-Z0-9 _-]`)

	// reSpaces collapses multiple consecutive spaces into one.
	reSpaces = regexp.MustCompile(` +`)
)

// Filename sanitizes a filename by stripping special characters from the stem,
// collapsing spaces, and lowercasing the extension. This prevents issues with
// the Samsung TV art API which can choke on certain characters.
//
// Examples:
//
//	"café (1).JPEG" → "caf 1.jpeg"
//	"...#$%.png"    → "image.png"
//	"hello.JPG"     → "hello.jpg"
func Filename(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	stem := strings.TrimSuffix(name, filepath.Ext(name))

	// Remove unsafe characters.
	stem = reUnsafe.ReplaceAllString(stem, "")

	// Collapse multiple spaces and trim.
	stem = reSpaces.ReplaceAllString(strings.TrimSpace(stem), " ")

	if stem == "" {
		stem = "image"
	}

	return stem + ext
}
