package collection_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
)

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []collection.Config{
		{},
		{Root: "relative"},
		{Root: t.TempDir(), MaxItems: -1},
		{Root: t.TempDir(), MaxImportBytes: -1},
		{Root: t.TempDir(), MaxPixels: -1},
	}
	for _, cfg := range tests {
		if store, err := collection.New(cfg); err == nil || store != nil {
			t.Fatalf("New(%+v) = (%T, %v), want error", cfg, store, err)
		}
	}
	if _, err := collection.New(collection.Config{Root: t.TempDir()}); err != nil {
		t.Fatalf("default limits: %v", err)
	}
}

func TestImportPreservesControlAndUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	mattesPath := filepath.Join(root, "mattes.json")
	notesPath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(mattesPath, []byte("operator control"), 0o600); err != nil {
		t.Fatalf("write matte control: %v", err)
	}
	if err := os.WriteFile(notesPath, []byte("operator notes"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	store := newStore(t, root)
	if _, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(encodeImage(t, "png", 2, 2)), Hint: "new.png",
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	assertContents(t, mattesPath, "operator control")
	assertContents(t, notesPath, "operator notes")
}

func TestImportRejectsUnsafeCollectionState(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "root symlink",
			setup: func(t *testing.T, root string) {
				t.Helper()
				target := t.TempDir()
				if err := os.Symlink(target, root); err != nil {
					t.Fatalf("symlink root: %v", err)
				}
			},
		},
		{
			name: "artwork symlink",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatalf("mkdir root: %v", err)
				}
				target := filepath.Join(t.TempDir(), "target.png")
				if err := os.WriteFile(target, encodeImage(t, "png", 1, 1), 0o644); err != nil {
					t.Fatalf("write target: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(root, "linked.png")); err != nil {
					t.Fatalf("symlink artwork: %v", err)
				}
			},
		},
		{
			name: "truncated existing artwork",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatalf("mkdir root: %v", err)
				}
				if err := os.WriteFile(filepath.Join(root, "broken.png"), []byte("not-png"), 0o644); err != nil {
					t.Fatalf("write broken artwork: %v", err)
				}
			},
		},
		{
			name: "control path is a file",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatalf("mkdir root: %v", err)
				}
				if err := os.WriteFile(filepath.Join(root, ".frame-tv-art-manager"), []byte("blocked"), 0o600); err != nil {
					t.Fatalf("write control blocker: %v", err)
				}
			},
		},
		{
			name: "control directory symlink",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatalf("mkdir root: %v", err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(root, ".frame-tv-art-manager")); err != nil {
					t.Fatalf("symlink control: %v", err)
				}
			},
		},
		{
			name: "staging directory symlink",
			setup: func(t *testing.T, root string) {
				t.Helper()
				control := filepath.Join(root, ".frame-tv-art-manager")
				if err := os.MkdirAll(control, 0o700); err != nil {
					t.Fatalf("mkdir control: %v", err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(control, "staging")); err != nil {
					t.Fatalf("symlink staging: %v", err)
				}
			},
		},
		{
			name: "journal symlink",
			setup: func(t *testing.T, root string) {
				t.Helper()
				control := filepath.Join(root, ".frame-tv-art-manager")
				if err := os.MkdirAll(filepath.Join(control, "staging"), 0o700); err != nil {
					t.Fatalf("mkdir control: %v", err)
				}
				target := filepath.Join(t.TempDir(), "journal")
				if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
					t.Fatalf("write journal target: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(control, "transaction.json")); err != nil {
					t.Fatalf("symlink journal: %v", err)
				}
			},
		},
		{
			name: "manifest symlink",
			setup: func(t *testing.T, root string) {
				t.Helper()
				control := filepath.Join(root, ".frame-tv-art-manager")
				if err := os.MkdirAll(filepath.Join(control, "staging"), 0o700); err != nil {
					t.Fatalf("mkdir control: %v", err)
				}
				target := filepath.Join(t.TempDir(), "manifest")
				if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
					t.Fatalf("write manifest target: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(control, "manifest.json")); err != nil {
					t.Fatalf("symlink manifest: %v", err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "art")
			tc.setup(t, root)
			store := newStore(t, root)
			snapshot, err := store.Import(context.Background(), collection.ImportRequest{
				Reader: bytes.NewReader(encodeImage(t, "png", 1, 1)), Hint: "safe.png",
			})
			if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
				t.Fatalf("snapshot=%+v err=%v", snapshot, err)
			}
		})
	}
}

