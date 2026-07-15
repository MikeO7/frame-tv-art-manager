package collection_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func TestImportPublishesValidatedArtworkAndManifest(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	data := encodeImage(t, "png", 3, 2)

	snapshot, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(data),
		Hint:   "My Summer!.PNG",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(snapshot.Items) != 1 || len(snapshot.Changes) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	item := snapshot.Items[0]
	wantDigest := sha256.Sum256(data)
	if item.Digest != wantDigest || item.Type != collection.FileTypePNG || item.Width != 3 || item.Height != 2 {
		t.Fatalf("item = %+v", item)
	}
	if !strings.HasPrefix(item.Name, "my-summer-") || filepath.Dir(item.Path) != root {
		t.Fatalf("unsafe or unexpected item = %+v", item)
	}
	assertMode(t, item.Path, 0o644)
	assertMode(t, filepath.Join(root, ".frame-tv-art-manager"), 0o700)
	assertMode(t, filepath.Join(root, ".frame-tv-art-manager", "staging"), 0o700)
	assertMode(t, filepath.Join(root, ".frame-tv-art-manager", "manifest.json"), 0o600)
	if _, err := os.Stat(filepath.Join(root, ".frame-tv-art-manager", "transaction.json")); !os.IsNotExist(err) {
		t.Fatalf("transaction journal remains: %v", err)
	}
}

func TestImportBatchCommitsOrderedImportsTogether(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	first := encodeImage(t, "png", 3, 2)
	second := encodeImage(t, "jpeg", 4, 3)
	snapshot, err := store.ImportBatch(context.Background(), []collection.ImportRequest{
		{Reader: bytes.NewReader(first), Hint: "first.png", Origin: collection.Origin{Key: "source:first", Class: collection.OriginSource}},
		{Reader: bytes.NewReader(second), Hint: "second.jpg", Origin: collection.Origin{Key: "source:second", Class: collection.OriginSource}},
		{Reader: bytes.NewReader(first), Hint: "again.png", Origin: collection.Origin{Key: "source:first-copy", Class: collection.OriginSource}},
	})
	if err != nil {
		t.Fatalf("ImportBatch() error = %v", err)
	}
	if len(snapshot.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(snapshot.Items))
	}
	wantKinds := []collection.ChangeKind{collection.ChangeAdded, collection.ChangeAdded, collection.ChangeAdopted}
	if len(snapshot.Changes) != len(wantKinds) {
		t.Fatalf("changes = %+v", snapshot.Changes)
	}
	for index, kind := range wantKinds {
		if snapshot.Changes[index].Kind != kind {
			t.Fatalf("change %d = %s, want %s", index, snapshot.Changes[index].Kind, kind)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".frame-tv-art-manager", "transaction.json")); !os.IsNotExist(err) {
		t.Fatalf("transaction journal remains: %v", err)
	}
}

