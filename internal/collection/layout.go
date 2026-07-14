package collection

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

func validateExistingLayout(root string) error {
	exists, err := inspectOptionalDirectory(root)
	if err != nil || !exists {
		return err
	}
	control := filepath.Join(root, controlDirectory)
	exists, err = inspectOptionalDirectory(control)
	if err != nil || !exists {
		return err
	}
	_, err = inspectOptionalDirectory(filepath.Join(control, stagingName))
	return err
}

func inspectOptionalDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect directory %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("path %s must be a non-symlink directory", path)
	}
	return true, nil
}

func ensureLayout(root string) error {
	for _, target := range []struct {
		path string
		mode fs.FileMode
	}{
		{path: root, mode: 0o755},
		{path: filepath.Join(root, controlDirectory), mode: 0o700},
		{path: filepath.Join(root, controlDirectory, stagingName), mode: 0o700},
	} {
		if err := ensureDirectory(target.path, target.mode); err != nil {
			return err
		}
	}
	return nil
}

func ensureDirectory(path string, mode fs.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(path, mode); err != nil {
			return fmt.Errorf("create directory %s: %w", path, err)
		}
		if err := syncOwnedDirectory(filepath.Dir(path)); err != nil {
			return fmt.Errorf("confirm created directory %s: %w", path, errors.Join(durablefs.ErrOutcomeUnknown, err))
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path %s must be a non-symlink directory", path)
	}
	if info.Mode().Perm() != mode.Perm() {
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("set directory mode %s: %w", path, err)
		}
		if err := syncOwnedDirectory(path); err != nil {
			return fmt.Errorf("confirm directory mode %s: %w", path, errors.Join(durablefs.ErrOutcomeUnknown, err))
		}
	}
	return nil
}

func journalPath(root string) string {
	return filepath.Join(root, controlDirectory, transactionName)
}