func TestImportUsesDeterministicCollisionNameAndSortedSnapshot(t *testing.T) {
	root := t.TempDir()
	input := encodeImage(t, "png", 3, 3)
	digest := sha256.Sum256(input)
	conflictingName := "photo--" + hex.EncodeToString(digest[:6]) + ".png"
	writeArtwork(t, root, conflictingName, encodeImage(t, "png", 1, 1))
	writeArtwork(t, root, "zebra.jpg", encodeImage(t, "jpeg", 2, 1))

	store := newStore(t, root)
	snapshot, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(input), Hint: "photo.png",
	})
	if err != nil {
		t.Fatalf("import collision: %v", err)
	}
	wantPrefix := "photo--" + hex.EncodeToString(digest[:8])
	if !strings.HasPrefix(snapshot.Changes[0].Name, wantPrefix) {
		t.Fatalf("collision name = %q, want prefix %q", snapshot.Changes[0].Name, wantPrefix)
	}
	names := make([]string, len(snapshot.Items))
	for index, item := range snapshot.Items {
		names[index] = item.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("snapshot not sorted: %v", names)
	}
}

func TestImportSnapshotDoesNotAliasLaterResults(t *testing.T) {
	store := newStore(t, t.TempDir())
	data := encodeImage(t, "png", 2, 2)
	first, err := store.Import(context.Background(), collection.ImportRequest{Reader: bytes.NewReader(data), Hint: "one.png"})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	wantName := first.Items[0].Name
	first.Items[0].Name = "mutated.png"
	first.Changes[0].Name = "mutated.png"
	first.Warnings = append(first.Warnings, "mutated")
	second, err := store.Import(context.Background(), collection.ImportRequest{Reader: bytes.NewReader(data), Hint: "two.png"})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Items[0].Name != wantName || len(second.Warnings) != 0 {
		t.Fatalf("snapshot alias leaked: %+v", second)
	}
}

func TestImportEnforcesCollectionLimitWithoutPublishing(t *testing.T) {
	root := t.TempDir()
	store, err := collection.New(collection.Config{Root: root, MaxItems: 1, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	first := encodeImage(t, "png", 1, 1)
	if _, err := store.Import(context.Background(), collection.ImportRequest{Reader: bytes.NewReader(first), Hint: "one.png"}); err != nil {
		t.Fatalf("first import: %v", err)
	}
	before := directoryFiles(t, root)
	snapshot, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(encodeImage(t, "png", 2, 2)), Hint: "two.png",
	})
	if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if after := directoryFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("limit failure mutated collection: before=%v after=%v", before, after)
	}
}

func TestImportHonorsCancellationWhileWaitingForMutation(t *testing.T) {
	store := newStore(t, t.TempDir())
	data := encodeImage(t, "png", 2, 2)
	reader := &gatedReader{data: data, started: make(chan struct{}), release: make(chan struct{})}
	firstDone := make(chan error, 1)
	go func() {
		_, err := store.Import(context.Background(), collection.ImportRequest{Reader: reader, Hint: "one.png"})
		firstDone <- err
	}()
	<-reader.started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	snapshot, err := store.Import(ctx, collection.ImportRequest{Reader: bytes.NewReader(data), Hint: "two.png"})
	if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	close(reader.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first import: %v", err)
	}
}

