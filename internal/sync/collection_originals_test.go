package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
)

var (
	stripeGreen = color.RGBA{G: 200, A: 255}
	bodyRed     = color.RGBA{R: 220, A: 255}
	bodyBlue    = color.RGBA{B: 220, A: 255}
)

// originalsCycle drives complete Sync Cycle preparation against a real
// collection store, counting the stage publications it makes.
type originalsCycle struct {
	t     *testing.T
	root  string
	cfg   *config.Config
	store *countingCollectionStore
}

func newOriginalsCycle(t *testing.T) *originalsCycle {
	t.Helper()
	root := t.TempDir()
	return &originalsCycle{
		t: t, root: root,
		cfg: &config.Config{
			ArtworkDir: root, OptimizeEnabled: true,
			OptimizeMaxWidth: 192, OptimizeMaxHeight: 108, OptimizeJPEGQuality: 95,
			LinearLightResize: true, ColorProfilePolicy: "convert-srgb",
			PortraitMode: "crop", PadStyle: "mat", MatTone: "auto", MaxCropPercent: 25, MaxUpscale: 3,
			MuseumModeIntensity: 5,
		},
		store: &countingCollectionStore{Store: newTestCollectionStore(t, root)},
	}
}

func (cycle *originalsCycle) run() collectionpkg.Snapshot {
	cycle.t.Helper()
	manager := &localCollection{cfg: cycle.cfg, logger: slog.Default(), loader: fixedSourceLoader{}, store: cycle.store}
	result, err := manager.prepareCycle(context.Background())
	if err != nil {
		cycle.t.Fatalf("prepareCycle() error = %v", err)
	}
	return result.snapshot
}

func (cycle *originalsCycle) write(name string, data []byte) {
	cycle.t.Helper()
	if err := os.WriteFile(filepath.Join(cycle.root, name), data, 0o644); err != nil {
		cycle.t.Fatalf("write %s: %v", name, err)
	}
}

