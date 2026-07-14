package collection_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func TestImportRejectsInvalidRequestAndCancellation(t *testing.T) {
	store := newStore(t, t.TempDir())
	data := encodeImage(t, "png", 1, 1)
	tests := []struct {
		name    string
		ctx     context.Context
		request collection.ImportRequest
	}{
		{name: "nil reader", ctx: context.Background(), request: collection.ImportRequest{Hint: "nil.png"}},
		{name: "negative limit", ctx: context.Background(), request: collection.ImportRequest{Reader: bytes.NewReader(data), Hint: "bad.png", MaxBytes: -1}},
		{name: "empty input", ctx: context.Background(), request: collection.ImportRequest{Reader: bytes.NewReader(nil), Hint: "empty.png"}},
		{name: "reader failure", ctx: context.Background(), request: collection.ImportRequest{Reader: errorReader{}, Hint: "error.png"}},
		{name: "canceled", ctx: canceledContext(), request: collection.ImportRequest{Reader: bytes.NewReader(data), Hint: "cancel.png"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := store.Import(tc.ctx, tc.request)
			if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
		})
	}
}

func TestImportRejectsRegisteredUnsupportedImageFormat(t *testing.T) {
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black})
	var data bytes.Buffer
	if err := gif.Encode(&data, img, nil); err != nil {
		t.Fatalf("encode GIF: %v", err)
	}
	store := newStore(t, t.TempDir())
	snapshot, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(data.Bytes()), Hint: "picture",
	})
	if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestImportWithNoFilenameUsesSafeDefault(t *testing.T) {
	store := newStore(t, t.TempDir())
	snapshot, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(encodeImage(t, "jpeg", 2, 2)), MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("import without hint: %v", err)
	}
	if !stringsHasPrefix(snapshot.Items[0].Name, "artwork-") {
		t.Fatalf("default name = %q", snapshot.Items[0].Name)
	}
}

func TestImportRejectsInvalidExistingEntries(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "artwork-shaped directory",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, "directory.png"), 0o755); err != nil {
					t.Fatalf("mkdir fake artwork: %v", err)
				}
			},
		},
		{
			name: "oversize artwork",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "large.jpg"), bytes.Repeat([]byte("x"), (1<<20)+1), 0o644); err != nil {
					t.Fatalf("write oversized artwork: %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			store := newStore(t, root)
			snapshot, err := store.Import(context.Background(), collection.ImportRequest{
				Reader: bytes.NewReader(encodeImage(t, "png", 1, 1)), Hint: "new.png",
			})
			if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
		})
	}
}

