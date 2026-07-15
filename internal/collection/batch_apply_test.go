package collection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

func TestCollisionSafeNameBoundsSourceIdentity(t *testing.T) {
	t.Parallel()
	key := "source:provider--" + strings.Repeat("very-long-identity-", 30)
	input := validatedImage{
		stem: "ignored", typeID: FileTypeJPEG, digest: sha256.Sum256([]byte("source bytes")),
	}
	name := collisionSafeName(nil, input, Origin{Key: key, Class: OriginSource})
	if len(name) > artwork.MaxNameBytes || !strings.HasSuffix(name, ".jpg") {
		t.Fatalf("collisionSafeName() = %q (%d bytes)", name, len(name))
	}
}

func TestApplyPublishesWholeStagedCollection(t *testing.T) {
	root := t.TempDir()
	valueStore, err := New(Config{Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	seed, err := valueStore.Import(context.Background(), ImportRequest{
		Reader: bytes.NewReader(encodeInternalImage(t, 2, 2)), Hint: "old.png",
	})
	if err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	oldPath := seed.Items[0].Path
	stage := t.TempDir()
	newName := "transformed.png"
	newBytes := encodeInternalImage(t, 3, 2)
	if err := os.WriteFile(filepath.Join(stage, newName), newBytes, 0o644); err != nil {
		t.Fatalf("write staged artwork: %v", err)
	}
	origin := Origin{Key: "source:test", Class: OriginSource}
	snapshot, err := valueStore.Apply(context.Background(), ApplyRequest{
		Directory: stage, Origins: map[string]Origin{newName: origin},
	})
	if err != nil {
		t.Fatalf("apply staged collection: %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Name != newName || snapshot.Items[0].Origin != origin {
		t.Fatalf("applied snapshot = %+v", snapshot)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced artwork remains: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, newName)); err != nil || !bytes.Equal(got, newBytes) {
		t.Fatalf("published bytes = %x, %v", got, err)
	}
	if _, err := os.Stat(journalPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed journal remains: %v", err)
	}
}

func TestApplyCarriesStableMetadataAcrossDerivativeRename(t *testing.T) {
	root := t.TempDir()
	valueStore, err := New(Config{Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	origin := Origin{Key: "source:direct--art--0123456789abcdef", Class: OriginSource}
	seed, err := valueStore.Import(context.Background(), ImportRequest{
		Reader: bytes.NewReader(encodeInternalImage(t, 2, 2)), Hint: "source.png", Origin: origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	name := "source-derived.png"
	if err := os.WriteFile(filepath.Join(stage, name), encodeInternalImage(t, 3, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	transformKey := strings.Repeat("a", sha256.Size*2)
	metadata := ItemMetadata{
		Key: seed.Items[0].Key, Origin: origin, SourceKeys: []string{origin.Key},
		TransformKey: transformKey, Derivative: DerivativeOptimized,
	}
	snapshot, err := valueStore.Apply(context.Background(), ApplyRequest{
		Directory: stage, Metadata: map[string]ItemMetadata{name: metadata},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Name != name || snapshot.Items[0].Key != seed.Items[0].Key ||
		snapshot.Items[0].TransformKey != transformKey || snapshot.Items[0].Derivative != DerivativeOptimized ||
		!slices.Equal(snapshot.Items[0].SourceKeys, []string{origin.Key}) {
		t.Fatalf("derived metadata = %+v", snapshot.Items)
	}
	unchangedStage := t.TempDir()
	if err := os.WriteFile(filepath.Join(unchangedStage, name), encodeInternalImage(t, 3, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	unchanged, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: unchangedStage})
	if err != nil {
		t.Fatal(err)
	}
	got := unchanged.Items[0]
	if got.Key != metadata.Key || got.Origin != metadata.Origin || got.TransformKey != transformKey ||
		got.Derivative != DerivativeOptimized || !slices.Equal(got.SourceKeys, metadata.SourceKeys) {
		t.Fatalf("unchanged apply lost metadata: %+v", got)
	}
}

func TestApplyDryRunAndReplacementRejectionDoNotMutateCollection(t *testing.T) {
	root := t.TempDir()
	valueStore, err := New(Config{Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	seed, err := valueStore.Import(context.Background(), ImportRequest{
		Reader: bytes.NewReader(encodeInternalImage(t, 2, 2)), Hint: "fixed.png",
	})
	if err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	original, err := os.ReadFile(seed.Items[0].Path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, seed.Items[0].Name), encodeInternalImage(t, 3, 2), 0o644); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	if _, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: stage, DryRun: true}); err == nil {
		t.Fatal("dry run accepted an in-place replacement")
	}
	if _, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: stage}); err == nil {
		t.Fatal("apply accepted an in-place replacement")
	}
	assertBytesEqual(t, seed.Items[0].Path, original)
	if _, err := os.Stat(journalPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected apply created journal: %v", err)
	}
}

func TestPrepareRecoversBatchTransaction(t *testing.T) {
	root := t.TempDir()
	valueStore, err := New(Config{Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	seed, err := valueStore.Import(context.Background(), ImportRequest{
		Reader: bytes.NewReader(encodeInternalImage(t, 2, 2)), Hint: "old.png",
	})
	if err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	stageDirectory := t.TempDir()
	newName := "new.png"
	if err := os.WriteFile(filepath.Join(stageDirectory, newName), encodeInternalImage(t, 3, 3), 0o644); err != nil {
		t.Fatalf("write staged artwork: %v", err)
	}
	s := valueStore.(*store)
	items, err := s.scanApplyDirectory(context.Background(), stageDirectory, seed.Items, nil, nil)
	if err != nil {
		t.Fatalf("scan staged collection: %v", err)
	}
	plan, err := buildBatchPlan(seed.Items, items)
	if err != nil {
		t.Fatalf("build batch plan: %v", err)
	}
	if err := ensureLayout(root); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	effect, err := s.stageBatchAddition(context.Background(), stageDirectory, plan.additions[0], 0)
	if err != nil {
		t.Fatalf("stage batch addition: %v", err)
	}
	tx := transaction{
		Version: 1, Kind: transactionKindBatch, Additions: []transactionEffect{effect},
		Deletions: []transactionEffect{{Final: seed.Items[0].Path, Digest: stringHex(seed.Items[0].Digest[:])}},
		Next:      newManifest(items),
	}
	if err := writeJournal(context.Background(), root, tx); err != nil {
		t.Fatalf("write batch intent: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recoverBatchTransaction(canceled, root, tx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled batch recovery error = %v", err)
	}
	if _, err := os.Stat(seed.Items[0].Path); err != nil {
		t.Fatalf("canceled recovery removed predecessor: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, newName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled recovery published successor: %v", err)
	}
	if _, err := os.Stat(journalPath(root)); err != nil {
		t.Fatalf("canceled recovery removed durable intent: %v", err)
	}
	recovered, err := valueStore.Prepare(context.Background(), PrepareRequest{})
	if err != nil {
		t.Fatalf("recover batch: %v", err)
	}
	if len(recovered.Items) != 1 || recovered.Items[0].Name != newName {
		t.Fatalf("recovered snapshot = %+v", recovered)
	}
	if _, err := os.Stat(seed.Items[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery did not remove predecessor: %v", err)
	}
}

func TestApplyCommitsOriginOnlyChangesAndSkipsUnchangedManifest(t *testing.T) {
	root := t.TempDir()
	valueStore, err := New(Config{Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	imageBytes := encodeInternalImage(t, 2, 2)
	seed, err := valueStore.Import(context.Background(), ImportRequest{
		Reader: bytes.NewReader(imageBytes), Hint: "same.png",
	})
	if err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	stage := t.TempDir()
	name := seed.Items[0].Name
	if err := os.WriteFile(filepath.Join(stage, name), imageBytes, 0o644); err != nil {
		t.Fatalf("write staged artwork: %v", err)
	}

	sourceOrigin := Origin{Key: "source:stable-id", Class: OriginSource}
	updated, err := valueStore.Apply(context.Background(), ApplyRequest{
		Directory: stage, Origins: map[string]Origin{name: sourceOrigin},
	})
	if err != nil {
		t.Fatalf("apply origin change: %v", err)
	}
	if len(updated.Items) != 1 || updated.Items[0].Origin != sourceOrigin || len(updated.Changes) != 0 {
		t.Fatalf("origin-only snapshot = %+v", updated)
	}
	assertBytesEqual(t, updated.Items[0].Path, imageBytes)

	manifestPath := filepath.Join(root, controlDirectory, manifestName)
	before, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("stat committed manifest: %v", err)
	}
	unchanged, err := valueStore.Apply(context.Background(), ApplyRequest{
		Directory: stage, Origins: map[string]Origin{name: sourceOrigin},
	})
	if err != nil {
		t.Fatalf("reapply unchanged collection: %v", err)
	}
	after, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("stat unchanged manifest: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged apply rewrote the durable manifest")
	}
	if unchanged.Generation != updated.Generation || unchanged.Items[0].Origin != sourceOrigin {
		t.Fatalf("unchanged snapshot = %+v, want generation %q", unchanged, updated.Generation)
	}

	projectedOrigin := Origin{Key: "operator:" + name, Class: OriginOperator}
	projected, err := valueStore.Apply(context.Background(), ApplyRequest{
		Directory: stage, Origins: map[string]Origin{name: projectedOrigin}, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry-run origin change: %v", err)
	}
	if !projected.DryRun || projected.Items[0].Origin != projectedOrigin {
		t.Fatalf("dry-run snapshot = %+v", projected)
	}
	persisted, err := valueStore.Prepare(context.Background(), PrepareRequest{})
	if err != nil {
		t.Fatalf("read persisted collection: %v", err)
	}
	if persisted.Items[0].Origin != sourceOrigin {
		t.Fatalf("dry run persisted origin %+v", persisted.Items[0].Origin)
	}
}

func TestApplyRejectsUnisolatedStageDirectoriesWithoutMutation(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "collection")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create collection root: %v", err)
	}
	valueStore, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	regular := filepath.Join(base, "regular")
	if err := os.WriteFile(regular, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write regular stage: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatalf("create stage symlink: %v", err)
	}
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatalf("create inside stage: %v", err)
	}
	resolvedInside := filepath.Join(link, "inside")

	tests := []struct {
		name      string
		directory string
	}{
		{name: "empty", directory: ""},
		{name: "relative", directory: "relative"},
		{name: "noncanonical", directory: filepath.Join(base, "missing", "..", "stage")},
		{name: "missing", directory: filepath.Join(base, "missing")},
		{name: "regular file", directory: regular},
		{name: "symlink", directory: link},
		{name: "collection root", directory: root},
		{name: "inside collection", directory: inside},
		{name: "resolves inside collection", directory: resolvedInside},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: test.directory}); err == nil {
				t.Fatalf("Apply(%q) succeeded", test.directory)
			}
		})
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read collection root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "inside" {
		t.Fatalf("rejected stages mutated collection: %v", entries)
	}

	unusableRoot := filepath.Join(regular, "child")
	unusableStore, err := New(Config{Root: unusableRoot})
	if err != nil {
		t.Fatalf("new unusable-root store: %v", err)
	}
	validStage := t.TempDir()
	if _, err := unusableStore.Apply(context.Background(), ApplyRequest{Directory: validStage}); err == nil {
		t.Fatal("Apply accepted a collection root whose parent is a regular file")
	}
}

func TestApplyCleansOnlyOwnedOrphanBatchStages(t *testing.T) {
	root := t.TempDir()
	valueStore, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := ensureLayout(root); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	staging := filepath.Join(root, controlDirectory, stagingName)
	orphan := filepath.Join(staging, "batch-orphan.png")
	control := filepath.Join(staging, "keep.control")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
		t.Fatalf("write orphan batch stage: %v", err)
	}
	if err := os.WriteFile(control, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write unrelated control file: %v", err)
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "new.png"), encodeInternalImage(t, 2, 2), 0o644); err != nil {
		t.Fatalf("write staged artwork: %v", err)
	}
	if _, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: stage}); err != nil {
		t.Fatalf("apply collection: %v", err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan batch stage remains: %v", err)
	}
	assertTestFile(t, control, "keep")
}

func TestApplyRejectsUnsafeOrphanBatchStage(t *testing.T) {
	root := t.TempDir()
	valueStore, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := ensureLayout(root); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	target := filepath.Join(t.TempDir(), "operator-file")
	if err := os.WriteFile(target, []byte("preserve"), 0o644); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	orphan := filepath.Join(root, controlDirectory, stagingName, "batch-orphan.png")
	if err := os.Symlink(target, orphan); err != nil {
		t.Fatalf("create unsafe orphan: %v", err)
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "new.png"), encodeInternalImage(t, 2, 2), 0o644); err != nil {
		t.Fatalf("write staged artwork: %v", err)
	}
	if _, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: stage}); err == nil {
		t.Fatal("Apply accepted a symlink in owned batch staging")
	}
	assertTestFile(t, target, "preserve")
	if _, err := os.Lstat(orphan); err != nil {
		t.Fatalf("unsafe orphan was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed apply published artwork: %v", err)
	}
}

