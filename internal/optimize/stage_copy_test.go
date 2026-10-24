package optimize

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestStageCopyRejectsUnstableAndInvalidInputs(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	missing := StageInput{Name: "missing.jpg", Path: filepath.Join(directory, "missing.jpg")}
	if _, err := openStageInput(missing); err == nil {
		t.Fatal("openStageInput(missing) error = nil")
	}
	if _, err := openStageInput(StageInput{Name: "directory.jpg", Path: directory}); err == nil {
		t.Fatal("openStageInput(directory) error = nil")
	}

	sourcePath := filepath.Join(directory, "source.jpg")
	if err := os.WriteFile(sourcePath, []byte("source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := StageInput{Name: "source.jpg", Path: sourcePath, Digest: sha256.Sum256([]byte("source bytes"))}
	openSource := func(t *testing.T) stageSource {
		t.Helper()
		source, err := openStageInput(input)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = source.file.Close() })
		return source
	}

	t.Run("destination exists", func(t *testing.T) {
		stageDirectory := t.TempDir()
		if err := os.WriteFile(filepath.Join(stageDirectory, input.Name), []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeStageInput(context.Background(), stageDirectory, input, openSource(t)); err == nil {
			t.Fatal("writeStageInput(occupied) error = nil")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := writeStageInput(ctx, t.TempDir(), input, openSource(t)); err == nil {
			t.Fatal("writeStageInput(canceled) error = nil")
		}
	})
	t.Run("digest changed", func(t *testing.T) {
		wrong := input
		wrong.Digest = sha256.Sum256([]byte("different"))
		if err := writeStageInput(context.Background(), t.TempDir(), wrong, openSource(t)); err == nil {
			t.Fatal("writeStageInput(wrong digest) error = nil")
		}
	})
	t.Run("path removed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.jpg")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		candidate := StageInput{Name: "source.jpg", Path: path}
		source, err := openStageInput(candidate)
		if err != nil {
			t.Fatal(err)
		}
		defer source.file.Close()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := verifyStageInputStable(candidate, source.file, source.before, source.opened); err == nil {
			t.Fatal("verifyStageInputStable(removed) error = nil")
		}
	})
	t.Run("path replaced", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.jpg")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		candidate := StageInput{Name: "source.jpg", Path: path}
		source, err := openStageInput(candidate)
		if err != nil {
			t.Fatal(err)
		}
		defer source.file.Close()
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyStageInputStable(candidate, source.file, source.before, source.opened); err == nil {
			t.Fatal("verifyStageInputStable(replaced) error = nil")
		}
	})
}
