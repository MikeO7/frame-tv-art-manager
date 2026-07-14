package reconcile

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// MatteOverride assigns one matte to one exact Artwork Collection filename.
type MatteOverride struct {
	Filename string `json:"filename"`
	Matte    string `json:"matte"`
}

// MatteOverrides is an immutable canonical set of per-artwork matte choices.
// Its comparable representation keeps Policy values safe to copy and compare.
type MatteOverrides struct {
	canonical string
}

// NewMatteOverrides validates, clones, and deterministically orders overrides.
func NewMatteOverrides(overrides []MatteOverride) (MatteOverrides, error) {
	entries := slices.Clone(overrides)
	slices.SortFunc(entries, func(left, right MatteOverride) int {
		return strings.Compare(left.Filename, right.Filename)
	})
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := validateMatteOverride(entry); err != nil {
			return MatteOverrides{}, err
		}
		key := strings.ToLower(entry.Filename)
		if _, duplicate := seen[key]; duplicate {
			return MatteOverrides{}, fmt.Errorf("matte overrides repeat filename %q", entry.Filename)
		}
		seen[key] = struct{}{}
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return MatteOverrides{}, fmt.Errorf("encode matte overrides: %w", err)
	}
	return MatteOverrides{canonical: string(encoded)}, nil
}

func validateMatteOverride(entry MatteOverride) error {
	name := entry.Filename
	lowerName := strings.ToLower(name)
	if name == "" || name != strings.TrimSpace(name) || filepath.Base(name) != name ||
		strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, "._") ||
		!supportedMatteFilename(lowerName) {
		return fmt.Errorf("matte override filename %q is unsafe or unsupported", name)
	}
	if entry.Matte == "" || entry.Matte != strings.TrimSpace(entry.Matte) || len(entry.Matte) > 128 {
		return fmt.Errorf("matte override for %q has an invalid matte", name)
	}
	return nil
}

func supportedMatteFilename(name string) bool {
	switch filepath.Ext(name) {
	case ".jpg", ".jpeg", ".png":
		return true
	default:
		return false
	}
}

func (overrides MatteOverrides) entries() ([]MatteOverride, error) {
	if overrides.canonical == "" {
		return []MatteOverride{}, nil
	}
	var entries []MatteOverride
	if err := json.Unmarshal([]byte(overrides.canonical), &entries); err != nil {
		return nil, fmt.Errorf("decode matte overrides: %w", err)
	}
	canonical, err := NewMatteOverrides(entries)
	if err != nil {
		return nil, err
	}
	if canonical.canonical != overrides.canonical {
		return nil, errors.New("matte overrides are not canonical")
	}
	return entries, nil
}

func (overrides MatteOverrides) index() (map[string]string, error) {
	entries, err := overrides.entries()
	if err != nil {
		return nil, err
	}
	indexed := make(map[string]string, len(entries))
	for _, entry := range entries {
		indexed[entry.Filename] = entry.Matte
	}
	return indexed, nil
}

// MarshalJSON keeps policy fingerprints stable and includes the full mapping.
func (overrides MatteOverrides) MarshalJSON() ([]byte, error) {
	entries, err := overrides.entries()
	if err != nil {
		return nil, err
	}
	return json.Marshal(entries)
}
