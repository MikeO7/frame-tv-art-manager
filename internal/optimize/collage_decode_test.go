package optimize

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// truncateToHeader leaves a PNG whose size can still be read and whose pixels
// cannot be decoded, so a render succeeds only from an already decoded copy.
func truncateToHeader(t *testing.T, path string) {
	t.Helper()
	const signatureAndIHDR = 8 + 8 + 13 + 4
	if err := os.Truncate(path, signatureAndIHDR); err != nil {
		t.Fatal(err)
	}
}

func collagePass(t *testing.T, names ...string) *catalogPass {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		writePNGForCollageTests(t, filepath.Join(dir, name), 30, 40)
	}
	cfg := DefaultConfig()
	cfg.PortraitMode = portraitModeCollage
	cfg.MaxWidth, cfg.MaxHeight = smallCollageWidth, smallCollageHeight
	return testPass(dir, cfg)
}

func TestPairedPortraitsAreDecodedOnce(t *testing.T) {
	t.Parallel()
	pass := collagePass(t, "a.png", "b.png")
	first, second := filepath.Join(pass.artworkDir, "a.png"), filepath.Join(pass.artworkDir, "b.png")
	if features := pass.portraitFeatures(context.Background(), []string{"a.png", "b.png"}); len(features) != 2 {
		t.Fatalf("portraitFeatures() = %v, want two", features)
	}
	// Measuring decoded both portraits. If rendering decoded them again it
	// would fail now.
	truncateToHeader(t, first)
	truncateToHeader(t, second)
	name, err := pass.renderCollageFile(context.Background(), first, second, "collage", extPNG)
	if err != nil {
		t.Fatalf("renderCollageFile() = %v, want the portraits decoded for pairing to be rendered", err)
	}
	if err := validateImage(context.Background(), filepath.Join(pass.artworkDir, name)); err != nil {
		t.Fatalf("collage is not a valid image: %v", err)
	}
	if len(pass.decoded) != 0 {
		t.Fatalf("%d decoded portraits are still held after their collage", len(pass.decoded))
	}
}

func TestDecodedPortraitsStayWithinTheWorkingSetLimit(t *testing.T) {
	t.Parallel()
	names := []string{"a.png", "b.png", "c.png", "d.png"}
	pass := collagePass(t, names...)
	single := workingInputOfSize(30, 40, 4)
	pictureBytes := int64(30 * 40 * 4)
	// Room for the pair being rendered and one more decoded portrait.
	pass.cfg.MaxWorkingBytes = estimatedWorkingBytes(single.plus(single), pass.cfg) + pictureBytes
	limit := workingByteLimit(pass.cfg)

	pass.portraitFeatures(context.Background(), names)
	if held := pass.decodedBytes(); held == 0 || held+estimatedWorkingBytes(single, pass.cfg) > limit+pictureBytes {
		t.Fatalf("holding %d bytes of decoded portraits beside a %d-byte decode, limit %d",
			held, estimatedWorkingBytes(single, pass.cfg), limit)
	}
	path := func(name string) string { return filepath.Join(pass.artworkDir, name) }
	pass.releaseDecoded(estimatedWorkingBytes(single.plus(single), pass.cfg), path("c.png"), path("d.png"))
	if held := pass.decodedBytes(path("c.png"), path("d.png")); held > pictureBytes {
		t.Fatalf("holding %d bytes of other portraits while rendering a pair, want at most one picture", held)
	}
	// A portrait that was released is decoded again when its turn comes.
	pass.dropDecoded(path("a.png"), path("b.png"))
	if _, err := pass.renderCollageFile(context.Background(), path("a.png"), path("b.png"), "collage", extPNG); err != nil {
		t.Fatalf("renderCollageFile(released portraits) = %v", err)
	}

	localFiles := map[string]struct{}{"c.png": {}, "d.png": {}}
	if err := pass.processCollages(context.Background(), localFiles); err != nil || len(localFiles) != 1 {
		t.Fatalf("processCollages() = %v with files %v, want one collage", err, localFiles)
	}
	if len(pass.decoded) != 0 {
		t.Fatalf("%d decoded portraits are still held after pairing ended", len(pass.decoded))
	}
}

func TestLeftoverPortraitIsNotKeptDecoded(t *testing.T) {
	t.Parallel()
	pass := collagePass(t, "a.png", "b.png", "c.png")
	pass.inputs = map[string]StageInput{"a.png": {}, "b.png": {}, "c.png": {}}
	localFiles := map[string]struct{}{"a.png": {}, "b.png": {}, "c.png": {}}
	if err := pass.processCollages(context.Background(), localFiles); err != nil {
		t.Fatalf("processCollages() = %v", err)
	}
	if len(localFiles) != 1 || len(pass.decoded) != 0 {
		t.Fatalf("files %v, %d decoded portraits held; want one collage and nothing held", localFiles, len(pass.decoded))
	}
}