func TestPrepareCompletesIdempotentBatchRecovery(t *testing.T) {
	root := t.TempDir()
	valueStore, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := ensureLayout(root); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	stageDirectory := t.TempDir()
	imageBytes := encodeInternalImage(t, 3, 2)
	name := "published.png"
	if err := os.WriteFile(filepath.Join(stageDirectory, name), imageBytes, 0o644); err != nil {
		t.Fatalf("write staged artwork: %v", err)
	}
	s := valueStore.(*store)
	items, err := s.scanApplyDirectory(context.Background(), stageDirectory, nil, nil, nil)
	if err != nil {
		t.Fatalf("scan staged collection: %v", err)
	}
	effect, err := s.stageBatchAddition(context.Background(), stageDirectory, items[0], 0)
	if err != nil {
		t.Fatalf("stage batch addition: %v", err)
	}
	if err := os.WriteFile(effect.Final, imageBytes, 0o644); err != nil {
		t.Fatalf("simulate published artwork: %v", err)
	}
	retiredDigest := sha256.Sum256([]byte("retired artwork"))
	tx := transaction{
		Version: 1, Kind: transactionKindBatch, Next: newManifest(items),
		Additions: []transactionEffect{effect},
		Deletions: []transactionEffect{{
			Final: filepath.Join(root, "already-absent.png"), Digest: stringHex(retiredDigest[:]),
		}},
	}
	if err := writeJournal(context.Background(), root, tx); err != nil {
		t.Fatalf("write batch intent: %v", err)
	}

	recovered, err := valueStore.Prepare(context.Background(), PrepareRequest{})
	if err != nil {
		t.Fatalf("recover batch: %v", err)
	}
	if len(recovered.Items) != 1 || recovered.Items[0].Name != name {
		t.Fatalf("recovered snapshot = %+v", recovered)
	}
	if _, err := os.Stat(effect.Stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published stage remains: %v", err)
	}
	if _, err := os.Stat(journalPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed journal remains: %v", err)
	}
	assertBytesEqual(t, effect.Final, imageBytes)
}

