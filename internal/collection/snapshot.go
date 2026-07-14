package collection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

const maxCollectionControlBytes int64 = 16 << 20

type manifest struct {
	Version    int            `json:"version"`
	Generation string         `json:"generation"`
	Items      []manifestItem `json:"items"`
}

type manifestItem struct {
	Name      string      `json:"name"`
	Digest    string      `json:"digest"`
	Type      FileType    `json:"type"`
	Width     int         `json:"width"`
	Height    int         `json:"height"`
	OriginKey string      `json:"origin_key"`
	Class     OriginClass `json:"class"`
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
			OriginKey: item.Origin.Key, Class: item.Origin.Class,
		})
	}
	return manifest{Version: 1, Generation: generation(items), Items: entries}
}

func generation(items []Item) string {
	entries := make([]manifestItem, 0, len(items))
	for _, item := range items {
		entries = append(entries, manifestItem{
			Name: item.Name, Digest: hex.EncodeToString(item.Digest[:]), Type: item.Type,
			Width: item.Width, Height: item.Height,
			OriginKey: item.Origin.Key, Class: item.Origin.Class,
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
	digests := make(map[[sha256.Size]byte]struct{}, len(items))
	for _, item := range items {
		if err := validateSnapshotItem(expectedRoot, item, names, digests); err != nil {
			return err
		}
		names[strings.ToLower(item.Name)] = struct{}{}
		digests[item.Digest] = struct{}{}
	}
	if generation(items) != snapshot.Generation {
		return errors.New("collection snapshot generation does not match its items")
	}
	return nil
}

func validateSnapshotItem(
	expectedRoot string,
	item Item,
	names map[string]struct{},
	digests map[[sha256.Size]byte]struct{},
) error {
	lowerName := strings.ToLower(item.Name)
	if err := validateSnapshotItemFacts(expectedRoot, item, lowerName); err != nil {
		return err
	}
	if _, exists := names[lowerName]; exists {
		return fmt.Errorf("collection snapshot repeats name %q", item.Name)
	}
	if _, exists := digests[item.Digest]; exists {
		return fmt.Errorf("collection snapshot repeats digest %s", hex.EncodeToString(item.Digest[:]))
	}
	if err := validateSnapshotOrigin(item); err != nil {
		return fmt.Errorf("collection snapshot item %q origin is invalid: %w", item.Name, err)
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
		if item.Origin.Key != "operator:"+item.Name {
			return errors.New("operator key does not match the item name")
		}
	case OriginOperatorUpload:
		if item.Origin.Key != "upload:"+hex.EncodeToString(item.Digest[:]) {
			return errors.New("upload key does not match the item digest")
		}
	case OriginSource:
		if !validSourceOriginKey(item.Origin.Key) {
			return errors.New("source key is invalid")
		}
	default:
		return fmt.Errorf("unknown class %q", item.Origin.Class)
	}
	return nil
}

func validSourceOriginKey(key string) bool {
	const prefix = "source:"
	return strings.HasPrefix(key, prefix) && len(key) > len(prefix) && len(key) <= 512 &&
		key == strings.TrimSpace(key) && !strings.ContainsAny(key, "/\\\x00\r\n")
}

type manifestOrigin struct {
	digest [sha256.Size]byte
	origin Origin
}

func readManifestOrigins(ctx context.Context, root string) (map[string]manifestOrigin, error) {
	value, exists, err := readManifest(ctx, root)
	if err != nil || !exists {
		return map[string]manifestOrigin{}, err
	}
	return validateManifestItems(value)
}

func readManifest(ctx context.Context, root string) (manifest, bool, error) {
	path := filepath.Join(root, controlDirectory, manifestName)
	data, err := durablefs.ReadStable(ctx, path, durablefs.StableReadOptions{
		MaxBytes: maxCollectionControlBytes, RequiredMode: 0o600,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return manifest{}, false, nil
	}
	if err != nil {
		return manifest{}, false, fmt.Errorf("read collection manifest: %w", err)
	}
	var value manifest
	if err := json.Unmarshal(data, &value); err != nil {
		return manifest{}, false, fmt.Errorf("decode collection manifest: %w", err)
	}
	if value.Version != 1 {
		return manifest{}, false, fmt.Errorf("unsupported collection manifest version %d", value.Version)
	}
	if _, err := validateManifestItems(value); err != nil {
		return manifest{}, false, err
	}
	return value, true, nil
}
