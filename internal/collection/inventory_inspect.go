package collection

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type artworkInspection struct {
	file   *os.File
	path   string
	before os.FileInfo
}

func inspectItem(
	ctx context.Context,
	root string,
	entry os.DirEntry,
	limits inventoryLimits,
	record manifestRecord,
) (_ Item, _ bool, resultErr error) {
	path, before, err := inspectArtworkEntry(ctx, root, entry, limits.maxBytes)
	if err != nil {
		return Item{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return Item{}, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Item{}, false, fmt.Errorf("open artwork %s: %w", entry.Name(), err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close artwork %s: %w", entry.Name(), err))
		}
	}()
	if err := ctx.Err(); err != nil {
		return Item{}, false, err
	}
	opened, err := file.Stat()
	if err != nil {
		return Item{}, false, fmt.Errorf("inspect opened artwork %s: %w", entry.Name(), err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return Item{}, false, fmt.Errorf("artwork %s changed while opening", entry.Name())
	}
	inspection := artworkInspection{file: file, path: path, before: opened}
	item, bytesRead, readErr := inspection.read(ctx, entry.Name(), limits, record)
	after, reinspectErr := inspection.validateStable(ctx, bytesRead)
	if readErr != nil {
		return Item{}, false, fmt.Errorf("validate artwork %s: %w", entry.Name(), errors.Join(readErr, reinspectErr))
	}
	if reinspectErr != nil {
		return Item{}, false, fmt.Errorf("reinspect artwork %s: %w", entry.Name(), reinspectErr)
	}
	item.Name = entry.Name()
	item.Path = inspection.path
	item.Size = after.Size()
	return item, true, nil
}

func inspectArtworkEntry(
	ctx context.Context,
	root string,
	entry os.DirEntry,
	maxBytes int64,
) (string, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("artwork %s is a symlink", entry.Name())
	}
	info, err := entry.Info()
	if err != nil {
		return "", nil, fmt.Errorf("inspect artwork %s: %w", entry.Name(), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("artwork %s is not a regular non-symlink file", entry.Name())
	}
	if info.Size() > maxBytes {
		return "", nil, fmt.Errorf("artwork %s exceeds %d-byte limit", entry.Name(), maxBytes)
	}
	return filepath.Join(root, entry.Name()), info, nil
}

func (inspection artworkInspection) read(
	ctx context.Context,
	name string,
	limits inventoryLimits,
	record manifestRecord,
) (Item, int64, error) {
	if record.item.Digest == ([sha256.Size]byte{}) {
		return inspection.decode(ctx, name, limits)
	}
	digest, bytesRead, err := hashBounded(ctx, inspection.file, limits.maxBytes)
	if err != nil {
		return Item{}, bytesRead, err
	}
	if item, reusable := reusableManifestItem(record, digest); reusable {
		return item, bytesRead, nil
	}
	if err := ctx.Err(); err != nil {
		return Item{}, bytesRead, err
	}
	if _, err := inspection.file.Seek(0, io.SeekStart); err != nil {
		return Item{}, bytesRead, fmt.Errorf("rewind artwork: %w", err)
	}
	return inspection.decode(ctx, name, limits)
}

func (inspection artworkInspection) decode(
	ctx context.Context,
	name string,
	limits inventoryLimits,
) (Item, int64, error) {
	validated, err := readAndValidateWithOptions(ctx, inspection.file, name, validationOptions(limits))
	if err != nil {
		return Item{}, int64(len(validated.data)), err
	}
	item := Item{
		Name: name, Key: name, Path: inspection.path, Digest: validated.digest,
		Type: validated.typeID, Size: inspection.before.Size(), Width: validated.width, Height: validated.height,
		Origin: Origin{Key: "operator:" + name, Class: OriginOperator},
	}
	return item, int64(len(validated.data)), nil
}

func (inspection artworkInspection) validateStable(ctx context.Context, bytesRead int64) (os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	after, statErr := inspection.file.Stat()
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(statErr, err)
	}
	pathAfter, pathErr := os.Lstat(inspection.path)
	if err := errors.Join(statErr, pathErr); err != nil {
		return nil, err
	}
	if pathAfter.Mode()&os.ModeSymlink != 0 || !pathAfter.Mode().IsRegular() ||
		!os.SameFile(inspection.before, after) || !os.SameFile(after, pathAfter) ||
		inspection.before.Size() != after.Size() || !inspection.before.ModTime().Equal(after.ModTime()) ||
		after.Size() != bytesRead {
		return nil, errors.New("artwork changed while reading")
	}
	return after, nil
}

func inspectPrepareItem(ctx context.Context, root string, entry os.DirEntry, limits inventoryLimits) (Item, error) {
	item, include, err := inspectItem(ctx, root, entry, limits, manifestRecord{})
	if err != nil {
		return Item{}, err
	}
	if !include {
		return Item{}, fmt.Errorf("artwork %s was not included", entry.Name())
	}
	return item, nil
}

func reusableManifestItem(record manifestRecord, digest [sha256.Size]byte) (Item, bool) {
	if record.item.Digest != digest {
		return Item{}, false
	}
	item := record.item
	item.SourceKeys = append([]string(nil), record.item.SourceKeys...)
	return item, true
}
