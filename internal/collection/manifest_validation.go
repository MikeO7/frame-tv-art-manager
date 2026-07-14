package collection

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func validateManifestItems(value manifest) (map[string]manifestOrigin, error) {
	origins := make(map[string]manifestOrigin, len(value.Items))
	names := make(map[string]struct{}, len(value.Items))
	digests := make(map[[sha256.Size]byte]struct{}, len(value.Items))
	items := make([]Item, 0, len(value.Items))
	for _, entry := range value.Items {
		item, err := validateManifestItem(entry, names, digests)
		if err != nil {
			return nil, err
		}
		origins[entry.Name] = manifestOrigin{digest: item.Digest, origin: item.Origin}
		names[strings.ToLower(entry.Name)] = struct{}{}
		digests[item.Digest] = struct{}{}
		items = append(items, item)
	}
	return validateManifestGeneration(value.Generation, items, origins)
}

func validateManifestItem(
	entry manifestItem,
	names map[string]struct{},
	digests map[[sha256.Size]byte]struct{},
) (Item, error) {
	lowerName, err := validateManifestName(entry.Name, names)
	if err != nil {
		return Item{}, err
	}
	digest, err := validateManifestDigest(entry.Name, entry.Digest, digests)
	if err != nil {
		return Item{}, err
	}
	item := Item{
		Name: entry.Name, Digest: digest, Type: entry.Type, Width: entry.Width, Height: entry.Height,
		Origin: Origin{Key: entry.OriginKey, Class: entry.Class},
	}
	if err := validateManifestItemFacts(item, lowerName); err != nil {
		return Item{}, err
	}
	return item, nil
}

func validateManifestName(name string, names map[string]struct{}) (string, error) {
	lowerName := strings.ToLower(name)
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("manifest item name %q is invalid", name)
	}
	if isReserved(lowerName) || !isSupportedName(name) {
		return "", fmt.Errorf("manifest item name %q is invalid", name)
	}
	if _, exists := names[lowerName]; exists {
		return "", fmt.Errorf("manifest item name %q is duplicated", name)
	}
	return lowerName, nil
}

func validateManifestDigest(
	name string,
	encoded string,
	digests map[[sha256.Size]byte]struct{},
) ([sha256.Size]byte, error) {
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != sha256.Size {
		return [sha256.Size]byte{}, fmt.Errorf("manifest item %q has invalid digest", name)
	}
	var digest [sha256.Size]byte
	copy(digest[:], decoded)
	if digest == ([sha256.Size]byte{}) {
		return [sha256.Size]byte{}, fmt.Errorf("manifest item %q has empty digest", name)
	}
	if _, exists := digests[digest]; exists {
		return [sha256.Size]byte{}, fmt.Errorf("manifest item %q repeats digest %s", name, encoded)
	}
	return digest, nil
}

func validateManifestItemFacts(item Item, lowerName string) error {
	if item.Width <= 0 || item.Height <= 0 {
		return fmt.Errorf("manifest item %q dimensions must be positive", item.Name)
	}
	if !snapshotTypeMatchesName(item.Type, lowerName) {
		return fmt.Errorf("manifest item %q type %q is invalid for its name", item.Name, item.Type)
	}
	if err := validateSnapshotOrigin(item); err != nil {
		return fmt.Errorf("manifest item %q origin is invalid: %w", item.Name, err)
	}
	return nil
}

func validateManifestGeneration(
	generationValue string,
	items []Item,
	origins map[string]manifestOrigin,
) (map[string]manifestOrigin, error) {
	if !validGeneration(generationValue) {
		return nil, errors.New("collection manifest generation is invalid")
	}
	sortItems(items)
	if generationValue != generation(items) {
		return nil, errors.New("collection manifest generation does not match its items")
	}
	return origins, nil
}
