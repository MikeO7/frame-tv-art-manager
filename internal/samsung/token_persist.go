package samsung

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

func persistAuthenticationToken(ctx context.Context, path, token string) error {
	token, err := validateAuthenticationToken(token)
	if err != nil {
		return tokenPersistenceError(err)
	}
	directory := filepath.Dir(path)
	if err := ensureAuthenticationTokenDirectory(directory); err != nil {
		return &Error{
			Kind: ErrorKindPersistence, Operation: "secure token directory",
			Outcome: OutcomeNotAttempted, Cause: err,
		}
	}
	if current, err := loadAuthenticationToken(path); err == nil && current == token {
		return nil
	}
	if err := rejectUnsafeAuthenticationTokenTarget(path); err != nil {
		return tokenPersistenceError(err)
	}
	if err := durablefs.Replace(ctx, path, 0o600, func(writer io.Writer) error {
		_, writeErr := io.WriteString(writer, token)
		return writeErr
	}); err != nil {
		return tokenPersistenceError(err)
	}
	return nil
}

func rejectUnsafeAuthenticationTokenTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing authentication token: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("existing authentication token must be a non-symlink regular file")
	}
	return nil
}

func ensureAuthenticationTokenDirectory(directory string) error {
	info, err := inspectOrCreateTokenDirectory(directory)
	if err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("set directory mode 0700: %w", err)
	}
	after, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("reinspect directory: %w", err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || after.Mode().Perm() != 0o700 || !os.SameFile(info, after) {
		return errors.New("authentication token directory changed while being secured")
	}
	return nil
}

func inspectOrCreateTokenDirectory(directory string) (os.FileInfo, error) {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create directory: %w", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("authentication token directory must be a non-symlink directory")
	}
	return info, nil
}

func tokenPersistenceError(err error) error {
	kind := ErrorKindPersistence
	outcome := OutcomeNotAttempted
	if errors.Is(err, durablefs.ErrOutcomeUnknown) {
		kind = ErrorKindOutcomeUnknown
		outcome = OutcomeUnknown
	}
	return &Error{
		Kind: kind, Operation: "persist authentication token", Outcome: outcome, Cause: err,
	}
}
