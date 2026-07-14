package collection

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const controlDirectory = ".frame-tv-art-manager"

type inventoryLimits struct {
	maxBytes  int64
	maxPixels int64
}

func scan(ctx context.Context, root string, maxBytes, maxPixels int64) ([]Item, error) {
	origins, err := readManifestOrigins(ctx, root)
	if err != nil {
		return nil, err
	}
	entries, exists, err := readRoot(root)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []Item{}, nil
	}
	return scanEntries(ctx, root, entries, origins, inventoryLimits{maxBytes: maxBytes, maxPixels: maxPixels})
}

func readRoot(root string) ([]os.DirEntry, bool, error) {
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, false, errors.New("collection root must be a non-symlink directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, false, fmt.Errorf("read root: %w", err)
	}
	return entries, true, nil
}

func scanEntries(
	ctx context.Context,
	root string,
	entries []os.DirEntry,
	origins map[string]manifestOrigin,
	limits inventoryLimits,
) ([]Item, error) {
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("scan collection: %w", err)
		}
		name := entry.Name()
		if isReserved(strings.ToLower(name)) || !isSupportedName(name) {
			continue
		}
		item, include, err := inspectItem(ctx, root, entry, limits.maxBytes, limits.maxPixels)
		if err != nil {
			return nil, err
		}
		if include {
			if origin, ok := origins[item.Name]; ok && origin.digest == item.Digest {
				item.Origin = origin.origin
			}
			items = append(items, item)
		}
	}
	sortItems(items)
	return items, nil
}

func inspectItem(ctx context.Context, root string, entry os.DirEntry, maxBytes, maxPixels int64) (Item, bool, error) {
	if entry.Type()&os.ModeSymlink != 0 {
		return Item{}, false, fmt.Errorf("artwork %s is a symlink", entry.Name())
	}
	info, err := entry.Info()
	if err != nil {
		return Item{}, false, fmt.Errorf("inspect artwork %s: %w", entry.Name(), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Item{}, false, fmt.Errorf("artwork %s is not a regular non-symlink file", entry.Name())
	}
	if info.Size() > maxBytes {
		return Item{}, false, fmt.Errorf("artwork %s exceeds %d-byte limit", entry.Name(), maxBytes)
	}
	path := filepath.Join(root, entry.Name())
	file, err := os.Open(path)
	if err != nil {
		return Item{}, false, fmt.Errorf("open artwork %s: %w", entry.Name(), err)
	}
	validated, validateErr := readAndValidate(ctx, file, entry.Name(), maxBytes, maxPixels)
	closeErr := file.Close()
	if validateErr != nil {
		return Item{}, false, fmt.Errorf("validate artwork %s: %w", entry.Name(), validateErr)
	}
	if closeErr != nil {
		return Item{}, false, fmt.Errorf("close artwork %s: %w", entry.Name(), closeErr)
	}
	return Item{
		Name: entry.Name(), Path: path, Digest: validated.digest,
		Type: validated.typeID, Size: info.Size(), Width: validated.width, Height: validated.height,
		Origin: Origin{Key: "operator:" + entry.Name(), Class: OriginOperator},
	}, true, nil
}

func isSupportedName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png":
		return true
	default:
		return false
	}
}

func (s *store) plan(items []Item, input validatedImage, origin Origin) ([]Item, Change, bool) {
	for index, item := range items {
		if item.Digest == input.digest {
			if item.Origin != origin && origin.Class == OriginSource {
				projected := cloneItems(items)
				projected[index].Origin = origin
				return projected, Change{Kind: ChangeAdopted, Name: item.Name}, true
			}
			return cloneItems(items), Change{Kind: ChangeDuplicate, Name: item.Name}, true
		}
	}
	name := collisionSafeName(items, input, origin)
	item := Item{
		Name: name, Path: filepath.Join(s.root, name), Digest: input.digest,
		Type: input.typeID, Size: int64(len(input.data)), Width: input.width, Height: input.height,
		Origin: origin,
	}
	projected := append(cloneItems(items), item)
	sortItems(projected)
	return projected, Change{Kind: ChangeAdded, Name: name}, false
}

func collisionSafeName(items []Item, input validatedImage, origins ...Origin) string {
	digest := fmt.Sprintf("%x", input.digest)
	extension := "." + string(input.typeID)
	stem := input.stem
	var origin Origin
	if len(origins) > 0 {
		origin = origins[0]
	}
	if origin.Class == OriginSource {
		stem = sourceOriginStem(origin.Key)
	}
	for length := 12; length <= len(digest); length += 4 {
		candidate := stem + "-" + digest[:length] + extension
		if origin.Class == OriginSource {
			candidate = stem + ".h_" + digest[:length] + extension
		}
		if nameAvailable(items, candidate) {
			return candidate
		}
	}
	if origin.Class == OriginSource {
		return stem + ".h_" + digest + "-artwork" + extension
	}
	return stem + "-" + digest + "-artwork" + extension
}

func sourceOriginStem(key string) string {
	_, identity, found := strings.Cut(key, ":")
	if !found {
		return string(OriginSource)
	}
	var stem strings.Builder
	stem.Grow(len(identity))
	for _, char := range identity {
		switch {
		case unicode.IsLetter(char), unicode.IsDigit(char), strings.ContainsRune("._-", char):
			stem.WriteRune(char)
		case unicode.IsSpace(char):
			stem.WriteByte('-')
		}
		if stem.Len() >= 100 {
			break
		}
	}
	result := strings.Trim(stem.String(), ".-_")
	if result == "" {
		return string(OriginSource)
	}
	return result
}

func nameAvailable(items []Item, candidate string) bool {
	for _, item := range items {
		if strings.EqualFold(item.Name, candidate) {
			return false
		}
	}
	return true
}

func sortItems(items []Item) {
	sort.Slice(items, func(left, right int) bool {
		if items[left].Name != items[right].Name {
			return items[left].Name < items[right].Name
		}
		return strings.Compare(fmt.Sprintf("%x", items[left].Digest), fmt.Sprintf("%x", items[right].Digest)) < 0
	})
}

func cloneItems(items []Item) []Item {
	cloned := make([]Item, len(items))
	copy(cloned, items)
	return cloned
}

func hashFile(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("hash %s: %w", path, err)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}
