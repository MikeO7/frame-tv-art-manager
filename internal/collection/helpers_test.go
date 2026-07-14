package collection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDefensiveHelpersRejectInconsistentState(t *testing.T) {
	digest := sha256.Sum256([]byte("one"))
	other := sha256.Sum256([]byte("two"))
	if err := verifyExpected([]Item{{Name: "one.png", Digest: digest}}, nil); err == nil {
		t.Fatal("expected count mismatch")
	}
	if err := verifyExpected(
		[]Item{{Name: "one.png", Digest: digest}},
		[]Item{{Name: "two.png", Digest: other}},
	); err == nil {
		t.Fatal("expected item mismatch")
	}
	if err := copyBytes(errorWriter{}, []byte("data")); err == nil {
		t.Fatal("expected writer failure")
	}
	if got := findDigestName(nil, digest); got != "" {
		t.Fatalf("missing digest name = %q", got)
	}
	if err := validateDimensions(0, 1, 10); err == nil {
		t.Fatal("expected non-positive width failure")
	}
	if err := validateDimensions(1, 0, 10); err == nil {
		t.Fatal("expected non-positive height failure")
	}
}

func TestCollisionNamingFallsBackAfterEveryDigestPrefixIsOccupied(t *testing.T) {
	digest := sha256.Sum256([]byte("input"))
	input := validatedImage{digest: digest, typeID: FileTypePNG, stem: "photo"}
	digestText := stringHex(digest[:])
	items := make([]Item, 0, 14)
	for length := 12; length <= len(digestText); length += 4 {
		items = append(items, Item{Name: "photo-" + digestText[:length] + ".png"})
	}
	name := collisionSafeName(items, input)
	if name != "photo-"+digestText+"-artwork.png" {
		t.Fatalf("fallback name = %q", name)
	}
}

func TestSortItemsUsesDigestAsStableTieBreaker(t *testing.T) {
	first := sha256.Sum256([]byte("a"))
	second := sha256.Sum256([]byte("b"))
	items := []Item{{Name: "same.png", Digest: second}, {Name: "same.png", Digest: first}}
	sortItems(items)
	if bytes.Compare(items[0].Digest[:], items[1].Digest[:]) >= 0 {
		t.Fatalf("items not ordered by digest: %+v", items)
	}
}

func TestValidTransactionPathsRequiresExactOwnedLocations(t *testing.T) {
	root := t.TempDir()
	digest := stringHex(make([]byte, sha256.Size))
	item := manifestItem{Name: "art.png", Digest: digest}
	valid := transaction{
		Version: 1,
		Stage:   filepath.Join(root, controlDirectory, stagingName, digest+".png"),
		Final:   filepath.Join(root, "art.png"),
		Digest:  digest,
		Next:    manifest{Items: []manifestItem{item}},
	}
	if !validTransactionPaths(root, valid) {
		t.Fatal("valid owned paths rejected")
	}
	wrongStage := valid
	wrongStage.Stage = filepath.Join(root, digest+".png")
	if validTransactionPaths(root, wrongStage) {
		t.Fatal("stage outside private directory accepted")
	}
	wrongItem := valid
	wrongItem.Next.Items[0].Name = "different.png"
	if validTransactionPaths(root, wrongItem) {
		t.Fatal("final path absent from manifest accepted")
	}
}

func TestEnsureDirectoryRepairsModeAndRejectsFiles(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "directory")
	if err := ensureDirectory(directory, 0o700); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	if err := ensureDirectory(directory, 0o755); err != nil {
		t.Fatalf("repair directory mode: %v", err)
	}
	file := filepath.Join(root, "file")
	if err := writeTestFile(file); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if err := ensureDirectory(file, 0o700); err == nil {
		t.Fatal("regular file accepted as directory")
	}
}