func TestPrepareRefusesChangedBatchDestinations(t *testing.T) {
	t.Run("addition", func(t *testing.T) {
		root := t.TempDir()
		valueStore, err := New(Config{Root: root})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		stageDirectory := t.TempDir()
		if err := os.WriteFile(filepath.Join(stageDirectory, "new.png"), encodeInternalImage(t, 2, 2), 0o644); err != nil {
			t.Fatalf("write staged artwork: %v", err)
		}
		s := valueStore.(*store)
		items, err := s.scanApplyDirectory(context.Background(), stageDirectory, nil, nil, nil)
		if err != nil {
			t.Fatalf("scan staged collection: %v", err)
		}
		effect, err := s.stageBatchAddition(context.Background(), stageDirectory, items[0], 0)
		if err != nil {
			t.Fatalf("stage batch addition: %v", err)
		}
		operatorBytes := encodeInternalImage(t, 4, 2)
		if err := os.WriteFile(effect.Final, operatorBytes, 0o644); err != nil {
			t.Fatalf("write changed destination: %v", err)
		}
		tx := transaction{
			Version: 1, Kind: transactionKindBatch, Next: newManifest(items),
			Additions: []transactionEffect{effect},
		}
		if err := writeJournal(context.Background(), root, tx); err != nil {
			t.Fatalf("write batch intent: %v", err)
		}
		if _, err := valueStore.Prepare(context.Background(), PrepareRequest{}); err == nil {
			t.Fatal("recovery replaced a changed addition destination")
		}
		assertBytesEqual(t, effect.Final, operatorBytes)
		if _, err := os.Stat(effect.Stage); err != nil {
			t.Fatalf("refused recovery removed stage: %v", err)
		}
		if _, err := os.Stat(journalPath(root)); err != nil {
			t.Fatalf("refused recovery removed journal: %v", err)
		}
	})

	t.Run("deletion", func(t *testing.T) {
		root := t.TempDir()
		valueStore, err := New(Config{Root: root})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		original := encodeInternalImage(t, 2, 2)
		originalDigest := sha256.Sum256(original)
		final := filepath.Join(root, "changed.png")
		changed := encodeInternalImage(t, 3, 2)
		if err := os.WriteFile(final, changed, 0o644); err != nil {
			t.Fatalf("write changed deletion target: %v", err)
		}
		tx := transaction{
			Version: 1, Kind: transactionKindBatch, Next: newManifest(nil),
			Deletions: []transactionEffect{{Final: final, Digest: stringHex(originalDigest[:])}},
		}
		if err := writeJournal(context.Background(), root, tx); err != nil {
			t.Fatalf("write batch intent: %v", err)
		}
		if _, err := valueStore.Prepare(context.Background(), PrepareRequest{}); err == nil {
			t.Fatal("recovery deleted changed artwork")
		}
		assertBytesEqual(t, final, changed)
		if _, err := os.Stat(journalPath(root)); err != nil {
			t.Fatalf("refused recovery removed journal: %v", err)
		}
	})
}

