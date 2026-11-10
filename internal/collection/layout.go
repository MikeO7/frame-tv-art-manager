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
	for _, name := range []string{stagingName, originalsName} {
		if _, err := inspectOptionalDirectory(filepath.Join(control, name)); err != nil {
			return err
		}
	}
	return nil
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
	// The artwork directory belongs to the operator, who may share it
	// group-writable over SMB or NFS: its mode is chosen only when this store
	// creates it. The control directories are private and repaired to 0700.
	if err := ensureArtworkRoot(root); err != nil {
		return err
	}
	for _, path := range []string{
		filepath.Join(root, controlDirectory),
		filepath.Join(root, controlDirectory, stagingName),
	} {
		if err := ensureDirectory(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func ensureArtworkRoot(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return ensureDirectory(root, 0o755)
	}
	if err != nil {
		return fmt.Errorf("inspect directory %s: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path %s must be a non-symlink directory", root)
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

// ControlPath returns the path of a named file inside the collection's private
// control directory. Control files are never cataloged as artwork.
func ControlPath(root, name string) string {
	return filepath.Join(root, controlDirectory, name)
}
