package collection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

const transactionKindBatch = "batch"

type transactionEffect struct {
	Stage  string `json:"stage,omitempty"`
	Final  string `json:"final"`
	Digest string `json:"digest"`
}

// stagedBytes returns the validated bytes to publish for one batch addition.
type stagedBytes func(context.Context, Item) ([]byte, error)

// stagedDirectory reads each addition from an untrusted staging directory,
// revalidating that its bytes still match the inventoried item.
func stagedDirectory(directory string) stagedBytes {
	return func(ctx context.Context, item Item) ([]byte, error) {
		return readStableBatchSource(ctx, filepath.Join(directory, item.Name), item)
	}
}

func (s *store) commitBatch(ctx context.Context, plan batchPlan, read stagedBytes) error {
	cleanupCtx := context.WithoutCancel(ctx)
	if err := cleanupBatchStages(ctx, s.root); err != nil {
		return err
	}
	tx := transaction{Version: 1, Kind: transactionKindBatch, Next: newManifest(plan.items)}
	for index, item := range plan.additions {
		effect, err := s.stageBatchAddition(ctx, item, index, read)
		if err != nil {
			return errors.Join(err, cleanupBatchStages(cleanupCtx, s.root))
		}
		tx.Additions = append(tx.Additions, effect)
	}
	for _, item := range plan.deletions {
		tx.Deletions = append(tx.Deletions, transactionEffect{
			Final: item.Path, Digest: hex.EncodeToString(item.Digest[:]),
		})
	}
	if err := writeJournal(ctx, s.root, tx); err != nil {
		return abandonBatchIntent(cleanupCtx, s.root, fmt.Errorf("publish batch transaction intent: %w", err))
	}
	return recoverBatchTransaction(ctx, s.root, tx, s.logger)
}

// abandonBatchIntent removes the staged artwork of a journal that was never
// published. A journal whose publication could not be confirmed may be visible
// and still names those stages, so they stay for recovery to roll forward; if
// the journal turns out to be absent, the next batch removes them as orphans.
func abandonBatchIntent(ctx context.Context, root string, intentErr error) error {
	if errors.Is(intentErr, durablefs.ErrOutcomeUnknown) {
		return intentErr
	}
	return errors.Join(intentErr, cleanupBatchStages(ctx, root))
}

func (s *store) stageBatchAddition(ctx context.Context, item Item, index int, read stagedBytes) (transactionEffect, error) {
	// The inventory leaves out invalid files the manifest does not record, so
	// a name it reports free can still be taken. Publication never replaces a
	// file, and refusing here keeps the collision out of the journal.
	if _, err := os.Lstat(item.Path); err == nil {
		return transactionEffect{}, fmt.Errorf("artwork name %s is taken by a file outside the inventory", item.Name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return transactionEffect{}, fmt.Errorf("inspect artwork destination %s: %w", item.Name, err)
	}
	data, err := read(ctx, item)
	if err != nil {
		return transactionEffect{}, err
	}
	digest := hex.EncodeToString(item.Digest[:])
	stage := filepath.Join(s.root, controlDirectory, stagingName, fmt.Sprintf("batch-%03d-%s%s", index, digest, filepath.Ext(item.Name)))
	if err := durablefs.Replace(ctx, stage, 0o644, func(writer io.Writer) error {
		if _, err := writer.Write(data); err != nil {
			return fmt.Errorf("write artwork bytes: %w", err)
		}
		return nil
	}); err != nil {
		return transactionEffect{}, fmt.Errorf("stage artwork %s: %w", item.Name, err)
	}
	return transactionEffect{Stage: stage, Final: item.Path, Digest: digest}, nil
}

// readStableBatchSource returns the bytes of one staged addition after
// confirming they are still the bytes the stage inventory fully validated:
// the same length and digest. Decoding them again would prove nothing more.
func readStableBatchSource(ctx context.Context, path string, expected Item) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read staged artwork %s: %w", expected.Name, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect staged artwork %s: %w", expected.Name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("staged artwork %s is not a regular non-symlink file", expected.Name)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open staged artwork %s: %w", expected.Name, err)
	}
	data, readErr := readBounded(file, expected.Size)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("read staged artwork %s: %w", expected.Name, errors.Join(readErr, closeErr))
	}
	if int64(len(data)) != expected.Size || sha256.Sum256(data) != expected.Digest {
		return nil, fmt.Errorf("staged artwork %s changed after inventory", expected.Name)
	}
	return data, nil
}