func TestPrepareRejectsMalformedBatchTransactionIntents(t *testing.T) {
	digest := sha256.Sum256([]byte("expected"))
	digestText := stringHex(digest[:])
	tests := []struct {
		name   string
		mutate func(string, *transaction)
	}{
		{name: "legacy fields mixed into batch", mutate: func(root string, tx *transaction) {
			tx.Stage = filepath.Join(root, controlDirectory, stagingName, "unexpected.png")
		}},
		{name: "no effects", mutate: func(_ string, tx *transaction) { tx.Deletions = nil }},
		{name: "invalid digest", mutate: func(_ string, tx *transaction) { tx.Deletions[0].Digest = "bad" }},
		{name: "destination outside root", mutate: func(_ string, tx *transaction) {
			tx.Deletions[0].Final = filepath.Join(t.TempDir(), "outside.png")
		}},
		{name: "reserved destination", mutate: func(root string, tx *transaction) {
			tx.Deletions[0].Final = filepath.Join(root, ".hidden.png")
		}},
		{name: "duplicate destination", mutate: func(_ string, tx *transaction) {
			tx.Deletions = append(tx.Deletions, tx.Deletions[0])
		}},
		{name: "deletion has stage", mutate: func(root string, tx *transaction) {
			tx.Deletions[0].Stage = filepath.Join(root, controlDirectory, stagingName, "batch-delete.png")
		}},
		{name: "deletion remains in manifest", mutate: func(root string, tx *transaction) {
			item := Item{
				Name: "old.png", Path: filepath.Join(root, "old.png"), Digest: digest,
				Type: FileTypePNG, Size: 1, Width: 1, Height: 1,
				Origin: Origin{Key: "operator:old.png", Class: OriginOperator},
			}
			tx.Next = newManifest([]Item{item})
		}},
		{name: "addition has unowned stage name", mutate: func(root string, tx *transaction) {
			item := Item{
				Name: "new.png", Path: filepath.Join(root, "new.png"), Digest: digest,
				Type: FileTypePNG, Size: 1, Width: 1, Height: 1,
				Origin: Origin{Key: "operator:new.png", Class: OriginOperator},
			}
			tx.Deletions = nil
			tx.Additions = []transactionEffect{{
				Stage: filepath.Join(root, controlDirectory, stagingName, "unowned.png"),
				Final: item.Path, Digest: digestText,
			}}
			tx.Next = newManifest([]Item{item})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			valueStore, err := New(Config{Root: root})
			if err != nil {
				t.Fatalf("new store: %v", err)
			}
			if err := ensureLayout(root); err != nil {
				t.Fatalf("ensure layout: %v", err)
			}
			tx := transaction{
				Version: 1, Kind: transactionKindBatch, Next: newManifest(nil),
				Deletions: []transactionEffect{{
					Final: filepath.Join(root, "old.png"), Digest: digestText,
				}},
			}
			test.mutate(root, &tx)
			if err := writeJournal(context.Background(), root, tx); err != nil {
				t.Fatalf("write malformed batch intent: %v", err)
			}
			if _, err := valueStore.Prepare(context.Background(), PrepareRequest{}); err == nil {
				t.Fatal("Prepare accepted malformed batch transaction intent")
			}
			if _, err := os.Stat(journalPath(root)); err != nil {
				t.Fatalf("rejected journal was removed: %v", err)
			}
		})
	}
}