func TestNoReplacePublicationPreservesUnexpectedDestination(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	final := filepath.Join(root, "final")
	if err := os.WriteFile(stage, []byte("staged"), 0o644); err != nil {
		t.Fatalf("write stage: %v", err)
	}
	if err := os.WriteFile(final, []byte("external"), 0o644); err != nil {
		t.Fatalf("write final: %v", err)
	}
	if err := publishNoReplace(context.Background(), stage, final); err == nil {
		t.Fatal("expected destination collision")
	}
	assertTestFile(t, final, "external")
	assertTestFile(t, stage, "staged")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	missing := filepath.Join(root, "canceled")
	if err := publishNoReplace(ctx, stage, missing); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publication error = %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("canceled publication created destination: %v", err)
	}
}

func TestRecoveryStageCleanupIsDigestGuarded(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	expected := sha256.Sum256([]byte("expected"))
	if err := removeRecoveredStage(context.Background(), stage, expected[:]); err != nil {
		t.Fatalf("absent stage cleanup: %v", err)
	}
	if err := os.WriteFile(stage, []byte("unexpected"), 0o644); err != nil {
		t.Fatalf("write mismatched stage: %v", err)
	}
	if err := removeRecoveredStage(context.Background(), stage, expected[:]); err == nil {
		t.Fatal("mismatched stage was accepted")
	}
	if err := os.WriteFile(stage, []byte("expected"), 0o644); err != nil {
		t.Fatalf("write expected stage: %v", err)
	}
	if err := removeRecoveredStage(context.Background(), stage, expected[:]); err != nil {
		t.Fatalf("remove expected stage: %v", err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("expected stage remains: %v", err)
	}
}

func TestTransactionInspectionRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(target, []byte("data"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := inspectDigest(link, []byte("digest")); err == nil {
		t.Fatal("transaction symlink accepted")
	}
	if err := recoverArtwork(context.Background(), transaction{Final: link}, []byte("digest")); err == nil {
		t.Fatal("recovery accepted final symlink")
	}
	if err := publishStagedArtwork(context.Background(), transaction{Stage: link}, []byte("digest")); err == nil {
		t.Fatal("publication accepted stage symlink")
	}
	if err := syncOwnedDirectory(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing directory sync accepted")
	}
}

func TestFullDecodeFactsAndCancellationAreRechecked(t *testing.T) {
	canceled, cancelImmediately := context.WithCancel(context.Background())
	cancelImmediately()
	if _, err := readAndValidate(canceled, bytes.NewReader([]byte("unused")), "", 10, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-validation cancellation error = %v", err)
	}

	image.RegisterFormat(
		"png",
		"ICFG",
		func(io.Reader) (image.Image, error) { return image.NewRGBA(image.Rect(0, 0, 2, 2)), nil },
		func(io.Reader) (image.Config, error) {
			return image.Config{ColorModel: color.RGBAModel, Width: 1, Height: 1}, nil
		},
	)
	if _, err := readAndValidate(context.Background(), bytes.NewReader([]byte("ICFG")), "", 10, 10); err == nil {
		t.Fatal("inconsistent full decode accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	image.RegisterFormat(
		"png",
		"CCTX",
		func(io.Reader) (image.Image, error) {
			cancel()
			return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
		},
		func(io.Reader) (image.Config, error) {
			return image.Config{ColorModel: color.RGBAModel, Width: 1, Height: 1}, nil
		},
	)
	if _, err := readAndValidate(ctx, bytes.NewReader([]byte("CCTX")), "", 10, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-decode cancellation error = %v", err)
	}
}

func TestFilesystemHelpersRejectNonDirectories(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, _, err := readRoot(file); err == nil {
		t.Fatal("regular file accepted as collection root")
	}
	if _, err := hashFile(root); err == nil {
		t.Fatal("directory accepted as hashable artwork")
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func stringHex(data []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(data)*2)
	for index, value := range data {
		result[index*2] = digits[value>>4]
		result[index*2+1] = digits[value&0x0f]
	}
	return string(result)
}

var _ io.Writer = errorWriter{}

func writeTestFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("contents %s = %q, want %q", path, data, want)
	}
}
