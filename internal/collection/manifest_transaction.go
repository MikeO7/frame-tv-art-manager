package collection

import (
	"context"
	"fmt"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

const transactionKindManifest = "manifest"

func commitManifest(ctx context.Context, root string, next manifest) error {
	tx := transaction{Version: 1, Kind: transactionKindManifest, Next: next}
	if err := writeJournal(ctx, root, tx); err != nil {
		return fmt.Errorf("publish manifest transaction intent: %w", err)
	}
	if err := writeManifest(ctx, root, next); err != nil {
		return fmt.Errorf("publish manifest: %w", err)
	}
	if err := durablefs.Remove(ctx, journalPath(root)); err != nil {
		return fmt.Errorf("complete manifest transaction: %w", err)
	}
	return nil
}

func recoverManifestTransaction(ctx context.Context, root string, next manifest) error {
	if err := writeManifest(ctx, root, next); err != nil {
		return fmt.Errorf("recover manifest: %w", err)
	}
	if err := durablefs.Remove(ctx, journalPath(root)); err != nil {
		return fmt.Errorf("finish manifest recovery: %w", err)
	}
	return nil
}

func validTransaction(root string, tx transaction) bool {
	if tx.Next.Version != 1 {
		return false
	}
	if _, err := validateManifestItems(tx.Next); err != nil {
		return false
	}
	if tx.Kind == transactionKindManifest {
		return tx.Stage == "" && tx.Final == "" && tx.Digest == "" &&
			len(tx.Additions) == 0 && len(tx.Deletions) == 0
	}
	if tx.Kind == transactionKindBatch {
		return validBatchTransaction(root, tx)
	}
	return tx.Kind == "" && len(tx.Additions) == 0 && len(tx.Deletions) == 0 && validTransactionPaths(root, tx)
}