// keptOriginals returns the names in the originals control directory.
func (cycle *originalsCycle) keptOriginals() []string {
	cycle.t.Helper()
	entries, err := os.ReadDir(filepath.Dir(collectionpkg.OriginalPath(cycle.root, collectionpkg.Original{})))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		cycle.t.Fatalf("read originals: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// requireOriginal asserts that item is rendered from exactly the given bytes
// and that those bytes are kept, privately, in the control directory.
func (cycle *originalsCycle) requireOriginal(item collectionpkg.Item, want ...[]byte) {
	cycle.t.Helper()
	if len(item.Originals) != len(want) {
		cycle.t.Fatalf("item %s originals = %+v, want %d", item.Name, item.Originals, len(want))
	}
	for index, original := range item.Originals {
		if original.Digest != sha256.Sum256(want[index]) {
			cycle.t.Fatalf("item %s original %d digest = %x, want the source bytes", item.Name, index, original.Digest)
		}
		path := collectionpkg.OriginalPath(cycle.root, original)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want[index]) {
			cycle.t.Fatalf("kept original %s = %d bytes, %v; want the untouched source", path, len(got), err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			cycle.t.Fatalf("kept original mode = %v, %v; want 0600", info, err)
		}
	}
}

func (cycle *originalsCycle) decode(item collectionpkg.Item) *image.RGBA {
	cycle.t.Helper()
	file, err := os.Open(item.Path)
	if err != nil {
		cycle.t.Fatalf("open %s: %v", item.Name, err)
	}
	defer func() { _ = file.Close() }()
	decoded, err := jpeg.Decode(file)
	if err != nil {
		cycle.t.Fatalf("decode %s: %v", item.Name, err)
	}
	rgba := image.NewRGBA(decoded.Bounds())
	for y := range decoded.Bounds().Dy() {
		for x := range decoded.Bounds().Dx() {
			rgba.Set(x, y, decoded.At(x, y))
		}
	}
	return rgba
}

func onlyItem(t *testing.T, snapshot collectionpkg.Snapshot) collectionpkg.Item {
	t.Helper()
	if len(snapshot.Items) != 1 {
		t.Fatalf("snapshot items = %+v, want exactly one", snapshot.Items)
	}
	return snapshot.Items[0]
}

// stripedPortrait encodes a portrait whose top and bottom edges carry a green
// stripe: cropping it to landscape removes both stripes, matting keeps both.
func stripedPortrait(t *testing.T, body color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 300))
	for y := range 300 {
		for x := range 200 {
			fill := body
			if y < 12 || y >= 288 {
				fill = stripeGreen
			}
			img.SetRGBA(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode portrait: %v", err)
	}
	return buffer.Bytes()
}

// greenSpan returns the first and last rows of the center column that show
// the stripe color, or -1, -1 when no stripe survived.
func greenSpan(img *image.RGBA) (int, int) {
	top, bottom := -1, -1
	x := img.Bounds().Dx() / 2
	for y := range img.Bounds().Dy() {
		if pixel := img.RGBAAt(x, y); pixel.G > 140 && pixel.R < 90 && pixel.B < 90 {
			if top < 0 {
				top = y
			}
			bottom = y
		}
	}
	return top, bottom
}

func TestSettingsChangeRederivesFromKeptOriginal(t *testing.T) {
	cycle := newOriginalsCycle(t)
	source := stripedPortrait(t, bodyRed)
	cycle.write("portrait.jpg", source)

	cropped := onlyItem(t, cycle.run())
	cycle.requireOriginal(cropped, source)
	if top, _ := greenSpan(cycle.decode(cropped)); top >= 0 {
		t.Fatalf("crop mode kept the stripe at row %d; the test needs a lossy first derivative", top)
	}
	if _, err := os.Stat(filepath.Join(cycle.root, "portrait.jpg")); !os.IsNotExist(err) {
		t.Fatalf("raw file should be replaced by its derivative, stat error = %v", err)
	}

	croppedBytes, err := os.ReadFile(cropped.Path)
	if err != nil {
		t.Fatal(err)
	}
	applies := cycle.store.applyCalls
	unchanged := onlyItem(t, cycle.run())
	after, err := os.ReadFile(unchanged.Path)
	if err != nil {
		t.Fatal(err)
	}
	if cycle.store.applyCalls != applies || unchanged.Name != cropped.Name || !bytes.Equal(after, croppedBytes) {
		t.Fatalf("same settings re-encoded the derivative: applies %d -> %d, name %s -> %s",
			applies, cycle.store.applyCalls, cropped.Name, unchanged.Name)
	}

	cycle.cfg.PortraitMode = "pad"
	matted := onlyItem(t, cycle.run())
	if matted.Name == cropped.Name || matted.Key != cropped.Key {
		t.Fatalf("matted item = %+v, cropped item = %+v", matted, cropped)
	}
	cycle.requireOriginal(matted, source)
	top, bottom := greenSpan(cycle.decode(matted))
	if top < 0 || bottom-top < 90 {
		t.Fatalf("mat did not recover the whole original: stripe rows %d..%d", top, bottom)
	}

	cycle.cfg.MatTone = "light"
	retoned := onlyItem(t, cycle.run())
	cycle.requireOriginal(retoned, source)
	if got := cycle.keptOriginals(); len(got) != 1 {
		t.Fatalf("kept originals = %v, want only the one source", got)
	}
}

func TestKeptOriginalFollowsFileLifecycle(t *testing.T) {
	tests := []struct {
		name  string
		after func(*testing.T, *originalsCycle, collectionpkg.Item) []byte
		check func(*testing.T, *originalsCycle, collectionpkg.Snapshot, []byte)
	}{
		{
			name: "rename keeps lineage without transforming again",
			after: func(t *testing.T, cycle *originalsCycle, item collectionpkg.Item) []byte {
				if err := os.Rename(item.Path, filepath.Join(cycle.root, "renamed by me.jpg")); err != nil {
					t.Fatal(err)
				}
				return nil
			},
			check: func(t *testing.T, cycle *originalsCycle, snapshot collectionpkg.Snapshot, _ []byte) {
				item := onlyItem(t, snapshot)
				if item.Name != "renamed by me.jpg" || item.Derivative != collectionpkg.DerivativeOptimized {
					t.Fatalf("renamed item = %+v", item)
				}
				cycle.requireOriginal(item, stripedPortrait(t, bodyRed))
				if cycle.store.applyCalls != 1 {
					t.Fatalf("rename was transformed again: %d applies", cycle.store.applyCalls)
				}
			},
		},
		{
			name: "delete collects the original after the delete commits",
			after: func(t *testing.T, _ *originalsCycle, item collectionpkg.Item) []byte {
				if err := os.Remove(item.Path); err != nil {
					t.Fatal(err)
				}
				return nil
			},
			check: func(t *testing.T, cycle *originalsCycle, snapshot collectionpkg.Snapshot, _ []byte) {
				if len(snapshot.Items) != 0 {
					t.Fatalf("deleted artwork remains: %+v", snapshot.Items)
				}
				if got := cycle.keptOriginals(); len(got) != 0 {
					t.Fatalf("original of deleted artwork kept: %v", got)
				}
			},
		},
		{
			name: "overwrite makes the new bytes the original",
			after: func(t *testing.T, cycle *originalsCycle, item collectionpkg.Item) []byte {
				replacement := stripedPortrait(t, bodyBlue)
				cycle.write(item.Name, replacement)
				return replacement
			},
			check: func(t *testing.T, cycle *originalsCycle, snapshot collectionpkg.Snapshot, replacement []byte) {
				cycle.requireOriginal(onlyItem(t, snapshot), replacement)
				if got := cycle.keptOriginals(); len(got) != 1 {
					t.Fatalf("kept originals = %v, want only the replacement", got)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cycle := newOriginalsCycle(t)
			cycle.write("portrait.jpg", stripedPortrait(t, bodyRed))
			first := onlyItem(t, cycle.run())
			replacement := test.after(t, cycle, first)
			test.check(t, cycle, cycle.run(), replacement)
		})
	}
}

func TestDryRunWritesNoOriginals(t *testing.T) {
	cycle := newOriginalsCycle(t)
	cycle.cfg.DryRun = true
	source := stripedPortrait(t, bodyRed)
	cycle.write("portrait.jpg", source)

	item := onlyItem(t, cycle.run())
	if item.Name != "portrait.jpg" || len(item.Originals) != 0 {
		t.Fatalf("dry-run item = %+v", item)
	}
	if _, err := os.Lstat(filepath.Join(cycle.root, ".frame-tv-art-manager")); !os.IsNotExist(err) {
		t.Fatalf("dry run created control state: %v", err)
	}
}

func TestLegacyDerivativeKeepsCurrentBytesAsItsOriginal(t *testing.T) {
	cycle := newOriginalsCycle(t)
	seed, err := cycle.store.Import(context.Background(), collectionpkg.ImportRequest{
		Reader: bytes.NewReader(stripedPortrait(t, bodyRed)), Hint: "seed.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A derivative published before originals were kept: transform metadata
	// but no originals.
	legacy := stripedPortrait(t, bodyBlue)
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "legacy.jpg"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cycle.store.Apply(context.Background(), collectionpkg.ApplyRequest{
		Directory: stage,
		Metadata: map[string]collectionpkg.ItemMetadata{"legacy.jpg": {
			Key: seed.Items[0].Key, Origin: seed.Items[0].Origin,
			TransformKey: strings.Repeat("a", 64), Derivative: collectionpkg.DerivativeOptimized,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	first := onlyItem(t, cycle.run())
	cycle.requireOriginal(first, legacy)
	cycle.cfg.PortraitMode = "pad"
	second := onlyItem(t, cycle.run())
	cycle.requireOriginal(second, legacy)
	if top, bottom := greenSpan(cycle.decode(second)); top < 0 || bottom-top < 90 {
		t.Fatalf("second derivative was stacked on the first: stripe rows %d..%d", top, bottom)
	}
}

func TestCollageRederivesFromBothKeptOriginals(t *testing.T) {
	cycle := newOriginalsCycle(t)
	cycle.cfg.PortraitMode = "collage"
	left, right := stripedPortrait(t, bodyRed), stripedPortrait(t, bodyBlue)
	cycle.write("a-left.jpg", left)
	cycle.write("b-right.jpg", right)

	collage := onlyItem(t, cycle.run())
	if collage.Derivative != collectionpkg.DerivativeCollage {
		t.Fatalf("collage item = %+v", collage)
	}
	cycle.requireOriginal(collage, left, right)

	cycle.cfg.MatTone = "light"
	retoned := onlyItem(t, cycle.run())
	if retoned.Name == collage.Name || retoned.Key != collage.Key || retoned.Derivative != collectionpkg.DerivativeCollage {
		t.Fatalf("re-rendered collage = %+v, first = %+v", retoned, collage)
	}
	cycle.requireOriginal(retoned, left, right)
	img := cycle.decode(retoned)
	middle := img.Bounds().Dy() / 2
	leftPixel, rightPixel := img.RGBAAt(img.Bounds().Dx()/4, middle), img.RGBAAt(3*img.Bounds().Dx()/4, middle)
	if leftPixel.R < 150 || rightPixel.B < 150 {
		t.Fatalf("collage halves = %+v / %+v, want the left original red and the right blue", leftPixel, rightPixel)
	}
	names := cycle.keptOriginals()
	slices.Sort(names)
	if len(names) != 2 {
		t.Fatalf("kept originals = %v, want both portraits", names)
	}
}
