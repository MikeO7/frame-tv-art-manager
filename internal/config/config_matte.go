package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

const maxMatteConfigBytes = 1 << 20

// MatteConfig holds per-image matte overrides loaded from a mattes.json file.
type MatteConfig struct {
	Overrides    map[string]string
	DefaultMatte string
}

// ReadMatteConfig reads operator matte policy from the Artwork Collection's
// control file. A missing file is an empty policy; malformed policy is an
// error so callers can fail before authorizing TV work.
func ReadMatteConfig(ctx context.Context, artworkDir string) (*MatteConfig, error) {
	if ctx == nil {
		return nil, errors.New("read matte config context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read matte config: %w", err)
	}
	if err := validateMatteArtworkDirectory(artworkDir); err != nil {
		return nil, err
	}
	path := filepath.Join(artworkDir, "mattes.json")
	raw, err := durablefs.ReadStable(ctx, path, durablefs.StableReadOptions{MaxBytes: maxMatteConfigBytes})
	if errors.Is(err, os.ErrNotExist) {
		return emptyMatteConfig(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read matte control file: %w", err)
	}
	config, err := decodeMatteConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("decode matte config: %w", err)
	}
	return config, nil
}

func validateMatteArtworkDirectory(directory string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("artwork directory must be absolute and canonical")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect artwork directory for matte config: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("artwork directory for matte config must be a non-symlink directory")
	}
	return nil
}

func decodeMatteConfig(raw []byte) (*MatteConfig, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("matte control file is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	opening, object := token.(json.Delim)
	if !object || opening != '{' {
		return nil, errors.New("expected one JSON object")
	}
	config := emptyMatteConfig()
	seen := make(map[string]struct{})
	for decoder.More() {
		filename, matte, err := decodeMatteEntry(decoder)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[filename]; duplicate {
			return nil, fmt.Errorf("duplicate matte key %q", filename)
		}
		seen[filename] = struct{}{}
		setMatteEntry(config, filename, matte)
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("close matte object: %w", err)
	}
	if closing != json.Delim('}') {
		return nil, errors.New("matte object is not closed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("matte control file contains trailing JSON")
		}
		return nil, fmt.Errorf("decode matte trailer: %w", err)
	}
	return config, nil
}

func decodeMatteEntry(decoder *json.Decoder) (string, string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", "", fmt.Errorf("decode matte filename: %w", err)
	}
	filename, valid := token.(string)
	if !valid {
		return "", "", errors.New("matte filename must be a string")
	}
	var matte string
	if err := decoder.Decode(&matte); err != nil {
		return "", "", fmt.Errorf("decode matte value for %q: %w", filename, err)
	}
	if err := validateMatteEntry(filename, matte); err != nil {
		return "", "", err
	}
	return filename, matte, nil
}

func validateMatteEntry(filename, matte string) error {
	if err := validateMatteFilename(filename); err != nil {
		return err
	}
	if matte == "" || matte != strings.TrimSpace(matte) || len(matte) > 128 || containsControlRune(matte) {
		return fmt.Errorf("matte value for %q must be normalized, nonblank, and at most 128 bytes", filename)
	}
	return nil
}

func validateMatteFilename(filename string) error {
	if filename == "_default" {
		return nil
	}
	if filename == "" || filename != strings.TrimSpace(filename) || filepath.Base(filename) != filename ||
		filename == "." || filename == ".." || strings.ContainsAny(filename, `/\`) || containsControlRune(filename) {
		return fmt.Errorf("matte filename %q is unsafe", filename)
	}
	return nil
}

func containsControlRune(value string) bool {
	return strings.ContainsFunc(value, func(character rune) bool {
		return character < ' ' || character == 0x7f
	})
}

func setMatteEntry(config *MatteConfig, filename, matte string) {
	if filename == "_default" {
		config.DefaultMatte = matte
		return
	}
	config.Overrides[filename] = matte
}

func emptyMatteConfig() *MatteConfig {
	return &MatteConfig{Overrides: make(map[string]string)}
}
