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
)

const controlDirectory = ".frame-tv-art-manager"

type inventoryLimits struct {
	maxBytes  int64
	maxPixels int64
}

// scan inventories the committed collection, failing on any unreadable or
// invalid artwork.
func scan(ctx context.Context, root string, limits inventoryLimits) ([]Item, error) {
	records, err := readManifestRecords(ctx, root)
	if err != nil {
		return nil, err
	}
	entries, exists, err := readRoot(root)
	if err != nil || !exists {
		return []Item{}, err
	}
	renamed := renamedRecords(records, entries)
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("scan collection: %w", err)
		}
		if !isArtworkName(entry.Name()) {
			continue
		}
		item, err := inspectItem(ctx, root, entry, limits, records[entry.Name()])
		if err != nil {
			return nil, err
		}
		items = append(items, withCommittedMetadata(item, records, renamed))
	}
	sortItems(items)
	return items, nil
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

// withRecordedMetadata restores the committed logical identity of item when
// its bytes still match the manifest record.
func withRecordedMetadata(item Item, records map[string]Item) Item {
	if record, ok := records[item.Name]; ok && record.Digest == item.Digest {
		return withMetadata(item, metadataOf(record))
	}
	return item
}

// withCommittedMetadata restores item's committed logical identity by name,
// or, for a file the operator renamed, from the derivative record with the
// same bytes whose name is gone. Without that, a renamed derivative would be
// mistaken for new artwork, lose its kept original, and be transformed again.
func withCommittedMetadata(item Item, records map[string]Item, renamed map[[sha256.Size]byte]Item) Item {
	if _, named := records[item.Name]; named {
		return withRecordedMetadata(item, records)
	}
	if record, ok := renamed[item.Digest]; ok {
		return withMetadata(item, metadataOf(record))
	}
	return item
}

// renamedRecords returns, by digest, the committed derivative records whose
// name no longer exists in the directory. A record whose artwork key is now
// taken by another file's name is left out so keys stay unique.
func renamedRecords(records map[string]Item, entries []os.DirEntry) map[[sha256.Size]byte]Item {
	present := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		present[strings.ToLower(entry.Name())] = struct{}{}
	}
	renamed := make(map[[sha256.Size]byte]Item)
	for name, record := range records {
		_, nameExists := present[strings.ToLower(name)]
		_, keyTaken := present[strings.ToLower(record.Key)]
		if record.Derivative != "" && !nameExists && !keyTaken {
			renamed[record.Digest] = record
		}
	}
	return renamed
}

// withMetadata returns item carrying the logical identity in metadata. The
// committed bytes remain authoritative for every media fact.
func withMetadata(item Item, metadata ItemMetadata) Item {
	item.Key = metadata.Key
	item.Origin = metadata.Origin
	item.SourceKeys = append([]string(nil), metadata.SourceKeys...)
	item.TransformKey = metadata.TransformKey
	item.Derivative = metadata.Derivative
	item.Originals = append([]Original(nil), metadata.Originals...)
	return item
}

func metadataOf(item Item) ItemMetadata {
	return ItemMetadata{
		Key: item.Key, Origin: item.Origin, SourceKeys: item.SourceKeys,
		TransformKey: item.TransformKey, Derivative: item.Derivative, Originals: item.Originals,
	}
}

// isArtworkName reports whether a directory entry name may be cataloged.
// Control files and unsupported extensions are never artwork.
func isArtworkName(name string) bool {
	return !isReserved(strings.ToLower(name)) && isSupportedName(name)
}

// validArtworkName additionally rejects names that are not a single path
// component, as required for names and keys recorded in control files.
func validArtworkName(name string) bool {
	return name != "" && filepath.Base(name) == name && !strings.ContainsAny(name, `/\`) && isArtworkName(name)
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
		Origin: origin,
	}
	if origin.Class == OriginSource {
		item.SourceKeys = []string{origin.Key}
	}
	projected := append(cloneItems(items), item)
	sortItems(projected)
	return projected, Change{Kind: ChangeAdded, Name: name}, false
}

func appendSourceKey(keys []string, key string) []string {
	result := append([]string(nil), keys...)
	if slices.Contains(keys, key) {
		return result
	}
	result = append(result, key)
	sort.Strings(result)
	return result
}

func collisionSafeName(items []Item, input validatedImage, origin Origin) string {
	extension := "." + string(input.typeID)
	label := input.stem
	if origin.Class == OriginSource {
		_, identity, found := strings.Cut(origin.Key, ":")
		if found {
			label = identity
		}
	}
	for digestBytes := 6; digestBytes <= len(input.digest); digestBytes += 2 {
		candidate := BuildContentName(label, input.digest, extension, digestBytes)
		if nameAvailable(items, candidate) {
			return candidate
		}
	}
	return BuildContentName("artwork", input.digest, extension, len(input.digest))
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
		//nolint:gosec // cloned is allocated to exactly len(items) immediately above
		cloned[index].Originals = append([]Original(nil), items[index].Originals...)
	}
	return cloned
}

func itemEqual(left, right Item) bool {
	return left.Name == right.Name && left.Key == right.Key && left.Path == right.Path &&
		left.Digest == right.Digest && left.Type == right.Type && left.Size == right.Size &&
		left.Width == right.Width && left.Height == right.Height && left.Origin == right.Origin &&
		left.TransformKey == right.TransformKey && left.Derivative == right.Derivative &&
		slices.Equal(left.SourceKeys, right.SourceKeys) && slices.Equal(left.Originals, right.Originals)
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
