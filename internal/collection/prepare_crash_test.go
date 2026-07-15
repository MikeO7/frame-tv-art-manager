package collection

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareRecoversManifestTransactionAtEveryDurableBoundary(t *testing.T) {
	for _, manifestPublished := range []bool{false, true} {
		name := "intent-only"
		if manifestPublished {
			name = "manifest-published"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			data := encodeInternalImage(t, 2, 2)
			artworkPath := filepath.Join(root, "operator.png")
			if err := os.WriteFile(artworkPath, data, 0o644); err != nil {
				t.Fatalf("write artwork: %v", err)
			}
			value, err := readAndValidate(context.Background(), bytes.NewReader(data), "operator.png", 1<<20, 1<<20)
			if err != nil {
				t.Fatalf("validate artwork: %v", err)
			}
			items := []Item{{
				Name: "operator.png", Key: "operator.png", Path: artworkPath, Digest: value.digest,
				Type: value.typeID, Width: value.width, Height: value.height,
				Origin: Origin{Key: "operator:operator.png", Class: OriginOperator},
			}}
			if err := ensureLayout(root); err != nil {
				t.Fatalf("ensure layout: %v", err)
			}
			next := newManifest(items)
			if err := writeJournal(context.Background(), root, transaction{
				Version: 1, Kind: transactionKindManifest, Next: next,
			}); err != nil {
				t.Fatalf("write journal: %v", err)
			}
			if manifestPublished {
				if err := writeManifest(context.Background(), root, next); err != nil {
					t.Fatalf("write manifest: %v", err)
				}
			}

			valueStore, err := New(Config{Root: root, MaxImportBytes: 1 << 20, MaxPixels: 1 << 20})
			if err != nil {
				t.Fatalf("new store: %v", err)
			}
			snapshot, err := valueStore.Prepare(context.Background(), PrepareRequest{})
			if err != nil {
				t.Fatalf("recover prepare: %v", err)
			}
			if len(snapshot.Items) != 1 || snapshot.Items[0].Name != "operator.png" {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			if _, err := os.Stat(journalPath(root)); !os.IsNotExist(err) {
				t.Fatalf("journal remains: %v", err)
			}
		})
	}
}

func TestManifestTransactionRejectsCancellationAndInvalidIntent(t *testing.T) {
	root := t.TempDir()
	if err := ensureLayout(root); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	next := newManifest(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := commitManifest(ctx, root, next); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled commit error = %v", err)
	}
	if err := recoverManifestTransaction(ctx, root, next); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery error = %v", err)
	}

	valid := transaction{Version: 1, Kind: transactionKindManifest, Next: next}
	if !validTransaction(root, valid) {
		t.Fatal("valid manifest transaction rejected")
	}
	withStage := valid
	withStage.Stage = filepath.Join(root, "unexpected")
	if validTransaction(root, withStage) {
		t.Fatal("manifest transaction with artwork stage accepted")
	}
	unknown := valid
	unknown.Kind = "unknown"
	if validTransaction(root, unknown) {
		t.Fatal("unknown transaction kind accepted")
	}
	invalidManifest := valid
	invalidManifest.Next.Version = 3
	if validTransaction(root, invalidManifest) {
		t.Fatal("invalid next manifest accepted")
	}
}

func TestManifestTransactionPropagatesPublicationAndCompletionFailures(t *testing.T) {
	t.Run("manifest publication", func(t *testing.T) {
		root := t.TempDir()
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		manifestPath := filepath.Join(root, controlDirectory, manifestName)
		if err := os.Mkdir(manifestPath, 0o700); err != nil {
			t.Fatalf("mkdir manifest blocker: %v", err)
		}
		if err := commitManifest(context.Background(), root, newManifest(nil)); err == nil {
			t.Fatal("manifest publication blocker was ignored")
		}
	})

	t.Run("journal completion", func(t *testing.T) {
		root := t.TempDir()
		if err := ensureLayout(root); err != nil {
			t.Fatalf("ensure layout: %v", err)
		}
		journal := journalPath(root)
		if err := os.Mkdir(journal, 0o700); err != nil {
			t.Fatalf("mkdir journal blocker: %v", err)
		}
		if err := os.WriteFile(filepath.Join(journal, "child"), []byte("block removal"), 0o600); err != nil {
			t.Fatalf("write journal blocker: %v", err)
		}
		if err := recoverManifestTransaction(context.Background(), root, newManifest(nil)); err == nil {
			t.Fatal("journal completion blocker was ignored")
		}
	})
}

func TestManifestComparisonChecksEveryCommittedFact(t *testing.T) {
	base := newManifest(nil)
	if !manifestsEqual(base, base) {
		t.Fatal("identical manifests differ")
	}
	version := base
	version.Version++
	if manifestsEqual(base, version) {
		t.Fatal("version mismatch accepted")
	}
	generationValue := base
	generationValue.Generation = "different"
	if manifestsEqual(base, generationValue) {
		t.Fatal("generation mismatch accepted")
	}
	digest := [32]byte{1}
	withItem := newManifest([]Item{{Name: "one.png", Digest: digest, Type: FileTypePNG, Width: 1, Height: 1}})
	if manifestsEqual(base, withItem) {
		t.Fatal("item count mismatch accepted")
	}
	changedItem := withItem
	changedItem.Items = append([]manifestItem(nil), withItem.Items...)
	changedItem.Items[0].Width++
	if manifestsEqual(withItem, changedItem) {
		t.Fatal("item fact mismatch accepted")
	}
}

func TestPrepareScannerPropagatesCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "image.png"), encodeInternalImage(t, 1, 1), 0o644); err != nil {
		t.Fatalf("write artwork: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := scanPrepare(ctx, root, 1<<20, 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan cancellation error = %v", err)
	}
}

func TestPrepareScannerPropagatesManifestAndRootFailures(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		root := t.TempDir()
		control := filepath.Join(root, controlDirectory)
		if err := os.Mkdir(control, 0o700); err != nil {
			t.Fatalf("mkdir control: %v", err)
		}
		if err := os.WriteFile(filepath.Join(control, manifestName), []byte("{"), 0o600); err != nil {
			t.Fatalf("write malformed manifest: %v", err)
		}
		if _, _, err := scanPrepare(context.Background(), root, 1<<20, 1<<20); err == nil {
			t.Fatal("malformed manifest accepted")
		}
	})
	t.Run("root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("write root file: %v", err)
		}
		if _, _, err := scanPrepare(context.Background(), root, 1<<20, 1<<20); err == nil {
			t.Fatal("regular-file root accepted")
		}
	})
}

func encodeInternalImage(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	value.Set(0, 0, color.RGBA{R: 100, G: 80, B: 60, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, value); err != nil {
		t.Fatalf("encode image: %v", err)
	}
	return output.Bytes()
}
