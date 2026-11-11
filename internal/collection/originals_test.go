package collection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// keptOriginalFixture is a committed derivative whose raw source was kept as
// its original by Apply.
type keptOriginalFixture struct {
	root     string
	store    *store
	rawPath  string
	raw      []byte
	derived  []byte
	original Original
}

func newKeptOriginalFixture(t *testing.T) keptOriginalFixture {
	t.Helper()
	root := t.TempDir()
	value, err := New(Config{Root: root, MaxImportBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fixture := keptOriginalFixture{
		root: root, store: value.(*store),
		raw: encodeInternalImage(t, 2, 2), derived: encodeInternalImage(t, 3, 2),
	}
	seed, err := value.Import(context.Background(), ImportRequest{Reader: bytes.NewReader(fixture.raw), Hint: "raw.png"})
	if err != nil {
		t.Fatal(err)
	}
	fixture.rawPath = seed.Items[0].Path
	fixture.original = Original{Digest: sha256.Sum256(fixture.raw), Type: FileTypePNG}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "derived.png"), fixture.derived, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := value.Apply(context.Background(), ApplyRequest{
		Directory: stage,
		Metadata: map[string]ItemMetadata{"derived.png": {
			Key: seed.Items[0].Key, Origin: seed.Items[0].Origin, TransformKey: strings.Repeat("a", 64),
			Derivative: DerivativeOptimized, Originals: []Original{fixture.original},
		}},
	}); err != nil {
		t.Fatalf("apply derivative: %v", err)
	}
	return fixture
}

func (fixture keptOriginalFixture) prepare(t *testing.T) Snapshot {
	t.Helper()
	snapshot, err := fixture.store.Prepare(context.Background(), PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return snapshot
}

func (fixture keptOriginalFixture) requireKept(t *testing.T) {
	t.Helper()
	path := OriginalPath(fixture.root, fixture.original)
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, fixture.raw) {
		t.Fatalf("kept original = %d bytes, %v; want the raw source", len(data), err)
	}
}

func TestApplyKeepsOriginalBeforeRemovingItsSource(t *testing.T) {
	fixture := newKeptOriginalFixture(t)
	fixture.requireKept(t)
	info, err := os.Stat(OriginalPath(fixture.root, fixture.original))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("original mode = %v, %v; want 0600", info, err)
	}
	directory, err := os.Stat(originalsDirectory(fixture.root))
	if err != nil || directory.Mode().Perm() != 0o700 {
		t.Fatalf("originals directory mode = %v, %v; want 0700", directory, err)
	}
	if _, err := os.Stat(fixture.rawPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw source remains beside its derivative: %v", err)
	}

	// Valid images inside the control directory are never cataloged.
	for _, path := range []string{
		filepath.Join(originalsDirectory(fixture.root), "stray.png"),
		filepath.Join(fixture.root, controlDirectory, "stray.png"),
	} {
		if err := os.WriteFile(path, encodeInternalImage(t, 4, 4), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := New(Config{Root: fixture.root, MaxImportBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Prepare(context.Background(), PrepareRequest{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Name != "derived.png" ||
		!slices.Equal(snapshot.Items[0].Originals, []Original{fixture.original}) {
		t.Fatalf("snapshot = %+v, want only the derivative with its original", snapshot.Items)
	}
	fixture.requireKept(t)
	if _, err := os.Stat(filepath.Join(originalsDirectory(fixture.root), "stray.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced originals entry was not collected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, controlDirectory, "stray.png")); err != nil {
		t.Fatalf("collection touched a control file outside originals: %v", err)
	}
}

func TestApplyRefusesLineageWhoseOriginalIsUnavailable(t *testing.T) {
	root := t.TempDir()
	value, err := New(Config{Root: root, MaxImportBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeInternalImage(t, 2, 2)
	seed, err := value.Import(context.Background(), ImportRequest{Reader: bytes.NewReader(raw), Hint: "raw.png"})
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "derived.png"), encodeInternalImage(t, 3, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := Original{Digest: sha256.Sum256([]byte("never seen")), Type: FileTypePNG}
	_, err = value.Apply(context.Background(), ApplyRequest{
		Directory: stage,
		Metadata: map[string]ItemMetadata{"derived.png": {
			Key: seed.Items[0].Key, Origin: seed.Items[0].Origin, TransformKey: strings.Repeat("a", 64),
			Derivative: DerivativeOptimized, Originals: []Original{missing},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "neither kept nor present") {
		t.Fatalf("Apply() error = %v, want unavailable original", err)
	}
	assertTestFile(t, seed.Items[0].Path, string(raw))
	if _, err := os.Stat(filepath.Join(root, "derived.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("derivative published without its original: %v", err)
	}
}

func TestCopyOriginalNeverPublishesPartialBytes(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	source := []byte("original bytes")
	tests := []struct {
		name   string
		ctx    context.Context
		digest [sha256.Size]byte
		want   error
	}{
		{name: "bytes changed", ctx: context.Background(), digest: sha256.Sum256([]byte("other bytes"))},
		{name: "canceled", ctx: canceled, digest: sha256.Sum256(source), want: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			sourcePath := filepath.Join(directory, "source.png")
			if err := os.WriteFile(sourcePath, source, 0o644); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(directory, "originals", "kept.png")
			if err := os.Mkdir(filepath.Dir(destination), 0o700); err != nil {
				t.Fatal(err)
			}
			err := copyOriginal(test.ctx, sourcePath, destination, test.digest)
			if err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("copyOriginal() error = %v, want %v", err, test.want)
			}
			entries, readErr := os.ReadDir(filepath.Dir(destination))
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("failed copy left %v, %v", entries, readErr)
			}
		})
	}
}

func TestPrepareCollectsOnlyUnreferencedOriginals(t *testing.T) {
	fixture := newKeptOriginalFixture(t)
	directory := originalsDirectory(fixture.root)
	unreferenced := hex.EncodeToString(make([]byte, sha256.Size)) + ".png"
	for _, name := range []string{".durable-123.tmp", unreferenced} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.prepare(t)
	fixture.requireKept(t)
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != fixture.original.Name() {
		t.Fatalf("originals after collection = %v, %v; want only the referenced original", entries, err)
	}

	if err := os.Mkdir(filepath.Join(directory, "unexpected"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Prepare(context.Background(), PrepareRequest{}); err == nil {
		t.Fatal("Prepare() accepted a directory inside originals")
	}
	fixture.requireKept(t)
}

func TestPrepareDryRunNeverCollectsOriginals(t *testing.T) {
	fixture := newKeptOriginalFixture(t)
	if err := os.Remove(filepath.Join(fixture.root, "derived.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Prepare(context.Background(), PrepareRequest{DryRun: true}); err != nil {
		t.Fatalf("dry-run prepare: %v", err)
	}
	fixture.requireKept(t)
	if snapshot := fixture.prepare(t); len(snapshot.Items) != 0 {
		t.Fatalf("deleted derivative remains: %+v", snapshot.Items)
	}
	if _, err := os.Stat(OriginalPath(fixture.root, fixture.original)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original of committed delete was not collected: %v", err)
	}
}

func TestPrepareKeepsLineageWhileRecordedArtworkIsUnreadable(t *testing.T) {
	fixture := newKeptOriginalFixture(t)
	path := filepath.Join(fixture.root, "derived.png")
	if err := os.WriteFile(path, []byte("temporarily unreadable"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := fixture.prepare(t)
	if len(snapshot.Items) != 0 || len(snapshot.Warnings) != 1 {
		t.Fatalf("snapshot = %+v, want the unreadable artwork excluded with a warning", snapshot)
	}
	fixture.requireKept(t)

	if err := os.WriteFile(path, fixture.derived, 0o644); err != nil {
		t.Fatal(err)
	}
	recovered := fixture.prepare(t)
	if len(recovered.Items) != 1 || !slices.Equal(recovered.Items[0].Originals, []Original{fixture.original}) ||
		recovered.Items[0].Derivative != DerivativeOptimized {
		t.Fatalf("recovered items = %+v, want the committed lineage", recovered.Items)
	}
	fixture.requireKept(t)
}

func TestOriginalsLayoutRejectsSymlinkedDirectory(t *testing.T) {
	fixture := newKeptOriginalFixture(t)
	directory := originalsDirectory(fixture.root)
	moved := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Prepare(context.Background(), PrepareRequest{}); err == nil ||
		!strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("Prepare() error = %v, want symlinked originals rejected", err)
	}
}

func TestOriginalLineageValidation(t *testing.T) {
	first := Original{Digest: sha256.Sum256([]byte("first")), Type: FileTypeJPEG}
	second := Original{Digest: sha256.Sum256([]byte("second")), Type: FileTypePNG}
	third := Original{Digest: sha256.Sum256([]byte("third")), Type: FileTypePNG}
	tests := []struct {
		name  string
		item  Item
		valid bool
	}{
		{name: "raw artwork without originals", item: Item{}, valid: true},
		{name: "optimized with one", item: Item{Derivative: DerivativeOptimized, Originals: []Original{first}}, valid: true},
		{name: "collage with its two portraits", item: Item{Derivative: DerivativeCollage, Originals: []Original{first, second}}, valid: true},
		{name: "legacy collage with one", item: Item{Derivative: DerivativeCollage, Originals: []Original{first}}, valid: true},
		{name: "raw artwork with an original", item: Item{Originals: []Original{first}}},
		{name: "optimized with two", item: Item{Derivative: DerivativeOptimized, Originals: []Original{first, second}}},
		{name: "collage with three", item: Item{Derivative: DerivativeCollage, Originals: []Original{first, second, third}}},
		{name: "repeated original", item: Item{Derivative: DerivativeCollage, Originals: []Original{first, first}}},
		{name: "empty digest", item: Item{Derivative: DerivativeOptimized, Originals: []Original{{Type: FileTypePNG}}}},
		{name: "unsupported type", item: Item{Derivative: DerivativeOptimized, Originals: []Original{{Digest: first.Digest, Type: "gif"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateOriginals(test.item); (err == nil) != test.valid {
				t.Fatalf("validateOriginals() error = %v, want valid %v", err, test.valid)
			}
		})
	}

	encoded := hex.EncodeToString(first.Digest[:])
	for name, valid := range map[string]bool{
		encoded + ".jpg":                  true,
		strings.ToUpper(encoded) + ".jpg": false,
		encoded:                           false,
		encoded + ".gif":                  false,
		"short.png":                       false,
	} {
		parsed, err := parseOriginalName(name)
		if (err == nil) != valid || (valid && parsed != first) {
			t.Fatalf("parseOriginalName(%q) = %+v, %v; want valid %v", name, parsed, err, valid)
		}
	}
	if _, err := parseOriginalNames("item.png", []string{"short.png"}); err == nil {
		t.Fatal("manifest accepted an invalid original name")
	}
}
