package optimize

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestStageCatalogRendersDerivativesFromKeptOriginals(t *testing.T) {
	tests := []struct {
		name string
		// damage alters the kept original before staging.
		damage func(t *testing.T, path string)
		// wantOwn reports that the derivative's current bytes become its
		// original; wantSkip that the derivative is left untouched.
		wantOwn  bool
		wantSkip bool
	}{
		{name: "kept original", damage: func(*testing.T, string) {}},
		{name: "missing original", damage: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, wantOwn: true},
		{name: "corrupt original", damage: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("bit rot"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantOwn: true},
		{name: "unreadable original", damage: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, wantSkip: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			derivativePath := filepath.Join(directory, "art.jpg")
			originalPath := filepath.Join(directory, "original.jpg")
			writeTestImage(t, derivativePath, 10, 6)
			writeTestImage(t, originalPath, 24, 16)
			derivativeDigest := fileSHA256(t, derivativePath)
			originalDigest := fileSHA256(t, originalPath)
			test.damage(t, originalPath)
			cfg := DefaultConfig()
			cfg.MaxWidth, cfg.MaxHeight = 10, 6

			stage, err := StageCatalog(context.Background(), StageRequest{
				Inputs: []StageInput{{
					Name: "art.jpg", Key: "art.jpg", Path: derivativePath, Digest: derivativeDigest,
					Width: 10, Height: 6, TransformKey: "previous settings", Derivative: DerivativeOptimized,
					Originals: []StageOriginal{{Digest: originalDigest, Path: originalPath}},
				}},
				Config: cfg,
			})
			if err != nil {
				t.Fatalf("StageCatalog() error = %v", err)
			}
			defer func() { _ = stage.Close() }()
			if test.wantSkip {
				if len(stage.Derivatives) != 0 {
					t.Fatalf("derivatives = %+v, want the file skipped this pass", stage.Derivatives)
				}
				return
			}
			want := originalDigest
			if test.wantOwn {
				want = derivativeDigest
			}
			if len(stage.Derivatives) != 1 || !slices.Equal(stage.Derivatives[0].Originals, [][sha256.Size]byte{want}) {
				t.Fatalf("derivatives = %+v, want one rendered from %x", stage.Derivatives, want)
			}
		})
	}
}

func fileSHA256(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func TestStageCatalogRecordsADerivativeRenderedAgainToTheSameBytes(t *testing.T) {
	directory := t.TempDir()
	originalPath := filepath.Join(directory, "original.jpg")
	writeTestImage(t, originalPath, 24, 16)
	originalDigest := fileSHA256(t, originalPath)
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 10, 6
	render := func(name, path string, digest [sha256.Size]byte) *Stage {
		t.Helper()
		stage, err := StageCatalog(context.Background(), StageRequest{
			Inputs: []StageInput{{
				Name: name, Key: "art.jpg", Path: path, Digest: digest,
				Width: 10, Height: 6, TransformKey: "previous settings", Derivative: DerivativeOptimized,
				Originals: []StageOriginal{{Digest: originalDigest, Path: originalPath}},
			}},
			Config: cfg,
		})
		if err != nil {
			t.Fatalf("StageCatalog() error = %v", err)
		}
		t.Cleanup(func() { _ = stage.Close() })
		if len(stage.Derivatives) != 1 {
			t.Fatalf("derivatives = %+v, want one", stage.Derivatives)
		}
		return stage
	}
	stalePath := filepath.Join(directory, "art.jpg")
	writeTestImage(t, stalePath, 10, 6)
	first := render("art.jpg", stalePath, fileSHA256(t, stalePath))
	name := first.Derivatives[0].Name
	rendered := filepath.Join(first.Directory, name)

	// The settings changed in a way that leaves this picture's bytes alone.
	second := render(name, rendered, fileSHA256(t, rendered))
	got := second.Derivatives[0]
	if got.Name != name || got.TransformKey != TransformKey(cfg) {
		t.Fatalf("derivative = %+v, want %s recorded with the current transform key", got, name)
	}
}