func recoverBatchTransaction(ctx context.Context, root string, tx transaction, logger *slog.Logger) error {
	for _, effect := range tx.Additions {
		expected, _ := hex.DecodeString(effect.Digest)
		if err := recoverBatchAddition(ctx, effect, expected); err != nil {
			return err
		}
	}
	if err := writeManifest(ctx, root, tx.Next); err != nil {
		return fmt.Errorf("recover batch manifest: %w", err)
	}
	for _, effect := range tx.Deletions {
		expected, _ := hex.DecodeString(effect.Digest)
		state, err := inspectDigest(effect.Final, expected)
		if err != nil {
			return err
		}
		switch state {
		case digestMismatch:
			// The file was edited after the deletion was planned. It is never
			// deleted, and refusing the whole transaction would block the
			// collection for good: the manifest no longer lists it, so the next
			// Prepare adopts the edited bytes as operator artwork.
			logger.Warn("Kept artwork that changed before its planned deletion",
				"name", filepath.Base(effect.Final))
		case digestMatch:
			if err := durablefs.Remove(ctx, effect.Final); err != nil {
				return fmt.Errorf("delete replaced artwork %s: %w", filepath.Base(effect.Final), err)
			}
		case digestAbsent:
		}
	}
	if err := durablefs.Remove(ctx, journalPath(root)); err != nil {
		return fmt.Errorf("finish batch recovery: %w", err)
	}
	return nil
}

func recoverBatchAddition(ctx context.Context, effect transactionEffect, expected []byte) error {
	state, err := inspectDigest(effect.Final, expected)
	if err != nil {
		return err
	}
	switch state {
	case digestMatch:
		return removeRecoveredStage(ctx, effect.Stage, expected)
	case digestAbsent:
		return publishStagedArtwork(ctx, transaction{Stage: effect.Stage, Final: effect.Final}, expected)
	default:
		return fmt.Errorf("refuse to replace changed artwork %s", filepath.Base(effect.Final))
	}
}

func cleanupBatchStages(ctx context.Context, root string) error {
	directory := filepath.Join(root, controlDirectory, stagingName)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read batch staging directory: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "batch-") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("batch stage %s is not a regular non-symlink file", entry.Name())
		}
		if err := durablefs.Remove(ctx, filepath.Join(directory, entry.Name())); err != nil {
			return fmt.Errorf("remove orphan batch stage %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func validBatchTransaction(root string, tx transaction) bool {
	if tx.Stage != "" || tx.Final != "" || tx.Digest != "" || len(tx.Additions)+len(tx.Deletions) == 0 {
		return false
	}
	manifestItems := make(map[string]string, len(tx.Next.Items))
	for _, item := range tx.Next.Items {
		manifestItems[item.Name] = item.Digest
	}
	seen := make(map[string]struct{}, len(tx.Additions)+len(tx.Deletions))
	for _, effect := range tx.Additions {
		if !validBatchEffect(root, effect, true, manifestItems, seen) {
			return false
		}
	}
	for _, effect := range tx.Deletions {
		if !validBatchEffect(root, effect, false, manifestItems, seen) {
			return false
		}
	}
	return true
}

func validBatchEffect(root string, effect transactionEffect, addition bool, items map[string]string, seen map[string]struct{}) bool {
	if !validBatchEffectBase(root, effect, seen) {
		return false
	}
	name := filepath.Base(effect.Final)
	if addition {
		return validBatchAddition(root, effect, name, items)
	}
	_, remains := items[name]
	return effect.Stage == "" && !remains
}

func validBatchEffectBase(root string, effect transactionEffect, seen map[string]struct{}) bool {
	digest, err := hex.DecodeString(effect.Digest)
	if err != nil || len(digest) != sha256.Size || filepath.Dir(effect.Final) != root {
		return false
	}
	if !isArtworkName(filepath.Base(effect.Final)) {
		return false
	}
	if _, exists := seen[effect.Final]; exists {
		return false
	}
	seen[effect.Final] = struct{}{}
	return true
}

func validBatchAddition(root string, effect transactionEffect, name string, items map[string]string) bool {
	stageRoot := filepath.Join(root, controlDirectory, stagingName)
	return filepath.Dir(effect.Stage) == stageRoot && strings.HasPrefix(filepath.Base(effect.Stage), "batch-") &&
		items[name] == effect.Digest
}
