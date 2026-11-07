package themes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

const maxFileBytes int64 = 8 << 20

// ErrDryRun reports a mutation refused because DRY_RUN is enabled.
var ErrDryRun = errors.New("DRY_RUN is enabled; collection changes are not saved")

// Store reads and transactionally rewrites collections.json. Mutations are
// serialized; reads never block on a mutation for longer than it takes to
// publish the replacement file.
type Store struct {
	path     string
	dryRun   bool
	mutation chan struct{}
}

// NewStore returns the store for the Artwork Collection rooted at
// artworkDir. A dry-run store reads normally and refuses every mutation.
func NewStore(artworkDir string, dryRun bool) (*Store, error) {
	if artworkDir == "" || !filepath.IsAbs(artworkDir) || filepath.Clean(artworkDir) != artworkDir {
		return nil, errors.New("themed collections need an absolute, canonical artwork directory")
	}
	return &Store{
		path: collection.ControlPath(artworkDir, FileName), dryRun: dryRun, mutation: make(chan struct{}, 1),
	}, nil
}

// Path is the control file location, for operator-facing messages.
func (store *Store) Path() string {
	return store.path
}

// Load returns the committed document. A missing file is an empty document;
// an unreadable, unsafe, or invalid file is an error so callers can fail safe.
func (store *Store) Load(ctx context.Context) (File, error) {
	if ctx == nil {
		return File{}, errors.New("load collections context is required")
	}
	data, err := durablefs.ReadStable(ctx, store.path, durablefs.StableReadOptions{
		MaxBytes: maxFileBytes, RequiredMode: 0o600,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return Empty(), nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read %s: %w", FileName, err)
	}
	file, err := decode(data)
	if err != nil {
		return File{}, fmt.Errorf("decode %s: %w", FileName, err)
	}
	return file, nil
}

// Update applies change to the committed document and publishes the result
// atomically. The change sees a private copy; nothing is written when it
// fails or when the store is in dry-run mode.
func (store *Store) Update(ctx context.Context, change func(*File) error) (File, error) {
	if ctx == nil || change == nil {
		return File{}, errors.New("update collections requires a context and a change")
	}
	if store.dryRun {
		return File{}, ErrDryRun
	}
	if err := ctx.Err(); err != nil {
		return File{}, fmt.Errorf("update collections: %w", err)
	}
	select {
	case store.mutation <- struct{}{}:
		defer func() { <-store.mutation }()
	case <-ctx.Done():
		return File{}, fmt.Errorf("wait for collections update: %w", ctx.Err())
	}
	file, err := store.Load(ctx)
	if err != nil {
		return File{}, err
	}
	if err := change(&file); err != nil {
		return File{}, err
	}
	if err := file.validate(); err != nil {
		return File{}, fmt.Errorf("validate updated collections: %w", err)
	}
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return File{}, fmt.Errorf("encode %s: %w", FileName, err)
	}
	if err := ensureControlDirectory(filepath.Dir(store.path)); err != nil {
		return File{}, err
	}
	if err := durablefs.Replace(ctx, store.path, 0o600, func(writer io.Writer) error {
		_, writeErr := writer.Write(append(encoded, '\n'))
		return writeErr
	}); err != nil {
		return File{}, fmt.Errorf("persist %s: %w", FileName, err)
	}
	return file, nil
}

func decode(data []byte) (File, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file File
	if err := decoder.Decode(&file); err != nil {
		return File{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return File{}, errors.New("file contains data after the JSON document")
	}
	if file.Collections == nil {
		return File{}, errors.New("collections must be a list")
	}
	if err := file.validate(); err != nil {
		return File{}, err
	}
	return file, nil
}

func ensureControlDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("create control directory: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect control directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("control directory must be a non-symlink directory")
	}
	return nil
}