func TestImportBatchRejectsInvalidInputWithoutMutation(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	valid := encodeImage(t, "png", 3, 2)
	_, err := store.ImportBatch(context.Background(), []collection.ImportRequest{
		{Reader: bytes.NewReader(valid), Hint: "valid.png"},
		{Reader: strings.NewReader("not an image"), Hint: "invalid.png"},
	})
	if !errors.Is(err, collection.ErrInvalidImport) {
		t.Fatalf("ImportBatch() error = %v, want ErrInvalidImport", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed batch mutated collection: %v", entries)
	}
}

func TestImportBatchRequestValidationAndDryRun(t *testing.T) {
	data := encodeImage(t, "png", 3, 2)

	t.Run("empty", func(t *testing.T) {
		store := newStore(t, t.TempDir())
		if _, err := store.ImportBatch(context.Background(), nil); err == nil {
			t.Fatal("ImportBatch() accepted an empty batch")
		}
	})
	t.Run("nil reader", func(t *testing.T) {
		store := newStore(t, t.TempDir())
		if _, err := store.ImportBatch(context.Background(), []collection.ImportRequest{{Hint: "nil.png"}}); err == nil {
			t.Fatal("ImportBatch() accepted a nil reader")
		}
	})
	t.Run("mixed dry run", func(t *testing.T) {
		store := newStore(t, t.TempDir())
		_, err := store.ImportBatch(context.Background(), []collection.ImportRequest{
			{Reader: bytes.NewReader(data), Hint: "one.png", DryRun: true},
			{Reader: bytes.NewReader(data), Hint: "two.png"},
		})
		if err == nil {
			t.Fatal("ImportBatch() accepted mixed dry-run modes")
		}
	})
	t.Run("dry run", func(t *testing.T) {
		root := t.TempDir()
		store := newStore(t, root)
		snapshot, err := store.ImportBatch(context.Background(), []collection.ImportRequest{
			{Reader: bytes.NewReader(data), Hint: "one.png", DryRun: true},
		})
		if err != nil || !snapshot.DryRun || len(snapshot.Changes) != 1 {
			t.Fatalf("ImportBatch() = %+v, %v", snapshot, err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("dry run mutated collection: %v, %v", entries, err)
		}
	})
	t.Run("invalid origin", func(t *testing.T) {
		store := newStore(t, t.TempDir())
		_, err := store.ImportBatch(context.Background(), []collection.ImportRequest{{
			Reader: bytes.NewReader(data), Hint: "one.png",
			Origin: collection.Origin{Key: "bad", Class: collection.OriginClass("invalid")},
		}})
		if err == nil || !strings.Contains(err.Error(), "origin") {
			t.Fatalf("ImportBatch() error = %v", err)
		}
	})
}

func TestImportBatchHandlesDuplicateOnlyAndItemLimit(t *testing.T) {
	data := encodeImage(t, "png", 3, 2)
	root := t.TempDir()
	store := newStore(t, root)
	if _, err := store.Import(context.Background(), collection.ImportRequest{Reader: bytes.NewReader(data), Hint: "one.png"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ImportBatch(context.Background(), []collection.ImportRequest{
		{Reader: bytes.NewReader(data), Hint: "duplicate.png"},
	})
	if err != nil || len(snapshot.Items) != 1 || snapshot.Changes[0].Kind != collection.ChangeDuplicate {
		t.Fatalf("duplicate-only batch = %+v, %v", snapshot, err)
	}

	limited, err := collection.New(collection.Config{Root: t.TempDir(), MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	second := encodeImage(t, "jpeg", 4, 3)
	_, err = limited.ImportBatch(context.Background(), []collection.ImportRequest{
		{Reader: bytes.NewReader(data), Hint: "one.png"},
		{Reader: bytes.NewReader(second), Hint: "two.jpg"},
	})
	if err == nil || !strings.Contains(err.Error(), "item limit") {
		t.Fatalf("ImportBatch() error = %v, want item limit", err)
	}
}

func TestImportDeduplicatesByFullDigest(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	data := encodeImage(t, "jpeg", 4, 3)
	first, err := store.Import(context.Background(), collection.ImportRequest{Reader: bytes.NewReader(data), Hint: "first.jpg"})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	second, err := store.Import(context.Background(), collection.ImportRequest{Reader: bytes.NewReader(data), Hint: "second.jpg"})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Name != first.Items[0].Name {
		t.Fatalf("duplicate changed collection: first=%+v second=%+v", first, second)
	}
	if len(second.Changes) != 1 || second.Changes[0].Kind != collection.ChangeDuplicate {
		t.Fatalf("changes = %+v", second.Changes)
	}
	if second.Generation != first.Generation {
		t.Fatalf("generation changed for duplicate: %q != %q", second.Generation, first.Generation)
	}
}

func TestReuploadOriginalBytesDeduplicatesAgainstCurrentDerivative(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	original := encodeImage(t, "png", 3, 2)
	imported, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(original), Hint: "vacation.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	const derivativeName = "vacation-transformed.png"
	if err := os.WriteFile(filepath.Join(stage, derivativeName), encodeImage(t, "png", 4, 3), 0o644); err != nil {
		t.Fatal(err)
	}
	item := imported.Items[0]
	transformed, err := store.Apply(context.Background(), collection.ApplyRequest{
		Directory: stage,
		Metadata: map[string]collection.ItemMetadata{derivativeName: {
			Key: item.Key, Origin: item.Origin, TransformKey: strings.Repeat("a", sha256.Size*2),
			Derivative: collection.DerivativeOptimized,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reuploaded, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(original), Hint: "vacation-again.png",
	})
	if err != nil {
		t.Fatalf("repeat original upload: %v", err)
	}
	if len(reuploaded.Items) != 1 || reuploaded.Items[0].Name != transformed.Items[0].Name ||
		len(reuploaded.Changes) != 1 || reuploaded.Changes[0].Kind != collection.ChangeDuplicate {
		t.Fatalf("re-upload result = %+v", reuploaded)
	}
}

func TestSourceImportCommitsOriginAndAdoptsExactOperatorDigestWithoutClobber(t *testing.T) {
	root := t.TempDir()
	data := encodeImage(t, "jpeg", 4, 3)
	operatorName := "operator-original.jpg"
	if err := os.WriteFile(filepath.Join(root, operatorName), data, 0o644); err != nil {
		t.Fatalf("write operator artwork: %v", err)
	}
	store := newStore(t, root)
	baseline, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil || len(baseline.Items) != 1 || baseline.Items[0].Origin.Class != collection.OriginOperator {
		t.Fatalf("prepare operator baseline = %+v, %v", baseline, err)
	}

	origin := collection.Origin{Key: "source:direct__stable", Class: collection.OriginSource}
	imported, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(data), Hint: "001__direct__stable.jpg", Origin: origin,
	})
	if err != nil {
		t.Fatalf("import source association: %v", err)
	}
	if len(imported.Items) != 1 || imported.Items[0].Name != operatorName || imported.Items[0].Origin != origin {
		t.Fatalf("source association changed operator bytes or origin: %+v", imported)
	}
	if imported.Items[0].Key != operatorName || !reflect.DeepEqual(imported.Items[0].SourceKeys, []string{origin.Key}) {
		t.Fatalf("source association lost stable artwork metadata: %+v", imported.Items[0])
	}
	if len(imported.Changes) != 1 || imported.Changes[0].Kind != collection.ChangeAdopted {
		t.Fatalf("source association changes = %+v", imported.Changes)
	}
	stable, err := store.Prepare(context.Background(), collection.PrepareRequest{})
	if err != nil || len(stable.Items) != 1 || stable.Items[0].Origin != origin ||
		stable.Items[0].Key != operatorName || !reflect.DeepEqual(stable.Items[0].SourceKeys, []string{origin.Key}) {
		t.Fatalf("committed source origin = %+v, %v", stable, err)
	}
}

func TestImportDryRunHasNoFilesystemMutation(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "absent-artwork")
	store := newStore(t, root)

	snapshot, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(encodeImage(t, "png", 2, 2)),
		Hint:   "preview.png",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry-run import: %v", err)
	}
	if !snapshot.DryRun || len(snapshot.Items) != 0 || len(snapshot.Changes) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("dry run mutated filesystem: %v", err)
	}
}

func TestImportRejectsUnsafeOrInvalidInputWithoutPublishing(t *testing.T) {
	tests := []struct {
		name    string
		hint    string
		data    []byte
		request func(*collection.ImportRequest)
	}{
		{name: "reserved", hint: "mattes.json", data: encodeImage(t, "png", 1, 1)},
		{name: "apple double", hint: "._photo.png", data: encodeImage(t, "png", 1, 1)},
		{name: "path traversal", hint: "../photo.png", data: encodeImage(t, "png", 1, 1)},
		{name: "unsupported extension", hint: "photo.gif", data: encodeImage(t, "png", 1, 1)},
		{name: "format mismatch", hint: "photo.jpg", data: encodeImage(t, "png", 1, 1)},
		{name: "unsupported bytes", hint: "photo.png", data: []byte("not an image")},
		{name: "truncated", hint: "photo.png", data: encodeImage(t, "png", 8, 8)[:30]},
		{name: "too many bytes", hint: "photo.png", data: encodeImage(t, "png", 2, 2), request: func(r *collection.ImportRequest) { r.MaxBytes = 4 }},
		{name: "too many pixels", hint: "photo.png", data: encodeImage(t, "png", 2, 2), request: func(r *collection.ImportRequest) { r.Policy.MaxPixels = 3 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := newStore(t, root)
			request := collection.ImportRequest{Reader: bytes.NewReader(tc.data), Hint: tc.hint}
			if tc.request != nil {
				tc.request(&request)
			}
			snapshot, err := store.Import(context.Background(), request)
			if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil {
				t.Fatalf("read root: %v", readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("invalid input published entries: %+v", entries)
			}
		})
	}
}

func TestImportReaderIsStrictlyBounded(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	reader := &countingReader{remaining: 1 << 20}
	_, err := store.Import(context.Background(), collection.ImportRequest{Reader: reader, Hint: "large.jpg", MaxBytes: 32})
	if err == nil {
		t.Fatal("expected bounded import error")
	}
	if reader.read > 33 {
		t.Fatalf("read %d bytes, want at most 33", reader.read)
	}
}

type countingReader struct {
	remaining int
	read      int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = 'x'
	}
	r.remaining -= len(p)
	r.read += len(p)
	return len(p), nil
}

func newStore(t *testing.T, root string) collection.Store {
	t.Helper()
	store, err := collection.New(collection.Config{Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func encodeImage(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: uint8(x + 1), G: uint8(y + 2), B: 80, A: 255})
		}
	}
	var output bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&output, img)
	} else {
		err = jpeg.Encode(&output, img, &jpeg.Options{Quality: 90})
	}
	if err != nil {
		t.Fatalf("encode image: %v", err)
	}
	return output.Bytes()
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %o, want %o", path, got, want)
	}
}
