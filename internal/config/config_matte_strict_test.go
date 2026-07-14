package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadMatteConfigReturnsValidatedControlFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	raw := []byte(`{"_default":"none","monet.jpg":"shadowbox_polar"}`)
	if err := os.WriteFile(filepath.Join(directory, "mattes.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := ReadMatteConfig(context.Background(), directory)
	if err != nil {
		t.Fatalf("ReadMatteConfig() error = %v", err)
	}
	if config.DefaultMatte != "none" || config.Overrides["monet.jpg"] != "shadowbox_polar" || len(config.Overrides) != 1 {
		t.Fatalf("matte config = %+v", config)
	}
}

func TestReadMatteConfigMissingIsEmpty(t *testing.T) {
	t.Parallel()

	config, err := ReadMatteConfig(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("ReadMatteConfig() error = %v", err)
	}
	if config == nil || config.DefaultMatte != "" || len(config.Overrides) != 0 {
		t.Fatalf("missing matte config = %+v", config)
	}
}

func TestReadMatteConfigRejectsUnsafeControlFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		raw   []byte
		setup func(*testing.T, string)
	}{
		{name: "invalid JSON", raw: []byte(`{"a.jpg":`)},
		{name: "invalid UTF-8", raw: []byte{'{', '"', 0xff, '"', ':', '"', 'n', 'o', 'n', 'e', '"', '}'}},
		{name: "null", raw: []byte(`null`)},
		{name: "array", raw: []byte(`[]`)},
		{name: "trailing JSON", raw: []byte(`{} {}`)},
		{name: "duplicate default", raw: []byte(`{"_default":"none","_default":"modern"}`)},
		{name: "duplicate filename", raw: []byte(`{"a.jpg":"none","a.jpg":"modern"}`)},
		{name: "unsafe parent filename", raw: []byte(`{"../a.jpg":"none"}`)},
		{name: "unsafe nested filename", raw: []byte(`{"nested/a.jpg":"none"}`)},
		{name: "unsafe backslash filename", raw: []byte(`{"nested\\a.jpg":"none"}`)},
		{name: "blank filename", raw: []byte(`{"":"none"}`)},
		{name: "unnormalized filename", raw: []byte(`{" a.jpg":"none"}`)},
		{name: "blank matte", raw: []byte(`{"a.jpg":""}`)},
		{name: "unnormalized matte", raw: []byte(`{"a.jpg":" none"}`)},
		{name: "oversized matte", raw: []byte(`{"a.jpg":"` + strings.Repeat("m", 129) + `"}`)},
		{name: "non-string matte", raw: []byte(`{"a.jpg":1}`)},
		{name: "oversized file", raw: bytes.Repeat([]byte{' '}, (1<<20)+1)},
		{name: "directory", setup: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "actual-mattes.json")
			if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "mattes.json")
			if test.setup != nil {
				test.setup(t, path)
			} else if err := os.WriteFile(path, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}

			config, err := ReadMatteConfig(context.Background(), directory)
			if err == nil || config != nil {
				t.Fatalf("ReadMatteConfig() = %+v, %v, want error", config, err)
			}
		})
	}
}

func TestReadMatteConfigRejectsInvalidDirectoryOrCanceledContext(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	symlinkDirectory := filepath.Join(t.TempDir(), "artwork-link")
	if err := os.Symlink(directory, symlinkDirectory); err != nil {
		t.Fatal(err)
	}
	fileDirectory := filepath.Join(t.TempDir(), "artwork-file")
	if err := os.WriteFile(fileDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		ctx       context.Context
		directory string
		wantError error
	}{
		{name: "relative", ctx: context.Background(), directory: "artwork"},
		{name: "non-canonical", ctx: context.Background(), directory: filepath.Join(directory, ".") + string(os.PathSeparator)},
		{name: "symlink directory", ctx: context.Background(), directory: symlinkDirectory},
		{name: "file directory", ctx: context.Background(), directory: fileDirectory},
		{name: "nil context", directory: directory},
		{name: "canceled", ctx: canceledContext(), directory: directory, wantError: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ReadMatteConfig(test.ctx, test.directory)
			if err == nil || (test.wantError != nil && !errors.Is(err, test.wantError)) {
				t.Fatalf("ReadMatteConfig() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
