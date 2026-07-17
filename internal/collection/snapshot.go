package collection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

const maxCollectionControlBytes int64 = 16 << 20

type manifest struct {
	Version    int            `json:"version"`
	Generation string         `json:"generation"`
	Items      []manifestItem `json:"items"`
}

type manifestItem struct {
	Name   string   `json:"name"`
	Digest string   `json:"digest"`
	Type   FileType `json:"type"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
	// VisualHash is retained only so manifests written by older releases can
	// be validated and migrated without rejecting their legacy field.
	VisualHash   string         `json:"visual_hash,omitempty"`
	OriginKey    string         `json:"origin_key"`
	Class        OriginClass    `json:"class"`
	Key          string         `json:"key,omitempty"`
	SourceKeys   []string       `json:"source_keys,omitempty"`
	TransformKey string         `json:"transform_key,omitempty"`
	Derivative   DerivativeKind `json:"derivative,omitempty"`
}

func buildSnapshot(root string, items []Item, changes []Change, dryRun bool) Snapshot {
	return buildSnapshotWithWarnings(root, items, changes, nil, dryRun)
}

func buildSnapshotWithWarnings(root string, items []Item, changes []Change, warnings []string, dryRun bool) Snapshot {
	cloned := cloneItems(items)
	sortItems(cloned)
	for index := range cloned {
		cloned[index].Path = filepath.Join(root, cloned[index].Name)
	}
	return Snapshot{
		Generation: generation(cloned),
		Items:      cloned,
		Changes:    append([]Change(nil), changes...),
		Warnings:   append([]string(nil), warnings...),
		DryRun:     dryRun,
	}
}

func newManifest(items []Item) manifest {
	entries := make([]manifestItem, 0, len(items))
	for _, item := range items {
		entries = append(entries, manifestItem{
			Name: item.Name, Digest: hex.EncodeToString(item.Digest[:]), Type: item.Type,
			Width: item.Width, Height: item.Height,
			OriginKey: item.Origin.Key, Class: item.Origin.Class, Key: item.Key,
			SourceKeys:   append([]string(nil), item.SourceKeys...),
			TransformKey: item.TransformKey, Derivative: item.Derivative,
		})
	}
	return manifest{Version: 3, Generation: generation(items), Items: entries}
}

func generation(items []Item) string {
	entries := make([]manifestItem, 0, len(items))
	for _, item := range items {
		entries = append(entries, manifestItem{
			Name: item.Name, Digest: hex.EncodeToString(item.Digest[:]), Type: item.Type,
			Width: item.Width, Height: item.Height,
			OriginKey: item.Origin.Key, Class: item.Origin.Class, Key: item.Key,
			SourceKeys:   append([]string(nil), item.SourceKeys...),
			TransformKey: item.TransformKey, Derivative: item.Derivative,
		})
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// ValidateSnapshot verifies that snapshot is a complete, internally
// consistent collection view rooted at expectedRoot. It does not inspect the
// filesystem; only Store can establish that the recorded facts match durable
// bytes.
func ValidateSnapshot(expectedRoot string, snapshot Snapshot) error {
	if expectedRoot == "" || !filepath.IsAbs(expectedRoot) || filepath.Clean(expectedRoot) != expectedRoot {
		return errors.New("collection snapshot root must be absolute and canonical")
	}
	if !validGeneration(snapshot.Generation) {
		return errors.New("collection snapshot generation is invalid")
	}

	items := cloneItems(snapshot.Items)
	sortItems(items)
	names := make(map[string]struct{}, len(items))
	keys := make(map[string]struct{}, len(items))
	digests := make(map[[sha256.Size]byte]struct{}, len(items))
	for _, item := range items {
		if err := validateSnapshotItem(expectedRoot, item, names, keys, digests); err != nil {
			return err
		}
		names[strings.ToLower(item.Name)] = struct{}{}
		keys[strings.ToLower(item.Key)] = struct{}{}
		digests[item.Digest] = struct{}{}
	}
	if SnapshotGeneration(items) != snapshot.Generation {
		return errors.New("collection snapshot generation does not match its items")
	}
	return nil
}

func validateSnapshotItem(
	expectedRoot string,
	item Item,
	names map[string]struct{},
	keys map[string]struct{},
	digests map[[sha256.Size]byte]struct{},
) error {
	lowerName := strings.ToLower(item.Name)
	if err := validateSnapshotItemFacts(expectedRoot, item, lowerName); err != nil {
		return err
	}
	if _, exists := names[lowerName]; exists {
		return fmt.Errorf("collection snapshot repeats name %q", item.Name)
	}
	if item.Key == "" || filepath.Base(item.Key) != item.Key || strings.ContainsAny(item.Key, `/\\`) ||
		isReserved(strings.ToLower(item.Key)) || !isSupportedName(item.Key) {
		return fmt.Errorf("collection snapshot item %q artwork key %q is invalid", item.Name, item.Key)
	}
	if _, exists := keys[strings.ToLower(item.Key)]; exists {
		return fmt.Errorf("collection snapshot repeats artwork key %q", item.Key)
	}
	if _, exists := digests[item.Digest]; exists {
		return fmt.Errorf("collection snapshot repeats digest %s", hex.EncodeToString(item.Digest[:]))
	}
	if err := validateSnapshotOrigin(item); err != nil {
		return fmt.Errorf("collection snapshot item %q origin is invalid: %w", item.Name, err)
	}
	if err := validateSnapshotDerivative(item); err != nil {
		return fmt.Errorf("collection snapshot item %q derivative is invalid: %w", item.Name, err)
	}
	return nil
}

func validateSnapshotItemFacts(expectedRoot string, item Item, lowerName string) error {
	if item.Name == "" || filepath.Base(item.Name) != item.Name || strings.ContainsAny(item.Name, `/\`) ||
		isReserved(lowerName) || !isSupportedName(item.Name) {
		return fmt.Errorf("collection snapshot item name %q is invalid", item.Name)
	}
	if item.Path != filepath.Join(expectedRoot, item.Name) {
		return fmt.Errorf("collection snapshot item %q path is not the exact collection path", item.Name)
	}
	if item.Digest == ([sha256.Size]byte{}) {
		return fmt.Errorf("collection snapshot item %q digest is empty", item.Name)
	}
	if item.Size <= 0 {
		return fmt.Errorf("collection snapshot item %q size must be positive", item.Name)
	}
	if item.Width <= 0 || item.Height <= 0 {
		return fmt.Errorf("collection snapshot item %q dimensions must be positive", item.Name)
	}
	if !snapshotTypeMatchesName(item.Type, lowerName) {
		return fmt.Errorf("collection snapshot item %q type %q is invalid for its name", item.Name, item.Type)
	}
	return nil
}

func validGeneration(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func snapshotTypeMatchesName(typeID FileType, lowerName string) bool {
	switch typeID {
	case FileTypeJPEG:
		return strings.HasSuffix(lowerName, ".jpg") || strings.HasSuffix(lowerName, ".jpeg")
	case FileTypePNG:
		return strings.HasSuffix(lowerName, ".png")
	default:
		return false
	}
}

func validateSnapshotOrigin(item Item) error {
	switch item.Origin.Class {
	case OriginOperator:
		if !validPrefixedKey(item.Origin.Key, "operator:") {
			return errors.New("operator key is invalid")
		}
	case OriginOperatorUpload:
		return validateUploadOrigin(item)
	case OriginSource:
		if !validSourceOriginKey(item.Origin.Key) {
			return errors.New("source key is invalid")
		}
	case OriginDerived:
		if !validPrefixedKey(item.Origin.Key, "derived:") {
			return errors.New("derived key is invalid")
		}
	default:
		return fmt.Errorf("unknown class %q", item.Origin.Class)
	}
	return nil
}

func validateUploadOrigin(item Item) error {
	encoded := strings.TrimPrefix(item.Origin.Key, "upload:")
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != sha256.Size || encoded != strings.ToLower(encoded) {
		return errors.New("upload key is invalid")
	}
	if item.Derivative == "" && item.Origin.Key != "upload:"+hex.EncodeToString(item.Digest[:]) {
		return errors.New("upload key does not match the item digest")
	}
	return nil
}

func validPrefixedKey(key, prefix string) bool {
	value := strings.TrimPrefix(key, prefix)
	return value != key && value != "" && len(key) <= 512 && key == strings.TrimSpace(key) &&
		!strings.ContainsAny(value, "/\\\x00\r\n")
}

func validateSnapshotDerivative(item Item) error {
	if err := validateSourceReferences(item); err != nil {
		return err
	}
	return validateTransformMetadata(item)
}

func validateSourceReferences(item Item) error {
	if !slices.IsSorted(item.SourceKeys) {
		return errors.New("source references are not sorted")
	}
	seen := make(map[string]struct{}, len(item.SourceKeys))
	for _, key := range item.SourceKeys {
		if !validSourceOriginKey(key) {
			return fmt.Errorf("source reference %q is invalid", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("source reference %q is duplicated", key)
		}
		seen[key] = struct{}{}
	}
	if item.Origin.Class == OriginSource {
		if _, exists := seen[item.Origin.Key]; !exists {
			return errors.New("source origin is absent from source references")
		}
	}
	return nil
}

func validateTransformMetadata(item Item) error {
	switch item.Derivative {
	case "":
		if item.TransformKey != "" {
			return errors.New("untransformed artwork has a transform key")
		}
		if item.Origin.Class == OriginDerived {
			return errors.New("derived artwork has no derivative kind")
		}
	case DerivativeOptimized, DerivativeCollage:
		decoded, err := hex.DecodeString(item.TransformKey)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("transformed artwork has an invalid transform key")
		}
		if (item.Derivative == DerivativeCollage) != (item.Origin.Class == OriginDerived) {
			return errors.New("collage derivative and derived origin disagree")
		}
	default:
		return fmt.Errorf("unknown derivative kind %q", item.Derivative)
	}
	return nil
}

func validSourceOriginKey(key string) bool {
	const prefix = "source:"
	return strings.HasPrefix(key, prefix) && len(key) > len(prefix) && len(key) <= 512 &&
		key == strings.TrimSpace(key) && !strings.ContainsAny(key, "/\\\x00\r\n")
}
