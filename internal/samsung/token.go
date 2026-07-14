package samsung

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxAuthenticationTokenBytes = 4 * 1024

//nolint:gocyclo // ordered Lstat/open/fstat checks make the filesystem trust boundary auditable
func loadAuthenticationToken(path string) (string, error) {
	directory := filepath.Dir(path)
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return "", fmt.Errorf("inspect authentication token directory: %w", err)
	}
	if directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0o700 {
		return "", errors.New("authentication token directory must be a non-symlink directory with mode 0700")
	}

	before, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect authentication token: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 {
		return "", errors.New("authentication token must be a non-symlink regular file with mode 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open authentication token: %w", err)
	}
	defer func() { _ = file.Close() }()
	after, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("reinspect authentication token: %w", err)
	}
	opened, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect opened authentication token: %w", err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 ||
		!os.SameFile(before, after) || !os.SameFile(after, opened) {
		return "", errors.New("authentication token changed while being opened")
	}

	data, err := io.ReadAll(io.LimitReader(file, maxAuthenticationTokenBytes+1))
	if err != nil {
		return "", fmt.Errorf("read authentication token: %w", err)
	}
	if len(data) > maxAuthenticationTokenBytes {
		return "", fmt.Errorf("authentication token exceeds %d bytes", maxAuthenticationTokenBytes)
	}
	return validateAuthenticationToken(string(data))
}

func validateAuthenticationToken(token string) (string, error) {
	if token == "" || !utf8.ValidString(token) || strings.TrimSpace(token) != token {
		return "", errors.New("authentication token must be nonempty, valid UTF-8, and normalized")
	}
	if len(token) > maxAuthenticationTokenBytes {
		return "", fmt.Errorf("authentication token exceeds %d bytes", maxAuthenticationTokenBytes)
	}
	return token, nil
}