func TestApplyValidationFailuresLeaveCollectionUntouched(t *testing.T) {
	t.Run("projected item limit", func(t *testing.T) {
		root := t.TempDir()
		valueStore, err := New(Config{Root: root, MaxItems: 1})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		stage := t.TempDir()
		if err := os.WriteFile(filepath.Join(stage, "one.png"), encodeInternalImage(t, 2, 2), 0o644); err != nil {
			t.Fatalf("write first stage: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stage, "two.png"), encodeInternalImage(t, 3, 2), 0o644); err != nil {
			t.Fatalf("write second stage: %v", err)
		}
		if _, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: stage}); err == nil {
			t.Fatal("Apply accepted a collection above its item limit")
		}
		assertCollectionRootEmpty(t, root)
	})

	tests := []struct {
		name    string
		entry   string
		content []byte
		origin  Origin
	}{
		{name: "control entry", entry: "notes.txt", content: []byte("not artwork")},
		{name: "invalid artwork", entry: "broken.png", content: []byte("not an image")},
		{
			name: "invalid origin", entry: "valid.png", content: encodeInternalImage(t, 2, 2),
			origin: Origin{Key: "source:bad/path", Class: OriginSource},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			valueStore, err := New(Config{Root: root})
			if err != nil {
				t.Fatalf("new store: %v", err)
			}
			stage := t.TempDir()
			if err := os.WriteFile(filepath.Join(stage, test.entry), test.content, 0o644); err != nil {
				t.Fatalf("write staged entry: %v", err)
			}
			origins := map[string]Origin(nil)
			if test.origin != (Origin{}) {
				origins = map[string]Origin{test.entry: test.origin}
			}
			if _, err := valueStore.Apply(context.Background(), ApplyRequest{
				Directory: stage, Origins: origins,
			}); err == nil {
				t.Fatal("Apply accepted invalid staged collection")
			}
			assertCollectionRootEmpty(t, root)
		})
	}

	t.Run("untrustworthy current inventory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "broken.png"), []byte("not an image"), 0o644); err != nil {
			t.Fatalf("write broken collection artwork: %v", err)
		}
		valueStore, err := New(Config{Root: root})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		if _, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: t.TempDir()}); err == nil {
			t.Fatal("Apply accepted an untrustworthy current inventory")
		}
		assertTestFile(t, filepath.Join(root, "broken.png"), "not an image")
	})
}

