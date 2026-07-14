package reconcile

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
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

type legacyMappingStore struct {
	directory string
}

func (s legacyMappingStore) configured() bool { return s.directory != "" }

const maxLegacyMappingBytes = 4 << 20

func newLegacyMappingStore(directory string) (legacyMappingStore, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return legacyMappingStore{}, nil
	}
	directory = filepath.Clean(directory)
	if directory == "." || !filepath.IsAbs(directory) {
		return legacyMappingStore{}, errors.New("legacy mapping directory must be absolute")
	}
	return legacyMappingStore{directory: directory}, nil
}

func legacyMappingPath(directory, address string) string {
	key := strings.ReplaceAll(address, ".", "_")
	return filepath.Join(directory, "tv_"+key+"_mapping.json")
}

func (s legacyMappingStore) load(ctx context.Context, address string) (map[string]string, bool, error) {
	if s.directory == "" {
		return nil, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("load legacy mapping: %w", err)
	}
	if err := validateLegacyMappingDirectory(s.directory); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	path, err := s.mappingPath(address)
	if err != nil {
		return nil, false, err
	}
	return loadLegacyMappingFiles(ctx, path)
}

func (s legacyMappingStore) mappingPath(address string) (string, error) {
	if address == "" || address != strings.TrimSpace(address) || strings.ContainsAny(address, `/\\`) {
		return "", errors.New("legacy mapping address is unsafe")
	}
	path := legacyMappingPath(s.directory, address)
	if filepath.Dir(path) != s.directory {
		return "", errors.New("legacy mapping path escapes its directory")
	}
	return path, nil
}

func loadLegacyMappingFiles(ctx context.Context, path string) (map[string]string, bool, error) {
	primary, primaryExists, primaryErr := readLegacyMapping(ctx, path)
	if primaryErr == nil && primaryExists {
		return primary, true, nil
	}
	// Match the legacy store's contract: a missing primary means there is no
	// mapping to migrate. A backup is recovery for a present invalid primary,
	// not authority to resurrect a mapping that was deliberately removed.
	if primaryErr == nil && !primaryExists {
		return nil, false, nil
	}
	backup, backupExists, backupErr := readLegacyMapping(ctx, path+".bak")
	if backupErr == nil && backupExists {
		return backup, true, nil
	}
	return nil, true, fmt.Errorf("load legacy mapping and backup: %w", errors.Join(primaryErr, backupErr))
}

func validateLegacyMappingDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect legacy mapping directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("legacy mapping directory must be a non-symlink directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("legacy mapping directory mode is %04o, want 0700", info.Mode().Perm())
	}
	return nil
}

func readLegacyMapping(ctx context.Context, path string) (map[string]string, bool, error) {
	raw, err := durablefs.ReadStable(ctx, path, durablefs.StableReadOptions{
		MaxBytes: maxLegacyMappingBytes, RequiredMode: 0o600,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	mapping, err := decodeLegacyMapping(raw)
	if err != nil {
		return nil, true, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return mapping, true, nil
}

func decodeLegacyMapping(raw []byte) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	opening, object := token.(json.Delim)
	if !object || opening != '{' {
		return nil, errors.New("expected a JSON object")
	}
	mapping, err := decodeLegacyMappingEntries(decoder)
	if err != nil {
		return nil, err
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("close mapping object: %w", err)
	}
	if closing != json.Delim('}') {
		return nil, errors.New("mapping object is not closed")
	}
	if err := ensureLegacyMappingEOF(decoder); err != nil {
		return nil, err
	}
	if err := validateLegacyMapping(mapping); err != nil {
		return nil, err
	}
	return mapping, nil
}

func decodeLegacyMappingEntries(decoder *json.Decoder) (map[string]string, error) {
	mapping := make(map[string]string)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode mapping filename: %w", err)
		}
		name, valid := token.(string)
		if !valid {
			return nil, errors.New("mapping filename must be a string")
		}
		if _, duplicate := mapping[name]; duplicate {
			return nil, fmt.Errorf("mapping contains duplicate filename %q", name)
		}
		var contentID string
		if err := decoder.Decode(&contentID); err != nil {
			return nil, fmt.Errorf("decode mapping content ID for %q: %w", name, err)
		}
		mapping[name] = contentID
	}
	return mapping, nil
}

func ensureLegacyMappingEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("mapping contains trailing JSON")
		}
		return fmt.Errorf("decode mapping trailer: %w", err)
	}
	return nil
}

func validateLegacyMapping(mapping map[string]string) error {
	contentIDs := make(map[string]string, len(mapping))
	for name, contentID := range mapping {
		if name == "" || name != strings.TrimSpace(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
			return fmt.Errorf("mapping contains unsafe filename %q", name)
		}
		if contentID == "" || contentID != strings.TrimSpace(contentID) {
			return fmt.Errorf("mapping for %q contains an invalid content ID", name)
		}
		if previous, duplicate := contentIDs[contentID]; duplicate {
			return fmt.Errorf("content ID %q is mapped by both %q and %q", contentID, previous, name)
		}
		contentIDs[contentID] = name
	}
	return nil
}
