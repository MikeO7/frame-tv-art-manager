package durablefs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplacePublishesCompleteFileWithExactMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	err := Replace(context.Background(), path, 0o600, func(writer io.Writer) error {
		_, writeErr := io.WriteString(writer, "new-state")
		return writeErr
	})
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new-state" {
		t.Fatalf("target = %q, want new-state", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("target mode = %o, want 0600", info.Mode().Perm())
	}
	assertNoTemporaryFiles(t, dir)
}

func TestReadStableReadsBoundedRegularFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	if err := os.WriteFile(path, []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadStable(context.Background(), path, StableReadOptions{MaxBytes: 6, RequiredMode: 0o600})
	if err != nil {
		t.Fatalf("ReadStable() error = %v", err)
	}
	if string(data) != "stable" {
		t.Fatalf("ReadStable() = %q, want stable", data)
	}
	if _, err := ReadStable(
		context.Background(),
		path,
		StableReadOptions{MaxBytes: 6, RequiredMode: 0o644},
	); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("ReadStable(wrong mode) error = %v, want mode rejection", err)
	}

	if _, err := ReadStable(context.Background(), path, StableReadOptions{MaxBytes: 5}); err == nil {
		t.Fatal("ReadStable() accepted oversized file")
	}
	symlink := filepath.Join(directory, "control-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStable(context.Background(), symlink, StableReadOptions{MaxBytes: 6}); err == nil {
		t.Fatal("ReadStable() accepted symlink")
	}
}

func TestReadStablePreservesCancellationAndCloseErrors(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "control.json")
	if err := os.WriteFile(path, []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadStable(canceled, path, StableReadOptions{MaxBytes: 6}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadStable(canceled) error = %v, want canceled", err)
	}

	wantCloseErr := errors.New("injected close failure")
	ops := defaultStableReadOperations()
	ops.close = func(file *os.File) error {
		return errors.Join(file.Close(), wantCloseErr)
	}
	if _, err := readStable(context.Background(), path, StableReadOptions{MaxBytes: 6}, ops); !errors.Is(err, wantCloseErr) {
		t.Fatalf("readStable(close failure) error = %v, want close failure", err)
	}
}

func TestReadStableRejectsPathReplacementDuringRead(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "control.json")
	replacement := filepath.Join(directory, "replacement.json")
	if err := os.WriteFile(path, []byte("first!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	ops := defaultStableReadOperations()
	lstat := ops.lstat
	var calls int
	ops.lstat = func(candidate string) (os.FileInfo, error) {
		calls++
		if calls == 2 {
			if err := os.Rename(replacement, path); err != nil {
				t.Fatalf("replace path during read: %v", err)
			}
		}
		return lstat(candidate)
	}
	if _, err := readStable(
		context.Background(),
		path,
		StableReadOptions{MaxBytes: 6, RequiredMode: 0o600},
		ops,
	); err == nil || !strings.Contains(err.Error(), "changed while reading") {
		t.Fatalf("readStable(replaced path) error = %v, want stability rejection", err)
	}
}

func TestCreateExclusivePublishesOnceWithoutClobbering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.txt")
	if err := CreateExclusive(context.Background(), path, 0o600, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "first")
		return err
	}); err != nil {
		t.Fatalf("CreateExclusive() error = %v", err)
	}
	if err := CreateExclusive(context.Background(), path, 0o600, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "second")
		return err
	}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second CreateExclusive() error = %v, want exists", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first" {
		t.Fatalf("published file = %q, %v", data, err)
	}
}

