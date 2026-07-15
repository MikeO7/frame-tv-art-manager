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
	"slices"
	"sort"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

const controlDirectory = ".frame-tv-art-manager"

type inventoryLimits struct {
	maxBytes          int64
	maxPixels         int64
	computeVisualHash bool
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
			if metadata, ok := origins[item.Name]; ok && metadata.digest == item.Digest {
				item.Key = metadata.key
				item.Origin = metadata.origin
				item.SourceKeys = append([]string(nil), metadata.sourceKeys...)
				item.TransformKey = metadata.transformKey
				item.Derivative = metadata.derivative
			}
			items = append(items, item)
		}
	}
	sortItems(items)
	return items, nil
}

//nolint:gocyclo // the path/open/read identity checks form one fail-closed filesystem observation
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
	opened, openedErr := file.Stat()
	if openedErr != nil {
		_ = file.Close()
		return Item{}, false, fmt.Errorf("inspect opened artwork %s: %w", entry.Name(), openedErr)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return Item{}, false, fmt.Errorf("artwork %s changed while opening", entry.Name())
	}
	validated, validateErr := readAndValidate(ctx, file, entry.Name(), maxBytes, maxPixels)
	after, statErr := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	closeErr := file.Close()
	if validateErr != nil {
		return Item{}, false, fmt.Errorf("validate artwork %s: %w", entry.Name(), validateErr)
	}
	if err := errors.Join(statErr, pathErr, closeErr); err != nil {
		return Item{}, false, fmt.Errorf("reinspect artwork %s: %w", entry.Name(), err)
	}
	if pathAfter.Mode()&os.ModeSymlink != 0 || !pathAfter.Mode().IsRegular() ||
		!os.SameFile(opened, after) || !os.SameFile(after, pathAfter) ||
		opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) ||
		after.Size() != int64(len(validated.data)) {
		return Item{}, false, fmt.Errorf("artwork %s changed while reading", entry.Name())
	}
	return Item{
		Name: entry.Name(), Key: entry.Name(), Path: path, Digest: validated.digest,
		Type: validated.typeID, Size: after.Size(), Width: validated.width, Height: validated.height,
		Origin:     Origin{Key: "operator:" + entry.Name(), Class: OriginOperator},
		visualHash: validated.visualHash, visualHashValid: validated.visualHashValid,
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
		// Upload origin records the digest of the operator's original bytes.
		// A derivative has different current bytes but remains the same logical
		// upload, so a repeated upload must resolve to that existing item.
		if origin.Class == OriginOperatorUpload && item.Origin == origin {
			return cloneItems(items), Change{Kind: ChangeDuplicate, Name: item.Name}, true
		}
		if item.Digest == input.digest {
			if item.Origin != origin && origin.Class == OriginSource {
				projected := cloneItems(items)
				projected[index].Origin = origin
				projected[index].SourceKeys = appendSourceKey(projected[index].SourceKeys, origin.Key)
				return projected, Change{Kind: ChangeAdopted, Name: item.Name}, true
			}
			return cloneItems(items), Change{Kind: ChangeDuplicate, Name: item.Name}, true
		}
	}
	name := collisionSafeName(items, input, origin)
	item := Item{
		Name: name, Key: name, Path: filepath.Join(s.root, name), Digest: input.digest,
		Type: input.typeID, Size: int64(len(input.data)), Width: input.width, Height: input.height,
		Origin:     origin,
		visualHash: input.visualHash, visualHashValid: input.visualHashValid,
	}
	if origin.Class == OriginSource {
		item.SourceKeys = []string{origin.Key}
	}
	projected := append(cloneItems(items), item)
	sortItems(projected)
	return projected, Change{Kind: ChangeAdded, Name: name}, false
}

func appendSourceKey(keys []string, key string) []string {
	for _, existing := range keys {
		if existing == key {
			return append([]string(nil), keys...)
		}
	}
	result := append(append([]string(nil), keys...), key)
	sort.Strings(result)
	return result
}

func collisionSafeName(items []Item, input validatedImage, origins ...Origin) string {
	extension := "." + string(input.typeID)
	label := input.stem
	var origin Origin
	if len(origins) > 0 {
		origin = origins[0]
	}
	if origin.Class == OriginSource {
		_, identity, found := strings.Cut(origin.Key, ":")
		if found {
			label = identity
		}
	}
	for digestBytes := 6; digestBytes <= len(input.digest); digestBytes += 2 {
		candidate := artwork.BuildContentName(label, input.digest, extension, digestBytes)
		if nameAvailable(items, candidate) {
			return candidate
		}
	}
	return artwork.BuildContentName("artwork", input.digest, extension, len(input.digest))
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
	for index := range cloned {
		//nolint:gosec // cloned is allocated to exactly len(items) immediately above
		cloned[index].SourceKeys = append([]string(nil), items[index].SourceKeys...)
	}
	return cloned
}

func itemEqual(left, right Item) bool {
	return left.Name == right.Name && left.Key == right.Key && left.Path == right.Path &&
		left.Digest == right.Digest && left.Type == right.Type && left.Size == right.Size &&
		left.Width == right.Width && left.Height == right.Height && left.Origin == right.Origin &&
		left.TransformKey == right.TransformKey && left.Derivative == right.Derivative &&
		slices.Equal(left.SourceKeys, right.SourceKeys)
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