func TestImportRecoversDurableTransaction(t *testing.T) {
	data := encodeImage(t, "png", 2, 3)
	for _, state := range []string{"staged", "published", "linked"} {
		t.Run(state, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "recovery")
			prepareInterruptedImport(t, root, data, state)
			store := newStore(t, root)
			snapshot, err := store.Import(context.Background(), collection.ImportRequest{
				Reader: bytes.NewReader(data), Hint: "again.png",
			})
			if err != nil {
				t.Fatalf("recover import: %v", err)
			}
			if len(snapshot.Items) != 1 || snapshot.Changes[0].Kind != collection.ChangeDuplicate {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			if _, err := os.Stat(filepath.Join(root, ".frame-tv-art-manager", "transaction.json")); !os.IsNotExist(err) {
				t.Fatalf("journal remains: %v", err)
			}
		})
	}
}

func TestDryRunRefusesRecoveryWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "recovery")
	data := encodeImage(t, "png", 2, 3)
	prepareInterruptedImport(t, root, data, "staged")
	before := directoryFiles(t, root)
	store := newStore(t, root)
	snapshot, err := store.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(data), Hint: "preview.png", DryRun: true,
	})
	if err == nil || !reflect.DeepEqual(snapshot, collection.Snapshot{}) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if after := directoryFiles(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("dry run recovered transaction: before=%v after=%v", before, after)
	}
}

type gatedReader struct {
	data    []byte
	started chan struct{}
	release chan struct{}
	once    bool
}

func (r *gatedReader) Read(target []byte) (int, error) {
	if !r.once {
		r.once = true
		close(r.started)
		<-r.release
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	count := copy(target, r.data)
	r.data = r.data[count:]
	return count, nil
}

func prepareInterruptedImport(t *testing.T, root string, data []byte, state string) {
	t.Helper()
	templateRoot := t.TempDir()
	template := newStore(t, templateRoot)
	templateSnapshot, err := template.Import(context.Background(), collection.ImportRequest{
		Reader: bytes.NewReader(data), Hint: "recovered.png",
	})
	if err != nil {
		t.Fatalf("create transaction template: %v", err)
	}
	manifestData, err := os.ReadFile(filepath.Join(templateRoot, ".frame-tv-art-manager", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest template: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".frame-tv-art-manager", "staging"), 0o700); err != nil {
		t.Fatalf("create recovery layout: %v", err)
	}
	digest := sha256.Sum256(data)
	digestText := hex.EncodeToString(digest[:])
	stage := filepath.Join(root, ".frame-tv-art-manager", "staging", digestText+".png")
	final := filepath.Join(root, templateSnapshot.Items[0].Name)
	writeArtwork(t, filepath.Dir(stage), filepath.Base(stage), data)
	switch state {
	case "published":
		if err := os.Rename(stage, final); err != nil {
			t.Fatalf("publish interrupted artwork: %v", err)
		}
	case "linked":
		if err := os.Link(stage, final); err != nil {
			t.Fatalf("link interrupted artwork: %v", err)
		}
	}
	var next json.RawMessage
	if err := json.Unmarshal(manifestData, &next); err != nil {
		t.Fatalf("decode manifest template: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"version": 1, "stage": stage, "final": final, "digest": digestText, "next_manifest": next,
	})
	if err != nil {
		t.Fatalf("encode transaction: %v", err)
	}
	checksum := sha256.Sum256(payload)
	envelope, err := json.Marshal(map[string]any{"payload": json.RawMessage(payload), "checksum": hex.EncodeToString(checksum[:])})
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".frame-tv-art-manager", "transaction.json"), envelope, 0o600); err != nil {
		t.Fatalf("write transaction: %v", err)
	}
}

func writeArtwork(t *testing.T, root, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir artwork root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
		t.Fatalf("write artwork: %v", err)
	}
}

func directoryFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk directory: %v", err)
	}
	return files
}

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("contents %s = %q, want %q", path, data, want)
	}
}