func TestApplyAcceptsIsolatedStageWhenCollectionDoesNotExist(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	valueStore, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	snapshot, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("apply empty staged collection: %v", err)
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("empty staged snapshot = %+v", snapshot)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty apply created collection layout: %v", err)
	}
}

func TestApplyRejectsStageAliasingSymlinkedCollectionRoot(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real-root")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatalf("create real collection root: %v", err)
	}
	linkedRoot := filepath.Join(base, "linked-root")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatalf("link collection root: %v", err)
	}
	valueStore, err := New(Config{Root: linkedRoot})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := valueStore.Apply(context.Background(), ApplyRequest{Directory: realRoot}); err == nil {
		t.Fatal("Apply accepted a stage that aliases the collection root")
	}
}

func TestBatchTransactionErrorPathsPreserveDurableEvidence(t *testing.T) {
	t.Run("source disappears before staging", func(t *testing.T) {
		root := t.TempDir()
		valueStore, err := New(Config{Root: root})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		digest := sha256.Sum256([]byte("missing"))
		item := Item{
			Name: "missing.png", Path: filepath.Join(root, "missing.png"), Digest: digest,
			Type: FileTypePNG, Size: 1, Width: 1, Height: 1,
			Origin: Origin{Key: "operator:missing.png", Class: OriginOperator},
		}
		plan := batchPlan{items: []Item{item}, additions: []Item{item}}
		if err := valueStore.(*store).commitBatch(context.Background(), t.TempDir(), plan); err == nil {
			t.Fatal("commitBatch accepted a vanished staged source")
		}
		if _, err := os.Stat(journalPath(root)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed staging published a journal: %v", err)
		}
	})

	t.Run("canceled journal publication", func(t *testing.T) {
		root := t.TempDir()
		valueStore, err := New(Config{Root: root})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		digest := sha256.Sum256([]byte("old"))
		old := Item{Name: "old.png", Path: filepath.Join(root, "old.png"), Digest: digest}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := valueStore.(*store).commitBatch(ctx, t.TempDir(), batchPlan{deletions: []Item{old}}); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled commitBatch error = %v", err)
		}
		if _, err := os.Stat(journalPath(root)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled publication left a journal: %v", err)
		}
	})

	t.Run("canceled manifest recovery", func(t *testing.T) {
		root := t.TempDir()
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := recoverBatchTransaction(ctx, root, transaction{Next: newManifest(nil)}); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled recovery error = %v", err)
		}
	})

	t.Run("unsafe deletion target", func(t *testing.T) {
		root := t.TempDir()
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		target := filepath.Join(t.TempDir(), "target.png")
		if err := os.WriteFile(target, []byte("preserve"), 0o644); err != nil {
			t.Fatalf("write target: %v", err)
		}
		link := filepath.Join(root, "old.png")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("link deletion target: %v", err)
		}
		digest := sha256.Sum256([]byte("preserve"))
		tx := transaction{
			Next:      newManifest(nil),
			Deletions: []transactionEffect{{Final: link, Digest: stringHex(digest[:])}},
		}
		if err := recoverBatchTransaction(context.Background(), root, tx); err == nil {
			t.Fatal("recovery accepted a symlink deletion target")
		}
		assertTestFile(t, target, "preserve")
	})

	t.Run("journal removal failure", func(t *testing.T) {
		root := t.TempDir()
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		journal := journalPath(root)
		if err := os.Mkdir(journal, 0o700); err != nil {
			t.Fatalf("create obstructing journal directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(journal, "keep"), []byte("evidence"), 0o600); err != nil {
			t.Fatalf("populate obstructing journal directory: %v", err)
		}
		if err := recoverBatchTransaction(context.Background(), root, transaction{Next: newManifest(nil)}); err == nil {
			t.Fatal("recovery ignored failure to remove durable intent")
		}
		assertTestFile(t, filepath.Join(journal, "keep"), "evidence")
	})
}