func TestImportRejectsInvalidManifest(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "malformed", data: "{"},
		{name: "unsupported version", data: `{"version":2,"generation":"x","items":[]}`},
		{name: "bad digest", data: `{"version":1,"generation":"x","items":[{"name":"x.png","digest":"xx"}]}`},
		{name: "bad generation", data: `{"version":1,"generation":"x","items":[]}`},
		{name: "unsafe name", data: `{"version":1,"generation":"x","items":[{"name":"../x.png","digest":"0000000000000000000000000000000000000000000000000000000000000000"}]}`},
		{name: "duplicate name", data: `{"version":1,"generation":"x","items":[{"name":"x.png","digest":"0000000000000000000000000000000000000000000000000000000000000000"},{"name":"x.png","digest":"1111111111111111111111111111111111111111111111111111111111111111"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			control := filepath.Join(root, ".frame-tv-art-manager")
			if err := os.Mkdir(control, 0o700); err != nil {
				t.Fatalf("mkdir control: %v", err)
			}
			if err := os.WriteFile(filepath.Join(control, "manifest.json"), []byte(tc.data), 0o600); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			store := newStore(t, root)
			snapshot, err := store.Import(context.Background(), collection.ImportRequest{
				Reader: bytes.NewReader(encodeImage(t, "png", 1, 1)), Hint: "new.png",
			})
			if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
		})
	}
}

func TestPrepareRejectsOversizedControlFilesWithoutReadingThem(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"manifest.json", "transaction.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			control := filepath.Join(root, ".frame-tv-art-manager")
			if err := os.Mkdir(control, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(control, name)
			if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, (16<<20)+1); err != nil {
				t.Fatal(err)
			}
			store := newStore(t, root)
			if _, err := store.Prepare(context.Background(), collection.PrepareRequest{}); err == nil ||
				!strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("Prepare() error = %v, want bounded control-file rejection", err)
			}
		})
	}
}

func TestImportRejectsInvalidRecoveryJournal(t *testing.T) {
	data := encodeImage(t, "png", 1, 2)
	digest := sha256.Sum256(data)
	digestText := hex.EncodeToString(digest[:])
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "malformed envelope", setup: func(t *testing.T, root string) { writeJournalBytes(t, root, []byte("{")) }},
		{name: "checksum mismatch", setup: func(t *testing.T, root string) {
			writeJournalEnvelope(t, root, []byte(`{"version":1}`), "wrong")
		}},
		{name: "malformed payload", setup: func(t *testing.T, root string) { writeJournalEnvelope(t, root, []byte(`"{"`), "") }},
		{name: "unsupported version", setup: func(t *testing.T, root string) {
			writeRecoveryPayload(t, root, data, digestText, 2, "normal", "normal")
		}},
		{name: "escaped final path", setup: func(t *testing.T, root string) {
			writeRecoveryPayload(t, root, data, digestText, 1, "normal", "escaped")
		}},
		{name: "short digest", setup: func(t *testing.T, root string) {
			writeRecoveryPayload(t, root, data, "abcd", 1, "normal", "normal")
		}},
		{name: "missing staged artwork", setup: func(t *testing.T, root string) {
			writeRecoveryPayload(t, root, data, digestText, 1, "missing", "normal")
		}},
		{name: "mismatched staged artwork", setup: func(t *testing.T, root string) {
			writeRecoveryPayload(t, root, data, digestText, 1, "mismatch", "normal")
		}},
		{name: "mismatched published artwork", setup: func(t *testing.T, root string) {
			writeRecoveryPayload(t, root, data, digestText, 1, "published-mismatch", "normal")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "collection")
			if err := os.MkdirAll(filepath.Join(root, ".frame-tv-art-manager", "staging"), 0o700); err != nil {
				t.Fatalf("mkdir layout: %v", err)
			}
			tc.setup(t, root)
			store := newStore(t, root)
			snapshot, err := store.Import(context.Background(), collection.ImportRequest{
				Reader: bytes.NewReader(data), Hint: "new.png",
			})
			if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("reader failed")
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func writeRecoveryPayload(t *testing.T, root string, data []byte, digest string, version int, stageState, finalState string) {
	t.Helper()
	stage := filepath.Join(root, ".frame-tv-art-manager", "staging", digest+".png")
	final := filepath.Join(root, "recover-"+digest[:min(len(digest), 12)]+".png")
	if finalState == "escaped" {
		final = filepath.Join(filepath.Dir(root), "escaped.png")
	}
	switch stageState {
	case "normal":
		writeArtwork(t, filepath.Dir(stage), filepath.Base(stage), data)
	case "mismatch":
		writeArtwork(t, filepath.Dir(stage), filepath.Base(stage), encodeImage(t, "png", 2, 2))
	case "published-mismatch":
		writeArtwork(t, root, filepath.Base(final), encodeImage(t, "png", 2, 2))
	}
	item := map[string]any{
		"name": filepath.Base(final), "digest": digest, "type": "png", "width": 1, "height": 2,
		"origin_key": "upload:" + digest, "class": "operator-upload",
	}
	payload, err := json.Marshal(map[string]any{
		"version": version, "stage": stage, "final": final, "digest": digest,
		"next_manifest": map[string]any{"version": 1, "generation": "generation", "items": []any{item}},
	})
	if err != nil {
		t.Fatalf("marshal recovery payload: %v", err)
	}
	writeJournalEnvelope(t, root, payload, "")
}

func writeJournalEnvelope(t *testing.T, root string, payload []byte, checksum string) {
	t.Helper()
	if checksum == "" {
		digest := sha256.Sum256(payload)
		checksum = hex.EncodeToString(digest[:])
	}
	envelope, err := json.Marshal(map[string]any{"payload": json.RawMessage(payload), "checksum": checksum})
	if err != nil {
		t.Fatalf("marshal recovery envelope: %v", err)
	}
	writeJournalBytes(t, root, envelope)
}

func writeJournalBytes(t *testing.T, root string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".frame-tv-art-manager", "transaction.json"), data, 0o600); err != nil {
		t.Fatalf("write recovery journal: %v", err)
	}
}

func stringsHasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}
