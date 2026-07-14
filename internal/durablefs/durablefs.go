// Package durablefs provides crash-consistent publication of application-owned
// files and namespace mutations.
package durablefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrOutcomeUnknown marks an operation whose namespace mutation may be visible
// but could not be confirmed durable. Callers must inspect and reconcile the
// destination instead of blindly retrying the mutation.
var ErrOutcomeUnknown = errors.New("durable filesystem outcome unknown")

type operations struct {
	createTemp func(string, string) (*os.File, error)
	chmod      func(*os.File, os.FileMode) error
	sync       func(*os.File) error
	close      func(*os.File) error
	rename     func(string, string) error
	remove     func(string) error
	open       func(string) (*os.File, error)
	syncDir    func(*os.File) error
	closeDir   func(*os.File) error
}

func defaultOperations() operations {
	return operations{
		createTemp: os.CreateTemp,
		chmod:      func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) },
		sync:       func(file *os.File) error { return file.Sync() },
		close:      func(file *os.File) error { return file.Close() },
		rename:     os.Rename,
		remove:     os.Remove,
		open:       os.Open,
		syncDir:    func(file *os.File) error { return file.Sync() },
		closeDir:   func(file *os.File) error { return file.Close() },
	}
}

// Replace atomically publishes a complete file at path and synchronizes its
// containing directory. The writer is invoked against a same-directory
// temporary file, and the destination remains unchanged on failures before
// publication.
func Replace(ctx context.Context, path string, mode fs.FileMode, write func(io.Writer) error) error {
	return replace(ctx, path, mode, write, defaultOperations())
}

func replace(
	ctx context.Context,
	path string,
	mode fs.FileMode,
	write func(io.Writer) error,
	ops operations,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("replace %s before work: %w", path, err)
	}
	if write == nil {
		return fmt.Errorf("replace %s: writer is nil", path)
	}

	dir := filepath.Dir(path)
	temporary, err := ops.createTemp(dir, ".durable-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = ops.remove(temporaryPath) }()

	if err := ops.chmod(temporary, mode.Perm()); err != nil {
		return closeAfterFailure(temporary, fmt.Errorf("set temporary mode for %s: %w", path, err), ops.close)
	}
	if err := write(temporary); err != nil {
		return closeAfterFailure(temporary, fmt.Errorf("write temporary file for %s: %w", path, err), ops.close)
	}
	if err := ops.sync(temporary); err != nil {
		return closeAfterFailure(temporary, fmt.Errorf("sync temporary file for %s: %w", path, err), ops.close)
	}
	if err := ops.close(temporary); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("replace %s before publish: %w", path, err)
	}
	if err := ops.rename(temporaryPath, path); err != nil {
		return unknownOutcome(fmt.Sprintf("publish %s", path), err)
	}
	if err := syncDirectory(dir, ops); err != nil {
		return unknownOutcome(fmt.Sprintf("confirm published file %s", path), err)
	}
	return nil
}

// Rename moves oldPath to newPath and synchronizes every changed parent
// directory before reporting success.
func Rename(ctx context.Context, oldPath, newPath string) error {
	return rename(ctx, oldPath, newPath, defaultOperations())
}

func rename(ctx context.Context, oldPath, newPath string, ops operations) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("rename %s to %s before work: %w", oldPath, newPath, err)
	}
	if err := ops.rename(oldPath, newPath); err != nil {
		return unknownOutcome(fmt.Sprintf("rename %s to %s", oldPath, newPath), err)
	}

	newDir := filepath.Clean(filepath.Dir(newPath))
	if err := syncDirectory(newDir, ops); err != nil {
		return unknownOutcome(fmt.Sprintf("confirm rename %s to %s", oldPath, newPath), err)
	}
	oldDir := filepath.Clean(filepath.Dir(oldPath))
	if oldDir != newDir {
		if err := syncDirectory(oldDir, ops); err != nil {
			return unknownOutcome(fmt.Sprintf("confirm source removal for rename %s to %s", oldPath, newPath), err)
		}
	}
	return nil
}

// Remove deletes path and synchronizes its parent directory before reporting
// success.
func Remove(ctx context.Context, path string) error {
	return remove(ctx, path, defaultOperations())
}

func remove(ctx context.Context, path string, ops operations) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("remove %s before work: %w", path, err)
	}
	if err := ops.remove(path); err != nil {
		return unknownOutcome(fmt.Sprintf("remove %s", path), err)
	}
	if err := syncDirectory(filepath.Dir(path), ops); err != nil {
		return unknownOutcome(fmt.Sprintf("confirm removal of %s", path), err)
	}
	return nil
}

func syncDirectory(path string, ops operations) error {
	directory, err := ops.open(path)
	if err != nil {
		return fmt.Errorf("open directory %s: %w", path, err)
	}
	if err := ops.syncDir(directory); err != nil {
		return closeAfterFailure(directory, fmt.Errorf("sync directory %s: %w", path, err), ops.closeDir)
	}
	if err := ops.closeDir(directory); err != nil {
		return fmt.Errorf("close directory %s: %w", path, err)
	}
	return nil
}

func closeAfterFailure(file *os.File, operationErr error, closeFile func(*os.File) error) error {
	if err := closeFile(file); err != nil {
		return errors.Join(operationErr, fmt.Errorf("close after failure: %w", err))
	}
	return operationErr
}

func unknownOutcome(operation string, err error) error {
	return fmt.Errorf("%s: %w", operation, errors.Join(ErrOutcomeUnknown, err))
}
