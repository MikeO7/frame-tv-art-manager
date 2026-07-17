package collection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func validateManifestItems(value manifest) (map[string]manifestRecord, error) {
	records := make(map[string]manifestRecord, len(value.Items))
	names := make(map[string]struct{}, len(value.Items))
	keys := make(map[string]struct{}, len(value.Items))
	digests := make(map[[sha256.Size]byte]struct{}, len(value.Items))
	items := make([]Item, 0, len(value.Items))
	for _, entry := range value.Items {
		item, err := validateManifestItem(value.Version, entry, names, keys, digests)
		if err != nil {
			return nil, err
		}
		records[entry.Name] = manifestRecord{item: item}
		names[strings.ToLower(entry.Name)] = struct{}{}
		keys[strings.ToLower(item.Key)] = struct{}{}
		digests[item.Digest] = struct{}{}
		items = append(items, item)
	}
	return validateManifestGeneration(value.Version, value.Generation, items, records)
}

func validateManifestItem(
	version int,
	entry manifestItem,
	names map[string]struct{},
	keys map[string]struct{},
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
		Name: entry.Name, Key: entry.Key, Digest: digest, Type: entry.Type, Width: entry.Width, Height: entry.Height,
		Origin: Origin{Key: entry.OriginKey, Class: entry.Class}, SourceKeys: append([]string(nil), entry.SourceKeys...),
		TransformKey: entry.TransformKey, Derivative: entry.Derivative,
	}
	if err := decodeManifestVisualHash(version, entry, &item); err != nil {
		return Item{}, err
	}
	if version == 1 {
		item.Key = item.Name
		if item.Origin.Class == OriginSource {
			item.SourceKeys = []string{item.Origin.Key}
		}
	}
	if err := validateManifestItemFacts(item, lowerName, keys); err != nil {
		return Item{}, err
	}
	return item, nil
}

func decodeManifestVisualHash(version int, entry manifestItem, item *Item) error {
	if entry.VisualHash == "" {
		return nil
	}
	if version < 3 || len(entry.VisualHash) != 16 || entry.VisualHash != strings.ToLower(entry.VisualHash) {
		return fmt.Errorf("manifest item %q has invalid visual hash", entry.Name)
	}
	value, err := strconv.ParseUint(entry.VisualHash, 16, 64)
	if err != nil {
		return fmt.Errorf("manifest item %q has invalid visual hash", entry.Name)
	}
	item.visualHash = value
	item.visualHashValid = true
	return nil
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

func validateManifestItemFacts(item Item, lowerName string, keys map[string]struct{}) error {
	if item.Width <= 0 || item.Height <= 0 {
		return fmt.Errorf("manifest item %q dimensions must be positive", item.Name)
	}
	if !snapshotTypeMatchesName(item.Type, lowerName) {
		return fmt.Errorf("manifest item %q type %q is invalid for its name", item.Name, item.Type)
	}
	key := strings.ToLower(item.Key)
	if item.Key == "" || filepath.Base(item.Key) != item.Key || strings.ContainsAny(item.Key, `/\\`) ||
		isReserved(key) || !isSupportedName(item.Key) {
		return fmt.Errorf("manifest item %q artwork key %q is invalid", item.Name, item.Key)
	}
	if _, duplicate := keys[key]; duplicate {
		return fmt.Errorf("manifest item %q repeats artwork key %q", item.Name, item.Key)
	}
	if err := validateSnapshotOrigin(item); err != nil {
		return fmt.Errorf("manifest item %q origin is invalid: %w", item.Name, err)
	}
	if err := validateSnapshotDerivative(item); err != nil {
		return fmt.Errorf("manifest item %q derivative is invalid: %w", item.Name, err)
	}
	return nil
}

func validateManifestGeneration(
	version int,
	generationValue string,
	items []Item,
	records map[string]manifestRecord,
) (map[string]manifestRecord, error) {
	if !validGeneration(generationValue) {
		return nil, errors.New("collection manifest generation is invalid")
	}
	sortItems(items)
	want := generation(items)
	if version == 1 {
		want = legacyGeneration(items)
	}
	if generationValue != want {
		return nil, errors.New("collection manifest generation does not match its items")
	}
	return records, nil
}

func legacyGeneration(items []Item) string {
	type legacyItem struct {
		Name      string      `json:"name"`
		Digest    string      `json:"digest"`
		Type      FileType    `json:"type"`
		Width     int         `json:"width"`
		Height    int         `json:"height"`
		OriginKey string      `json:"origin_key"`
		Class     OriginClass `json:"class"`
	}
	entries := make([]legacyItem, 0, len(items))
	for _, item := range items {
		entries = append(entries, legacyItem{
			Name: item.Name, Digest: hex.EncodeToString(item.Digest[:]), Type: item.Type,
			Width: item.Width, Height: item.Height, OriginKey: item.Origin.Key, Class: item.Origin.Class,
		})
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
