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

// CreateExclusive atomically publishes a complete file only when path does
// not already exist. It uses a same-directory hard link as the no-clobber
// publication primitive and synchronizes the resulting namespace.
//
//nolint:gocyclo // linear crash-consistency boundaries intentionally retain precise error classification
func CreateExclusive(ctx context.Context, path string, mode fs.FileMode, write func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("create %s before work: %w", path, err)
	}
	if write == nil {
		return fmt.Errorf("create %s: writer is nil", path)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".durable-exclusive-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode.Perm()); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary mode for %s: %w", path, err)
	}
	if err := write(temporary); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary file for %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("create %s before publish: %w", path, err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s: %w", path, fs.ErrExist)
		}
		return fmt.Errorf("publish exclusive file %s: %w", path, err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return unknownOutcome(fmt.Sprintf("remove publication link for %s", path), err)
	}
	if err := syncDirectory(directory, defaultOperations()); err != nil {
		return unknownOutcome(fmt.Sprintf("confirm exclusive file %s", path), err)
	}
	return nil
}

// MoveExclusive durably moves a regular file to an unused path without ever
// replacing an existing destination. Both paths must share a directory so the
// hard-link publication is atomic and cannot cross filesystems.
func MoveExclusive(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("move %s to %s before work: %w", oldPath, newPath, err)
	}
	oldPath = filepath.Clean(oldPath)
	newPath = filepath.Clean(newPath)
	directory, err := validateExclusiveMove(oldPath, newPath)
	if err != nil {
		return err
	}
	if err := os.Link(oldPath, newPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("move to %s: %w", newPath, fs.ErrExist)
		}
		return fmt.Errorf("publish exclusive move %s to %s: %w", oldPath, newPath, err)
	}
	if err := syncDirectory(directory, defaultOperations()); err != nil {
		return unknownOutcome(fmt.Sprintf("confirm exclusive move publication %s", newPath), err)
	}
	if err := ctx.Err(); err != nil {
		return unknownOutcome(fmt.Sprintf("finish exclusive move %s to %s", oldPath, newPath), err)
	}
	if err := os.Remove(oldPath); err != nil {
		return unknownOutcome(fmt.Sprintf("remove exclusive move source %s", oldPath), err)
	}
	if err := syncDirectory(directory, defaultOperations()); err != nil {
		return unknownOutcome(fmt.Sprintf("confirm exclusive move source removal %s", oldPath), err)
	}
	return nil
}

func validateExclusiveMove(oldPath, newPath string) (string, error) {
	if oldPath == newPath {
		return "", errors.New("exclusive move source and destination are identical")
	}
	directory := filepath.Dir(oldPath)
	if directory != filepath.Dir(newPath) {
		return "", errors.New("exclusive move requires paths in the same directory")
	}
	info, err := os.Lstat(oldPath)
	if err != nil {
		return "", fmt.Errorf("inspect exclusive move source %s: %w", oldPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("exclusive move source %s is not a regular non-symlink file", oldPath)
	}
	return directory, nil
}