func TestMoveExclusivePublishesWithoutClobbering(t *testing.T) {
	t.Parallel()

	t.Run("move", func(t *testing.T) {
		directory := t.TempDir()
		source := filepath.Join(directory, "source")
		destination := filepath.Join(directory, "destination")
		if err := os.WriteFile(source, []byte("source bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := MoveExclusive(context.Background(), source, destination); err != nil {
			t.Fatalf("MoveExclusive() error = %v", err)
		}
		if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source still exists: %v", err)
		}
		if got, err := os.ReadFile(destination); err != nil || string(got) != "source bytes" {
			t.Fatalf("destination = %q, %v", got, err)
		}
	})

	t.Run("collision", func(t *testing.T) {
		directory := t.TempDir()
		source := filepath.Join(directory, "source")
		destination := filepath.Join(directory, "destination")
		if err := os.WriteFile(source, []byte("source bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, []byte("operator bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := MoveExclusive(context.Background(), source, destination); !errors.Is(err, os.ErrExist) {
			t.Fatalf("MoveExclusive() error = %v, want exists", err)
		}
		if got, err := os.ReadFile(source); err != nil || string(got) != "source bytes" {
			t.Fatalf("source = %q, %v", got, err)
		}
		if got, err := os.ReadFile(destination); err != nil || string(got) != "operator bytes" {
			t.Fatalf("destination = %q, %v", got, err)
		}
	})
}

func TestMoveExclusiveRejectsUnsafeInputsBeforePublication(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	regular := filepath.Join(directory, "regular")
	if err := os.WriteFile(regular, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name        string
		ctx         context.Context
		source      string
		destination string
	}{
		{name: "canceled", ctx: canceled, source: regular, destination: filepath.Join(directory, "canceled")},
		{name: "identical", ctx: context.Background(), source: regular, destination: regular},
		{name: "cross directory", ctx: context.Background(), source: regular, destination: filepath.Join(t.TempDir(), "other")},
		{name: "missing source", ctx: context.Background(), source: filepath.Join(directory, "missing"), destination: filepath.Join(directory, "missing-destination")},
		{name: "directory source", ctx: context.Background(), source: directory, destination: filepath.Join(filepath.Dir(directory), "directory-destination")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := MoveExclusive(test.ctx, test.source, test.destination); err == nil {
				t.Fatal("MoveExclusive() error = nil, want rejection")
			}
		})
	}

	symlink := filepath.Join(directory, "source-link")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}
	if err := MoveExclusive(context.Background(), symlink, filepath.Join(directory, "symlink-destination")); err == nil {
		t.Fatal("MoveExclusive() accepted symlink source")
	}
}

