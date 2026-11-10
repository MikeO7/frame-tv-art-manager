package themes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T, dryRun bool) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewStore(root, dryRun)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store, root
}

func addChristmas(file *File) error {
	christmas, err := NewCollection("Christmas", "Holiday paintings", true,
		Activation{Mode: ModeWindow, Start: "12-01", End: "01-06"})
	if err != nil {
		return err
	}
	if _, err := christmas.AddMember(Member{Key: "tree.jpg"}); err != nil {
		return err
	}
	return file.Add(christmas)
}

func TestStoreMissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	store, root := newTestStore(t, false)
	file, err := store.Load(context.Background())
	if err != nil || len(file.Collections) != 0 || file.Version != fileVersion {
		t.Fatalf("Load() = %+v, %v, want empty document", file, err)
	}
	if store.Path() != filepath.Join(root, ".frame-tv-art-manager", fileName) {
		t.Fatalf("Path() = %q", store.Path())
	}
}

func TestStoreUpdatePublishesPrivateControlFile(t *testing.T) {
	t.Parallel()
	store, root := newTestStore(t, false)
	if _, err := store.Update(context.Background(), addChristmas); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	info, err := os.Stat(store.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("collections file mode = %v, %v, want 0600", info, err)
	}
	directory, err := os.Stat(filepath.Join(root, ".frame-tv-art-manager"))
	if err != nil || directory.Mode().Perm() != 0o700 {
		t.Fatalf("control directory mode = %v, %v, want 0700", directory, err)
	}
	file, err := store.Load(context.Background())
	if err != nil || len(file.Collections) != 1 || file.Collections[0].Slug != "christmas" ||
		file.Collections[0].Members[0].Key != "tree.jpg" {
		t.Fatalf("Load() after Update() = %+v, %v", file, err)
	}
	entries, err := os.ReadDir(filepath.Dir(store.Path()))
	if err != nil || len(entries) != 1 {
		t.Fatalf("control directory entries = %v, %v, want only the published file", entries, err)
	}
}

func TestStoreFailedChangeLeavesCommittedFileUntouched(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t, false)
	if _, err := store.Update(context.Background(), addChristmas); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("change refused")
	if _, err := store.Update(context.Background(), func(file *File) error {
		file.Collections = nil
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("Update() error = %v, want %v", err, wantErr)
	}
	if _, err := store.Update(context.Background(), func(file *File) error {
		file.Collections[0].Activation = Activation{Mode: ModeWindow, Start: "13-40", End: "01-01"}
		return nil
	}); err == nil || !strings.Contains(err.Error(), "validate updated collections") {
		t.Fatalf("Update(invalid) error = %v, want validation failure", err)
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || string(after) != string(before) {
		t.Fatalf("committed file changed after failed updates: %v", err)
	}
}

func TestStoreDryRunRefusesMutationWithoutWriting(t *testing.T) {
	t.Parallel()
	store, root := newTestStore(t, true)
	if _, err := store.Update(context.Background(), addChristmas); !errors.Is(err, ErrDryRun) {
		t.Fatalf("Update() error = %v, want ErrDryRun", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("dry-run Update() created %v, %v", entries, err)
	}
}

func TestStoreRejectsUnreadableFiles(t *testing.T) {
	t.Parallel()
	collectionJSON := func(slug, name, activation string) string {
		return `{"slug":"` + slug + `","name":"` + name + `","exclusive":true,"activation":` + activation + `,"members":[]}`
	}
	tests := []struct {
		name    string
		content string
		mode    os.FileMode
		want    string
	}{
		{"malformed JSON", `{"version":1,"collections":[`, 0o600, "decode collections.json"},
		{"unknown field", `{"version":1,"collections":[],"extra":true}`, 0o600, "unknown field"},
		{"trailing data", `{"version":1,"collections":[]} {}`, 0o600, "after the JSON document"},
		{"future version", `{"version":9,"collections":[]}`, 0o600, "unsupported collections file version"},
		{"missing list", `{"version":1}`, 0o600, "collections must be a list"},
		{
			"slug mismatch", `{"version":1,"collections":[` + collectionJSON("x", "Christmas", `{"mode":"off"}`) + `]}`,
			0o600, "does not match its name",
		},
		{
			"duplicate slug", `{"version":1,"collections":[` + collectionJSON("a", "A", `{"mode":"off"}`) + "," +
				collectionJSON("a", "a", `{"mode":"off"}`) + `]}`,
			0o600, "duplicated",
		},
		{
			"bad activation", `{"version":1,"collections":[` +
				collectionJSON("a", "A", `{"mode":"window","start":"12-01"}`) + `]}`,
			0o600, "needs both start and end",
		},
		{"world-readable", `{"version":1,"collections":[]}`, 0o644, "mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, _ := newTestStore(t, false)
			if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path(), []byte(test.content), test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(store.Path(), test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
			if _, err := store.Update(context.Background(), addChristmas); err == nil {
				t.Fatal("Update() rewrote an unreadable collections file")
			}
		})
	}
}

func TestNewStoreRequiresCanonicalRoot(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"", "relative/art", "/srv/art/../art"} {
		if _, err := NewStore(root, false); err == nil {
			t.Fatalf("NewStore(%q) succeeded", root)
		}
	}
}

func TestStoreHonorsCancellation(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t, false)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Update(canceled, addChristmas); !errors.Is(err, context.Canceled) {
		t.Fatalf("Update(canceled) error = %v, want cancellation", err)
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled Update() wrote the file: %v", err)
	}
}
