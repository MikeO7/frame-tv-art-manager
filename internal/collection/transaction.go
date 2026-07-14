package collection

import (
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
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

const (
	manifestName    = "manifest.json"
	transactionName = "transaction.json"
	stagingName     = "staging"
)

type transaction struct {
	Version   int                 `json:"version"`
	Kind      string              `json:"kind,omitempty"`
	Stage     string              `json:"stage,omitempty"`
	Final     string              `json:"final,omitempty"`
	Digest    string              `json:"digest,omitempty"`
	Additions []transactionEffect `json:"additions,omitempty"`
	Deletions []transactionEffect `json:"deletions,omitempty"`
	Next      manifest            `json:"next_manifest"`
}

type journalEnvelope struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

func (s *store) commit(ctx context.Context, input validatedImage, items []Item, duplicate bool) error {
	if duplicate {
		return commitManifest(ctx, s.root, newManifest(items))
	}
	digest := hex.EncodeToString(input.digest[:])
	stage := filepath.Join(s.root, controlDirectory, stagingName, digest+"."+string(input.typeID))
	final := filepath.Join(s.root, findDigestName(items, input.digest))
	if err := durablefs.Replace(ctx, stage, 0o644, func(writer io.Writer) error {
		return copyBytes(writer, input.data)
	}); err != nil {
		return fmt.Errorf("stage artwork: %w", err)
	}
	tx := transaction{
		Version: 1, Stage: stage, Final: final, Digest: digest, Next: newManifest(items),
	}
	if err := writeJournal(ctx, s.root, tx); err != nil {
		return fmt.Errorf("publish transaction intent: %w", err)
	}
	if err := publishNoReplace(ctx, stage, final); err != nil {
		return fmt.Errorf("publish artwork: %w", err)
	}
	if err := writeManifest(ctx, s.root, tx.Next); err != nil {
		return fmt.Errorf("publish manifest: %w", err)
	}
	if err := durablefs.Remove(ctx, journalPath(s.root)); err != nil {
		return fmt.Errorf("complete transaction: %w", err)
	}
	return nil
}

func findDigestName(items []Item, digest [sha256.Size]byte) string {
	for _, item := range items {
		if item.Digest == digest {
			return item.Name
		}
	}
	return ""
}

func recoverTransaction(ctx context.Context, root string) error {
	tx, exists, err := readJournal(ctx, root)
	if err != nil || !exists {
		return err
	}
	if tx.Kind == transactionKindManifest {
		return recoverManifestTransaction(ctx, root, tx.Next)
	}
	if tx.Kind == transactionKindBatch {
		return recoverBatchTransaction(ctx, root, tx)
	}
	expected, err := hex.DecodeString(tx.Digest)
	if err != nil || len(expected) != sha256.Size {
		return errors.New("transaction digest is invalid")
	}
	if err := recoverArtwork(ctx, tx, expected); err != nil {
		return err
	}
	if err := writeManifest(ctx, root, tx.Next); err != nil {
		return fmt.Errorf("recover manifest: %w", err)
	}
	if err := durablefs.Remove(ctx, journalPath(root)); err != nil {
		return fmt.Errorf("finish recovery: %w", err)
	}
	return nil
}

func recoverArtwork(ctx context.Context, tx transaction, expected []byte) error {
	finalState, err := inspectDigest(tx.Final, expected)
	if err != nil {
		return err
	}
	switch finalState {
	case digestMatch:
		return removeRecoveredStage(ctx, tx.Stage, expected)
	case digestMismatch:
		return errors.New("published transaction path contains unexpected artwork")
	case digestAbsent:
		return publishStagedArtwork(ctx, tx, expected)
	default:
		return errors.New("transaction artwork has invalid digest state")
	}
}

func publishStagedArtwork(ctx context.Context, tx transaction, expected []byte) error {
	stageState, err := inspectDigest(tx.Stage, expected)
	if err != nil {
		return err
	}
	if stageState != digestMatch {
		return errors.New("transaction has neither matching staged nor published artwork")
	}
	if err := publishNoReplace(ctx, tx.Stage, tx.Final); err != nil {
		return fmt.Errorf("recover artwork publication: %w", err)
	}
	return nil
}

func publishNoReplace(ctx context.Context, stage, final string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publish %s before work: %w", final, err)
	}
	if err := os.Link(stage, final); err != nil {
		return fmt.Errorf("link staged artwork to %s: %w", final, err)
	}
	if err := syncOwnedDirectory(filepath.Dir(final)); err != nil {
		return fmt.Errorf("confirm no-replace publication %s: %w", final, errors.Join(durablefs.ErrOutcomeUnknown, err))
	}
	if err := durablefs.Remove(ctx, stage); err != nil {
		return fmt.Errorf("remove published stage: %w", err)
	}
	return nil
}

func syncOwnedDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %s: %w", path, err)
	}
	if err := directory.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync directory %s: %w", path, err), directory.Close())
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close directory %s: %w", path, err)
	}
	return nil
}

func removeRecoveredStage(ctx context.Context, stage string, expected []byte) error {
	state, err := inspectDigest(stage, expected)
	if err != nil {
		return err
	}
	if state == digestAbsent {
		return nil
	}
	if state != digestMatch {
		return errors.New("recovery stage contains unexpected artwork")
	}
	if err := durablefs.Remove(ctx, stage); err != nil {
		return fmt.Errorf("clean recovered stage: %w", err)
	}
	return nil
}

func rejectActiveTransaction(ctx context.Context, root string) error {
	_, exists, err := readJournal(ctx, root)
	if err != nil {
		return fmt.Errorf("inspect active collection transaction: %w", err)
	}
	if exists {
		return errors.New("dry run cannot return a snapshot while recovery is required")
	}
	return nil
}

type digestState uint8

const (
	digestAbsent digestState = iota
	digestMatch
	digestMismatch
)

func inspectDigest(path string, expected []byte) (digestState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return digestAbsent, nil
	}
	if err != nil {
		return digestAbsent, fmt.Errorf("inspect transaction artwork %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return digestAbsent, fmt.Errorf("transaction artwork %s is not a regular non-symlink file", path)
	}
	digest, err := hashFile(path)
	if err != nil {
		return digestAbsent, err
	}
	if string(digest[:]) == string(expected) {
		return digestMatch, nil
	}
	return digestMismatch, nil
}

func writeManifest(ctx context.Context, root string, value manifest) error {
	return writeJSON(ctx, filepath.Join(root, controlDirectory, manifestName), 0o600, value)
}

func writeJournal(ctx context.Context, root string, value transaction) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode transaction: %w", err)
	}
	digest := sha256.Sum256(payload)
	envelope := journalEnvelope{Payload: payload, Checksum: hex.EncodeToString(digest[:])}
	return writeJSON(ctx, journalPath(root), 0o600, envelope)
}

func writeJSON(ctx context.Context, path string, mode fs.FileMode, value any) error {
	return durablefs.Replace(ctx, path, mode, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
		}
		return nil
	})
}

func readJournal(ctx context.Context, root string) (transaction, bool, error) {
	path := journalPath(root)
	data, err := durablefs.ReadStable(ctx, path, durablefs.StableReadOptions{
		MaxBytes: maxCollectionControlBytes, RequiredMode: 0o600,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return transaction{}, false, nil
	}
	if err != nil {
		return transaction{}, false, fmt.Errorf("read transaction journal: %w", err)
	}
	var envelope journalEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return transaction{}, false, fmt.Errorf("decode transaction envelope: %w", err)
	}
	digest := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(digest[:]) {
		return transaction{}, false, errors.New("transaction checksum does not match payload")
	}
	var tx transaction
	if err := json.Unmarshal(envelope.Payload, &tx); err != nil {
		return transaction{}, false, fmt.Errorf("decode transaction: %w", err)
	}
	if tx.Version != 1 || !validTransaction(root, tx) {
		return transaction{}, false, errors.New("transaction paths or version are invalid")
	}
	return tx, true, nil
}

func validTransactionPaths(root string, tx transaction) bool {
	stageRoot := filepath.Join(root, controlDirectory, stagingName)
	if filepath.Dir(tx.Stage) != stageRoot || filepath.Dir(tx.Final) != root {
		return false
	}
	if filepath.Base(tx.Stage) != tx.Digest+filepath.Ext(tx.Stage) || filepath.Base(tx.Final) == "" {
		return false
	}
	for _, item := range tx.Next.Items {
		if item.Name == filepath.Base(tx.Final) && item.Digest == tx.Digest && !isReserved(strings.ToLower(item.Name)) {
			return true
		}
	}
	return false
}
