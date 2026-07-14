package reconcile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

type stateStore struct {
	directory string
	replace   func(context.Context, string, fs.FileMode, func(io.Writer) error) error
}

const maxReconciliationStateBytes int64 = 16 << 20

func newStateStore(directory string) stateStore {
	return stateStore{directory: directory, replace: durablefs.Replace}
}

func (s stateStore) path(identity TVIdentity) string {
	key := sha256.Sum256([]byte(identity.Address + "\x00" + identity.Model))
	return filepath.Join(s.directory, "tv_"+hex.EncodeToString(key[:16])+"_reconciliation.json")
}

func (s stateStore) load(ctx context.Context, identity TVIdentity) (State, bool, error) {
	if err := s.validateDirectory(false); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return State{}, false, nil
		}
		return State{}, false, err
	}
	path := s.path(identity)
	data, err := durablefs.ReadStable(ctx, path, durablefs.StableReadOptions{
		MaxBytes: maxReconciliationStateBytes, RequiredMode: 0o600,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("read reconciliation state: %w", err)
	}
	state, err := decodeState(data, identity)
	if err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func (s stateStore) save(ctx context.Context, state State) error {
	if err := validateState(state, state.TV); err != nil {
		return fmt.Errorf("refuse invalid reconciliation state: %w", err)
	}
	if err := s.validateDirectory(true); err != nil {
		return err
	}
	data, err := encodeState(state)
	if err != nil {
		return err
	}
	path := s.path(state.TV)
	err = s.replace(ctx, path, 0o600, func(writer io.Writer) error {
		_, writeErr := writer.Write(data)
		return writeErr
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, durablefs.ErrOutcomeUnknown) {
		return fmt.Errorf("persist reconciliation state revision %d: %w", state.Revision, err)
	}
	observed, exists, readErr := s.load(context.WithoutCancel(ctx), state.TV)
	if readErr != nil {
		return errors.Join(ErrPersistenceUnknown, err, fmt.Errorf("inspect unknown persistence outcome: %w", readErr))
	}
	if !exists || !reflect.DeepEqual(observed, state) {
		return errors.Join(ErrPersistenceUnknown, err)
	}
	// The intended revision is visible and valid, but its directory durability
	// was not confirmed. Mutation authority must still fail closed.
	return errors.Join(ErrPersistenceUnknown, err)
}

func (s stateStore) validateDirectory(create bool) error {
	info, err := os.Lstat(s.directory)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := os.MkdirAll(s.directory, 0o700); err != nil {
			return fmt.Errorf("create reconciliation state directory: %w", err)
		}
		if err := os.Chmod(s.directory, 0o700); err != nil {
			return fmt.Errorf("secure reconciliation state directory: %w", err)
		}
		info, err = os.Lstat(s.directory)
	}
	if err != nil {
		return fmt.Errorf("inspect reconciliation state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("reconciliation state directory must be a non-symlink directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("reconciliation state directory mode is %04o, want 0700", info.Mode().Perm())
	}
	return nil
}

func encodeState(state State) ([]byte, error) {
	state.Checksum = ""
	canonical, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode reconciliation state: %w", err)
	}
	digest := sha256.Sum256(canonical)
	state.Checksum = hex.EncodeToString(digest[:])
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode checksummed reconciliation state: %w", err)
	}
	return append(data, '\n'), nil
}

func decodeState(data []byte, identity TVIdentity) (State, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("decode reconciliation state: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return State{}, err
	}
	provided := state.Checksum
	state.Checksum = ""
	canonical, err := json.Marshal(state)
	if err != nil {
		return State{}, fmt.Errorf("verify reconciliation state checksum: %w", err)
	}
	digest := sha256.Sum256(canonical)
	if provided == "" || !strings.EqualFold(provided, hex.EncodeToString(digest[:])) {
		return State{}, errors.New("reconciliation state checksum mismatch")
	}
	state.Checksum = provided
	if err := validateState(state, identity); err != nil {
		return State{}, err
	}
	return state, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("reconciliation state contains trailing JSON")
		}
		return fmt.Errorf("decode reconciliation state trailer: %w", err)
	}
	return nil
}