func TestStableBatchSourceAndCleanupRejectUnsafeInputs(t *testing.T) {
	digest := sha256.Sum256([]byte("expected"))
	expected := Item{Name: "art.png", Digest: digest, Type: FileTypePNG, Width: 1, Height: 1}
	if _, err := readStableBatchSource(context.Background(), filepath.Join(t.TempDir(), "missing.png"), expected); err == nil {
		t.Fatal("missing staged source was accepted")
	}
	target := filepath.Join(t.TempDir(), "target.png")
	if err := os.WriteFile(target, encodeInternalImage(t, 1, 1), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(t.TempDir(), "art.png")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("link staged source: %v", err)
	}
	if _, err := readStableBatchSource(context.Background(), link, expected); err == nil {
		t.Fatal("symlink staged source was accepted")
	}
	if _, err := readStableBatchSource(context.Background(), target, expected); err == nil {
		t.Fatal("changed staged source was accepted")
	}

	if err := cleanupBatchStages(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("cleanup absent staging directory: %v", err)
	}
	root := t.TempDir()
	control := filepath.Join(root, controlDirectory)
	if err := os.Mkdir(control, 0o700); err != nil {
		t.Fatalf("create control directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(control, stagingName), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write staging obstruction: %v", err)
	}
	if err := cleanupBatchStages(context.Background(), root); err == nil {
		t.Fatal("cleanup accepted a non-directory staging path")
	}
}

