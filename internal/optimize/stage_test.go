package optimize

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageCatalogIsolatesTransformAndCleansWorkspace(t *testing.T) {
	sourceDirectory := t.TempDir()
	const sourceName = "landscape.h_abcdef123456.jpg"
	sourcePath := filepath.Join(sourceDirectory, sourceName)
	writeTestImage(t, sourcePath, 12, 8)
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceDigest := sha256.Sum256(sourceBytes)
	cfg := DefaultConfig()
	cfg.MaxWidth = 10
	cfg.MaxHeight = 6

	stage, err := StageCatalog(context.Background(), StageRequest{
		Inputs: []StageInput{{Name: sourceName, Path: sourcePath, Digest: sourceDigest}},
		Config: cfg,
	})
	if err != nil {
		t.Fatalf("StageCatalog: %v", err)
	}
	if stage.Optimized != 1 || len(stage.Renames) != 1 {
		t.Fatalf("stage effects = count %d, renames %v; want one of each", stage.Optimized, stage.Renames)
	}
	rename := stage.Renames[0]
	if rename.OldName != sourceName || rename.NewName == "" || rename.NewName == sourceName {
		t.Fatalf("stage rename = %+v", rename)
	}

	workspaceInfo, err := os.Stat(stage.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if workspaceInfo.Mode().Perm() != 0o700 {
		t.Fatalf("stage mode = %o, want 0700", workspaceInfo.Mode().Perm())
	}
	outputPath := filepath.Join(stage.Directory, rename.NewName)
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("stat staged output: %v", err)
	}
	if outputInfo.Mode().Perm() != 0o644 {
		t.Fatalf("staged output mode = %o, want 0644", outputInfo.Mode().Perm())
	}
	if err := ValidateImage(outputPath); err != nil {
		t.Fatalf("staged output is invalid: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage.Directory, sourceName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untransformed staged input still exists: %v", err)
	}

	afterBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read authoritative source: %v", err)
	}
	if sha256.Sum256(afterBytes) != sourceDigest {
		t.Fatal("authoritative source bytes changed")
	}
	sourceEntries, err := os.ReadDir(sourceDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceEntries) != 1 || sourceEntries[0].Name() != sourceName {
		t.Fatalf("authoritative directory changed: %v", sourceEntries)
	}

	workspace := stage.Directory
	if err := stage.Close(); err != nil {
		t.Fatalf("close stage: %v", err)
	}
	if err := stage.Close(); err != nil {
		t.Fatalf("close stage twice: %v", err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains after Close: %v", err)
	}
}

func TestStageCatalogCancellationDoesNotCreateWorkspace(t *testing.T) {
	tempParent := t.TempDir()
	t.Setenv("TMPDIR", tempParent)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stage, err := StageCatalog(ctx, StageRequest{Config: DefaultConfig()})
	if stage != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("StageCatalog = (%v, %v), want (nil, canceled)", stage, err)
	}
	entries, readErr := os.ReadDir(tempParent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled staging left workspace entries: %v", entries)
	}
}

func TestStageCatalogFailureCleansWorkspace(t *testing.T) {
	tempParent := t.TempDir()
	t.Setenv("TMPDIR", tempParent)
	sourcePath := filepath.Join(t.TempDir(), "source.jpg")
	writeTestImage(t, sourcePath, 8, 8)

	stage, err := StageCatalog(context.Background(), StageRequest{
		Inputs: []StageInput{{Name: "source.jpg", Path: sourcePath}},
		Config: DefaultConfig(),
	})
	if stage != nil || err == nil || !strings.Contains(err.Error(), "digest changed") {
		t.Fatalf("StageCatalog = (%v, %v), want cleaned digest failure", stage, err)
	}
	entries, readErr := os.ReadDir(tempParent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed staging left workspace entries: %v", entries)
	}
}

func TestStageCatalogRejectsUnsafeInputs(t *testing.T) {
	t.Run("traversal name", func(t *testing.T) {
		_, err := StageCatalog(context.Background(), StageRequest{
			Inputs: []StageInput{{Name: "../source.jpg", Path: "unused"}},
		})
		if err == nil || !strings.Contains(err.Error(), "plain filename") {
			t.Fatalf("StageCatalog error = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target.jpg")
		writeTestImage(t, target, 8, 8)
		targetBytes, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(directory, "link.jpg")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		_, err = StageCatalog(context.Background(), StageRequest{
			Inputs: []StageInput{{Name: "link.jpg", Path: link, Digest: sha256.Sum256(targetBytes)}},
			Config: DefaultConfig(),
		})
		if err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
			t.Fatalf("StageCatalog error = %v", err)
		}
	})
}
