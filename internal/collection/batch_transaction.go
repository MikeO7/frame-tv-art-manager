package collection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

func (s *store) commitBatch(ctx context.Context, sourceDirectory string, plan batchPlan) error {
	cleanupCtx := context.WithoutCancel(ctx)
	if err := cleanupBatchStages(ctx, s.root); err != nil {
		return err
	}
	tx := transaction{Version: 1, Kind: transactionKindBatch, Next: newManifest(plan.items)}
	for index, item := range plan.additions {
		effect, err := s.stageBatchAddition(ctx, sourceDirectory, item, index)
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
		return errors.Join(fmt.Errorf("publish batch transaction intent: %w", err), cleanupBatchStages(cleanupCtx, s.root))
	}
	return recoverBatchTransaction(ctx, s.root, tx)
}

func (s *store) stageBatchAddition(
	ctx context.Context,
	sourceDirectory string,
	item Item,
	index int,
) (transactionEffect, error) {
	source := filepath.Join(sourceDirectory, item.Name)
	input, err := readStableBatchSource(ctx, source, item)
	if err != nil {
		return transactionEffect{}, err
	}
	digest := hex.EncodeToString(item.Digest[:])
	stage := filepath.Join(s.root, controlDirectory, stagingName, fmt.Sprintf("batch-%03d-%s%s", index, digest, filepath.Ext(item.Name)))
	if err := durablefs.Replace(ctx, stage, 0o644, func(writer io.Writer) error {
		return copyBytes(writer, input.data)
	}); err != nil {
		return transactionEffect{}, fmt.Errorf("stage artwork %s: %w", item.Name, err)
	}
	return transactionEffect{Stage: stage, Final: item.Path, Digest: digest}, nil
}

func readStableBatchSource(ctx context.Context, path string, expected Item) (validatedImage, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return validatedImage{}, fmt.Errorf("inspect staged artwork %s: %w", expected.Name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return validatedImage{}, fmt.Errorf("staged artwork %s is not a regular non-symlink file", expected.Name)
	}
	file, err := os.Open(path)
	if err != nil {
		return validatedImage{}, fmt.Errorf("open staged artwork %s: %w", expected.Name, err)
	}
	input, readErr := readAndValidate(ctx, file, expected.Name, info.Size(), int64(expected.Width)*int64(expected.Height))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return validatedImage{}, fmt.Errorf("read staged artwork %s: %w", expected.Name, errors.Join(readErr, closeErr))
	}
	if input.digest != expected.Digest || input.typeID != expected.Type || input.width != expected.Width || input.height != expected.Height {
		return validatedImage{}, fmt.Errorf("staged artwork %s changed after inventory", expected.Name)
	}
	return input, nil
}

func recoverBatchTransaction(ctx context.Context, root string, tx transaction) error {
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
		if state == digestMismatch {
			return fmt.Errorf("refuse to delete changed artwork %s", filepath.Base(effect.Final))
		}
		if state == digestMatch {
			if err := durablefs.Remove(ctx, effect.Final); err != nil {
				return fmt.Errorf("delete replaced artwork %s: %w", filepath.Base(effect.Final), err)
			}
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
	name := filepath.Base(effect.Final)
	if name == "." || name == "" || isReserved(strings.ToLower(name)) || !isSupportedName(name) {
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
