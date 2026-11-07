package collection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

// originalsName is the control subdirectory that keeps the untouched bytes of
// every artwork the image pipeline transformed. Like the rest of the control
// directory it is never cataloged, renamed, optimized, or uploaded.
const originalsName = "originals"

// Original identifies the untouched bytes a derivative is rendered from. It is
// stored as <sha256><.jpg|.png> inside the originals control directory, so the
// name alone proves which bytes it must contain.
type Original struct {
	Digest [sha256.Size]byte
	Type   FileType
}

// Name returns the original's file name inside the originals directory.
func (original Original) Name() string {
	return hex.EncodeToString(original.Digest[:]) + "." + string(original.Type)
}

// OriginalPath returns where the collection rooted at root keeps original.
func OriginalPath(root string, original Original) string {
	return filepath.Join(originalsDirectory(root), original.Name())
}

func originalsDirectory(root string) string {
	return filepath.Join(root, controlDirectory, originalsName)
}

func parseOriginalName(name string) (Original, error) {
	encoded, extension, found := strings.Cut(name, ".")
	decoded, err := hex.DecodeString(encoded)
	if !found || err != nil || len(decoded) != sha256.Size || encoded != strings.ToLower(encoded) {
		return Original{}, fmt.Errorf("original %q is not a lowercase SHA-256 file name", name)
	}
	original := Original{Type: FileType(extension)}
	copy(original.Digest[:], decoded)
	if original.Type != FileTypeJPEG && original.Type != FileTypePNG {
		return Original{}, fmt.Errorf("original %q has unsupported type %q", name, extension)
	}
	return original, nil
}

func originalNames(originals []Original) []string {
	if len(originals) == 0 {
		return nil
	}
	names := make([]string, 0, len(originals))
	for _, original := range originals {
		names = append(names, original.Name())
	}
	return names
}

func parseOriginalNames(item string, names []string) ([]Original, error) {
	if len(names) == 0 {
		return nil, nil
	}
	originals := make([]Original, 0, len(names))
	for _, name := range names {
		original, err := parseOriginalName(name)
		if err != nil {
			return nil, fmt.Errorf("manifest item %q: %w", item, err)
		}
		originals = append(originals, original)
	}
	return originals, nil
}

// validateOriginals enforces the lineage shape: only a derivative has
// originals, an optimized image has exactly one, and a collage has one (a
// collage made before originals were kept) or its two portraits in order.
func validateOriginals(item Item) error {
	if len(item.Originals) == 0 {
		return nil
	}
	switch item.Derivative {
	case DerivativeOptimized:
		if len(item.Originals) != 1 {
			return errors.New("optimized artwork must have exactly one original")
		}
	case DerivativeCollage:
		if len(item.Originals) > 2 {
			return errors.New("collage artwork has more than two originals")
		}
	default:
		return errors.New("untransformed artwork has originals")
	}
	seen := make(map[[sha256.Size]byte]struct{}, len(item.Originals))
	for _, original := range item.Originals {
		if original.Digest == ([sha256.Size]byte{}) {
			return errors.New("original digest is empty")
		}
		if original.Type != FileTypeJPEG && original.Type != FileTypePNG {
			return fmt.Errorf("original type %q is unsupported", original.Type)
		}
		if _, duplicate := seen[original.Digest]; duplicate {
			return errors.New("original is repeated")
		}
		seen[original.Digest] = struct{}{}
	}
	return nil
}

// keepOriginals durably saves every original newly referenced by next, before
// the transaction that removes the artwork it is copied from. A missing original can only come from a committed artwork with
// the same bytes; anything else would publish a manifest whose lineage cannot
// be honored, so the transaction is refused.
func (s *store) keepOriginals(ctx context.Context, current, next []Item) error {
	committed := make(map[string]Item, len(current))
	byDigest := make(map[[sha256.Size]byte]Item, len(current))
	for _, item := range current {
		committed[item.Name] = item
		byDigest[item.Digest] = item
	}
	saved := make(map[Original]struct{})
	for _, item := range next {
		previous := committed[item.Name]
		for _, original := range item.Originals {
			if _, done := saved[original]; done || slices.Contains(previous.Originals, original) {
				continue
			}
			if err := s.keepOriginal(ctx, original, byDigest); err != nil {
				return fmt.Errorf("keep original of %s: %w", item.Name, err)
			}
			saved[original] = struct{}{}
		}
	}
	return nil
}

func (s *store) keepOriginal(ctx context.Context, original Original, byDigest map[[sha256.Size]byte]Item) error {
	path := OriginalPath(s.root, original)
	exists, err := regularFileExists(path)
	if err != nil || exists {
		return err
	}
	source, found := byDigest[original.Digest]
	if !found || source.Type != original.Type {
		return fmt.Errorf("original %s is neither kept nor present in the collection", original.Name())
	}
	if err := ensureDirectory(originalsDirectory(s.root), 0o700); err != nil {
		return fmt.Errorf("prepare originals directory: %w", err)
	}
	return copyOriginal(ctx, filepath.Join(s.root, source.Name), path, original.Digest)
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect original %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("original %s is not a regular non-symlink file", filepath.Base(path))
	}
	return true, nil
}

// copyOriginal streams source into a private temporary file beside
// destination, verifies its digest, and only then publishes it, so an
// interrupted or mismatched copy never leaves a truncated original in place.
func copyOriginal(ctx context.Context, source, destination string, digest [sha256.Size]byte) (err error) {
	before, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect original source %s: %w", filepath.Base(source), err)
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("original source %s is not a regular non-symlink file", filepath.Base(source))
	}
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open original source %s: %w", filepath.Base(source), err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened original source %s: %w", filepath.Base(source), err)
	}
	if !os.SameFile(before, opened) {
		return fmt.Errorf("original source %s changed while opening", filepath.Base(source))
	}
	return durablefs.Replace(ctx, destination, 0o600, func(writer io.Writer) error {
		hash := sha256.New()
		if _, err := io.Copy(io.MultiWriter(writer, hash), &contextReader{ctx: ctx, reader: file}); err != nil {
			return fmt.Errorf("copy original bytes: %w", err)
		}
		if got := [sha256.Size]byte(hash.Sum(nil)); got != digest {
			return fmt.Errorf("original source %s changed: got %x, want %x", filepath.Base(source), got, digest)
		}
		return nil
	})
}

// collectOriginals removes every file in the originals directory that the
// committed items no longer reference, including temporary files left by an
// interrupted copy. Callers invoke it only after the manifest naming items is
// durable, so an original is never removed while committed lineage needs it.
func collectOriginals(ctx context.Context, root string, items []Item) error {
	directory := originalsDirectory(root)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read originals directory: %w", err)
	}
	referenced := make(map[string]struct{})
	for _, item := range items {
		for _, original := range item.Originals {
			referenced[original.Name()] = struct{}{}
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("collect originals: %w", err)
		}
		if _, kept := referenced[entry.Name()]; kept {
			continue
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("originals entry %s is not a regular non-symlink file", entry.Name())
		}
		if err := durablefs.Remove(ctx, filepath.Join(directory, entry.Name())); err != nil {
			return fmt.Errorf("remove unreferenced original %s: %w", entry.Name(), err)
		}
	}
	return nil
}