func TestCreateExclusiveRejectsInvalidAndInterruptedWrites(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CreateExclusive(canceled, filepath.Join(directory, "canceled"), 0o600, func(io.Writer) error {
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateExclusive(canceled) error = %v, want canceled", err)
	}
	if err := CreateExclusive(context.Background(), filepath.Join(directory, "nil-writer"), 0o600, nil); err == nil {
		t.Fatal("CreateExclusive(nil writer) error = nil")
	}
	wantErr := errors.New("interrupted encoder")
	path := filepath.Join(directory, "interrupted")
	err := CreateExclusive(context.Background(), path, 0o600, func(writer io.Writer) error {
		if _, writeErr := io.WriteString(writer, "partial"); writeErr != nil {
			return writeErr
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("CreateExclusive(interrupted) error = %v, want %v", err, wantErr)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted destination exists: %v", err)
	}
	if err := CreateExclusive(context.Background(), filepath.Join(directory, "missing", "file"), 0o600, func(io.Writer) error {
		return nil
	}); err == nil {
		t.Fatal("CreateExclusive(missing parent) error = nil")
	}
}

func TestReplaceFailureBeforePublishPreservesTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "art.jpg")
	if err := os.WriteFile(path, []byte("last-known-good"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	wantErr := errors.New("encoder failed")

	err := Replace(context.Background(), path, 0o644, func(writer io.Writer) error {
		if _, writeErr := io.WriteString(writer, "partial"); writeErr != nil {
			return writeErr
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Replace() error = %v, want encoder failure", err)
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("pre-publication error classified as unknown: %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read preserved target: %v", readErr)
	}
	if string(got) != "last-known-good" {
		t.Fatalf("preserved target = %q", got)
	}
	assertNoTemporaryFiles(t, dir)
}

func TestReplaceHonorsCancellation(t *testing.T) {
	t.Run("before work", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		path := filepath.Join(t.TempDir(), "state.json")
		called := false
		err := Replace(ctx, path, 0o600, func(io.Writer) error {
			called = true
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Replace() error = %v, want canceled", err)
		}
		if called {
			t.Fatal("writer called after cancellation")
		}
	})

	t.Run("before publish", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "state.json")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatalf("seed target: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		err := Replace(ctx, path, 0o600, func(writer io.Writer) error {
			if _, writeErr := io.WriteString(writer, "new"); writeErr != nil {
				return writeErr
			}
			cancel()
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Replace() error = %v, want canceled", err)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read target: %v", readErr)
		}
		if string(got) != "old" {
			t.Fatalf("target changed after cancellation: %q", got)
		}
		assertNoTemporaryFiles(t, dir)
	})
}

func TestReplaceClassifiesPostPublishFailuresAsUnknown(t *testing.T) {
	boundaryErr := errors.New("directory unavailable")
	ops := defaultOperations()
	ops.open = func(string) (*os.File, error) { return nil, boundaryErr }
	path := filepath.Join(t.TempDir(), "state.json")

	err := replace(context.Background(), path, 0o600, func(writer io.Writer) error {
		_, writeErr := io.WriteString(writer, "published")
		return writeErr
	}, ops)
	if !errors.Is(err, boundaryErr) || !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("replace() error = %v, want boundary and unknown errors", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read published target: %v", readErr)
	}
	if string(got) != "published" {
		t.Fatalf("published target = %q", got)
	}
}

func TestReplaceFailureBoundaries(t *testing.T) {
	failure := errors.New("injected failure")
	tests := []struct {
		name        string
		mutate      func(*operations)
		wantUnknown bool
	}{
		{name: "create", mutate: func(ops *operations) {
			ops.createTemp = func(string, string) (*os.File, error) { return nil, failure }
		}},
		{name: "chmod", mutate: func(ops *operations) {
			ops.chmod = func(*os.File, os.FileMode) error { return failure }
		}},
		{name: "sync file", mutate: func(ops *operations) {
			ops.sync = func(*os.File) error { return failure }
		}},
		{name: "close file", mutate: func(ops *operations) {
			ops.close = func(file *os.File) error {
				if err := file.Close(); err != nil {
					t.Fatalf("close injected file: %v", err)
				}
				return failure
			}
		}},
		{name: "rename", wantUnknown: true, mutate: func(ops *operations) {
			ops.rename = func(string, string) error { return failure }
		}},
		{name: "open directory", wantUnknown: true, mutate: func(ops *operations) {
			ops.open = func(string) (*os.File, error) { return nil, failure }
		}},
		{name: "sync directory", wantUnknown: true, mutate: func(ops *operations) {
			ops.syncDir = func(*os.File) error { return failure }
		}},
		{name: "close directory", wantUnknown: true, mutate: func(ops *operations) {
			ops.closeDir = func(file *os.File) error {
				if err := file.Close(); err != nil {
					t.Fatalf("close injected directory: %v", err)
				}
				return failure
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			ops := defaultOperations()
			test.mutate(&ops)
			err := replace(context.Background(), filepath.Join(dir, "target"), 0o600, func(writer io.Writer) error {
				_, writeErr := io.WriteString(writer, "payload")
				return writeErr
			}, ops)
			if !errors.Is(err, failure) {
				t.Fatalf("replace() error = %v, want injected failure", err)
			}
			if errors.Is(err, ErrOutcomeUnknown) != test.wantUnknown {
				t.Fatalf("replace() unknown = %t, want %t: %v", errors.Is(err, ErrOutcomeUnknown), test.wantUnknown, err)
			}
			assertNoTemporaryFiles(t, dir)
		})
	}
}

func TestRenameSynchronizesChangedDirectories(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	targetDir := filepath.Join(root, "target")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("create target directory: %v", err)
	}
	oldPath := filepath.Join(sourceDir, "old")
	newPath := filepath.Join(targetDir, "new")
	if err := os.WriteFile(oldPath, []byte("data"), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	if err := Rename(context.Background(), oldPath, newPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old path still exists or stat failed: %v", err)
	}
	if got, err := os.ReadFile(newPath); err != nil || string(got) != "data" {
		t.Fatalf("new path = %q, error = %v", got, err)
	}
}

func TestRenameAndRemoveUnknownOutcomes(t *testing.T) {
	failure := errors.New("sync failed")
	t.Run("rename", func(t *testing.T) {
		dir := t.TempDir()
		oldPath := filepath.Join(dir, "old")
		if err := os.WriteFile(oldPath, []byte("data"), 0o600); err != nil {
			t.Fatalf("seed source: %v", err)
		}
		ops := defaultOperations()
		ops.syncDir = func(*os.File) error { return failure }
		err := rename(context.Background(), oldPath, filepath.Join(dir, "new"), ops)
		if !errors.Is(err, failure) || !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("rename() error = %v", err)
		}
	})

	t.Run("remove", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "obsolete")
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("seed target: %v", err)
		}
		ops := defaultOperations()
		ops.open = func(string) (*os.File, error) { return nil, failure }
		err := remove(context.Background(), path, ops)
		if !errors.Is(err, failure) || !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("remove() error = %v", err)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("removed path state error = %v", statErr)
		}
	})
}

func TestRenameAndRemoveHonorPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := Rename(ctx, path, filepath.Join(dir, "renamed")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Rename() error = %v, want canceled", err)
	}
	if err := Remove(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remove() error = %v, want canceled", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("canceled operations changed target: %v", err)
	}
}

func assertNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read temporary directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".durable-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}