func TestBatchApplyCancellationAndDefensiveBranches(t *testing.T) {
	t.Run("store mutation wait", func(t *testing.T) {
		valueStore, err := New(Config{Root: filepath.Join(t.TempDir(), "collection")})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		s := valueStore.(*store)
		s.mutation <- struct{}{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := valueStore.Apply(ctx, ApplyRequest{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Apply error = %v", err)
		}
		<-s.mutation
	})

	t.Run("origin manifest cancellation", func(t *testing.T) {
		root := t.TempDir()
		valueStore, err := New(Config{Root: root})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		digest := sha256.Sum256([]byte("item"))
		current := []Item{{
			Name: "item.png", Path: filepath.Join(root, "item.png"), Digest: digest,
			Type: FileTypePNG, Size: 1, Width: 1, Height: 1,
			Origin: Origin{Key: "operator:item.png", Class: OriginOperator},
		}}
		next := cloneItems(current)
		next[0].Origin = Origin{Key: "source:item", Class: OriginSource}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := valueStore.(*store).applyOriginsOnly(ctx, current, next, buildSnapshot(root, next, nil, false)); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled origin commit error = %v", err)
		}
		if _, err := os.Stat(journalPath(root)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled origin commit left journal: %v", err)
		}
	})

	t.Run("invalid layout for origin commit", func(t *testing.T) {
		parentFile := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(parentFile, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("write parent file: %v", err)
		}
		valueStore, err := New(Config{Root: filepath.Join(parentFile, "collection")})
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		if _, err := valueStore.(*store).applyOriginsOnly(
			context.Background(), []Item{{Name: "one.png"}}, []Item{{Name: "two.png"}}, Snapshot{},
		); err == nil {
			t.Fatal("origin-only commit accepted an invalid collection layout")
		}
	})

	if itemsEqual(nil, []Item{{Name: "extra.png"}}) {
		t.Fatal("item slices with different lengths compare equal")
	}
	valueStore, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := valueStore.(*store).scanApplyDirectory(
		context.Background(), filepath.Join(t.TempDir(), "missing"), nil, nil, nil,
	); err == nil {
		t.Fatal("missing staged directory scanned successfully")
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "art.png"), encodeInternalImage(t, 1, 1), 0o644); err != nil {
		t.Fatalf("write staged artwork: %v", err)
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatalf("read staged artwork: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := valueStore.(*store).scanApplyEntry(ctx, stage, entries[0], nil, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stage scan error = %v", err)
	}
	if _, err := readStableBatchSource(ctx, filepath.Join(stage, "art.png"), Item{
		Name: "art.png", Type: FileTypePNG, Width: 1, Height: 1,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stable read error = %v", err)
	}

	cleanupRoot := t.TempDir()
	if err := ensureLayout(cleanupRoot); err != nil {
		t.Fatalf("ensure cleanup layout: %v", err)
	}
	orphan := filepath.Join(cleanupRoot, controlDirectory, stagingName, "batch-orphan.png")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	if err := cleanupBatchStages(ctx, cleanupRoot); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cleanup error = %v", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("canceled cleanup removed orphan: %v", err)
	}

	target := filepath.Join(t.TempDir(), "target.png")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatalf("write recovery target: %v", err)
	}
	link := filepath.Join(t.TempDir(), "final.png")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("link recovery destination: %v", err)
	}
	if err := recoverBatchAddition(context.Background(), transactionEffect{Final: link}, []byte("target")); err == nil {
		t.Fatal("batch addition recovery accepted a symlink destination")
	}
}

func assertCollectionRootEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read collection root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("collection root is not empty: %v", entries)
	}
}

func assertBytesEqual(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("bytes at %s changed", path)
	}
}
