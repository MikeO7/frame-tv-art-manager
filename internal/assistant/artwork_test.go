package assistant

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

func writeCorruptCollections(t *testing.T, store *themes.Store) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func folderArtwork(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestAddArtworkFromBase64CreatesAnOffCollection(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	var added addArtworkOutput
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Christmas", "image_base64": "data:image/jpeg;base64," + base64Of(paintedJPEG(t, 640, 360, 3)),
		"title": "Skaters on the Canal", "artist": "Image model", "description": "Dutch winter, oil on panel",
	}, &added)
	if added.Collection != "christmas" || !added.CollectionCreated || added.Duplicate || added.AlreadyInCollection ||
		!strings.HasPrefix(added.Filename, "skaters-on-the-canal") || added.Key == "" || len(added.Digest) != 64 ||
		added.Width != 640 || added.Height != 360 || added.CollectionActive {
		t.Fatalf("add_artwork = %+v", added)
	}
	if added.Fit.Mode != "mat" || added.Fit.Scale != 3 {
		t.Fatalf("fit = %+v, want a mat at the 3x upscale cap for a 640x360 image", added.Fit)
	}
	warnings := strings.Join(added.Warnings, " ")
	if !strings.Contains(warnings, "small (640x360)") || !strings.Contains(warnings, "not active today") {
		t.Fatalf("warnings = %q, want small-image and inactive-collection warnings", warnings)
	}
	file, err := fixture.themes.Load(context.Background())
	if err != nil || len(file.Collections) != 1 {
		t.Fatalf("collections = %+v, %v", file, err)
	}
	christmas := file.Collections[0]
	if christmas.Activation.Mode != themes.ModeOff || !christmas.Exclusive || len(christmas.Members) != 1 {
		t.Fatalf("created collection = %+v", christmas)
	}
	member := christmas.Members[0]
	if member.Key != added.Key || member.Name != added.Filename || member.Title != "Skaters on the Canal" ||
		member.Artist != "Image model" || !strings.HasPrefix(member.Origin, "upload:") {
		t.Fatalf("member = %+v", member)
	}
	if names := folderArtwork(t, fixture.cfg.ArtworkDir); !slices.Equal(names, []string{added.Filename}) {
		t.Fatalf("artwork folder = %v, want only %s", names, added.Filename)
	}

	var again addArtworkOutput
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "christmas", "image_base64": base64Of(paintedJPEG(t, 640, 360, 3)),
	}, &again)
	if !again.Duplicate || !again.AlreadyInCollection || again.Filename != added.Filename || again.CollectionCreated {
		t.Fatalf("repeated add_artwork = %+v, want a deduplicated existing member", again)
	}
}

func TestAddArtworkReportsMatAndExactFits(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{environ: []string{"MAX_DOWNLOAD_SIZE_MB=20"}})
	var exact, square addArtworkOutput
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Gallery", "image_base64": base64Of(paintedPNG(t, 3840, 2160)),
	}, &exact)
	if exact.Fit.Mode != "exact" || exact.Fit.Scale != 1 || len(exact.Warnings) != 1 {
		t.Fatalf("exact fit = %+v, want only the inactive-collection warning", exact)
	}
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Gallery", "image_base64": base64Of(paintedJPEG(t, 2000, 2000, 9)),
	}, &square)
	if square.Fit.Mode != "mat" || !strings.Contains(square.Fit.Description, "gallery mat") {
		t.Fatalf("square fit = %+v, want a gallery mat", square.Fit)
	}
}

func TestAddArtworkRejectsBadImagesWithoutChangingAnything(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	oversized := make([]byte, (2<<20)+1)
	copy(oversized, paintedJPEG(t, 16, 16, 1))
	truncated := paintedJPEG(t, 64, 64, 2)
	truncated = truncated[:len(truncated)/2]
	tests := []struct {
		name      string
		arguments map[string]any
		want      string
	}{
		{"no image", map[string]any{"collection": "Christmas"}, "provide the image"},
		{"both sources", map[string]any{"collection": "Christmas", "image_url": "https://example.com/a.jpg", "image_base64": "AAAA"}, "not both"},
		{"invalid base64", map[string]any{"collection": "Christmas", "image_base64": "%%%not base64%%%"}, "not valid base64"},
		{"not an image", map[string]any{"collection": "Christmas", "image_base64": base64Of([]byte("GIF89a not supported"))}, "not a JPEG or PNG"},
		{"oversized", map[string]any{"collection": "Christmas", "image_base64": base64Of(oversized)}, "limit"},
		{"truncated JPEG", map[string]any{"collection": "Christmas", "image_base64": base64Of(truncated)}, "rejected"},
		{"bad collection name", map[string]any{"collection": "***", "image_base64": base64Of(paintedJPEG(t, 16, 16, 4))}, "letter or digit"},
		{"plain http link", map[string]any{"collection": "Christmas", "image_url": "http://example.com/a.jpg"}, "only https"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if text := fixture.callError(t, "add_artwork", test.arguments); !strings.Contains(text, test.want) {
				t.Fatalf("add_artwork error = %q, want %q", text, test.want)
			}
		})
	}
	if names := folderArtwork(t, fixture.cfg.ArtworkDir); len(names) != 0 {
		t.Fatalf("rejected images left artwork behind: %v", names)
	}
	file, err := fixture.themes.Load(context.Background())
	if err != nil || len(file.Collections) != 0 {
		t.Fatalf("rejected images left collections behind: %+v, %v", file, err)
	}
}

func TestAddArtworkRefusesToOverwriteAnUnreadableCollectionsFile(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	writeCorruptCollections(t, fixture.themes)
	text := fixture.callError(t, "add_artwork", map[string]any{
		"collection": "Christmas", "image_base64": base64Of(paintedJPEG(t, 32, 18, 5)),
	})
	if !strings.Contains(text, "collections.json") {
		t.Fatalf("add_artwork error = %q", text)
	}
	if names := folderArtwork(t, fixture.cfg.ArtworkDir); len(names) != 0 {
		t.Fatalf("artwork was stored without a recorded membership: %v", names)
	}
}

func TestPreviewArtworkReturnsATVSizedJPEG(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	var added addArtworkOutput
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Christmas", "image_base64": base64Of(paintedJPEG(t, 1200, 1200, 6)),
	}, &added)
	before := folderArtwork(t, fixture.cfg.ArtworkDir)
	for _, reference := range []string{added.Filename, added.Key, added.Digest[:16]} {
		var output previewOutput
		result := fixture.call(t, "preview_artwork", map[string]any{"artwork": reference}, &output)
		if output.Width != 960 || output.Height != 540 || output.Fit.Mode != "mat" || output.Filename != added.Filename {
			t.Fatalf("preview_artwork(%s) = %+v", reference, output)
		}
		imageContent, ok := result.Content[0].(*mcp.ImageContent)
		if !ok || imageContent.MIMEType != "image/jpeg" {
			t.Fatalf("preview content = %#v, want a JPEG image first", result.Content)
		}
		decoded, err := jpeg.Decode(bytes.NewReader(imageContent.Data))
		if err != nil || decoded.Bounds().Dx() != 960 || decoded.Bounds().Dy() != 540 {
			t.Fatalf("preview JPEG = %v, %v, want a decodable 960x540 image", decoded, err)
		}
	}
	if after := folderArtwork(t, fixture.cfg.ArtworkDir); !slices.Equal(after, before) {
		t.Fatalf("preview changed the artwork folder: %v -> %v", before, after)
	}
	if text := fixture.callError(t, "preview_artwork", map[string]any{"artwork": "missing.jpg"}); !strings.Contains(text, "list_artwork") {
		t.Fatalf("missing preview error = %q", text)
	}
}

func TestListArtworkPagesAndFiltersByCollection(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	var tree, snow, everyday addArtworkOutput
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Christmas", "title": "Tree", "artist": "Model",
		"image_base64": base64Of(paintedJPEG(t, 64, 36, 10)),
	}, &tree)
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Christmas", "title": "Snow",
		"image_base64": base64Of(paintedJPEG(t, 64, 36, 20)),
	}, &snow)
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Holding", "title": "Everyday",
		"image_base64": base64Of(paintedJPEG(t, 64, 36, 30)),
	}, &everyday)
	fixture.call(t, "delete_collection", map[string]any{"collection": "Holding"}, nil)
	fixture.call(t, "update_collection", map[string]any{
		"collection": "Christmas", "activation": map[string]any{"mode": "window", "start": "12-01", "end": "01-06"},
	}, nil)
	fixture.artwork.placements = map[string][]string{tree.Digest: {"192.0.2.10"}}

	var first, second listArtworkOutput
	fixture.call(t, "list_artwork", map[string]any{"limit": 2}, &first)
	if first.Total != 3 || len(first.Artwork) != 2 || first.NextCursor != "2" {
		t.Fatalf("first page = %+v", first)
	}
	fixture.call(t, "list_artwork", map[string]any{"limit": 2, "cursor": first.NextCursor}, &second)
	if len(second.Artwork) != 1 || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
	byName := map[string]artworkSummary{}
	for _, summary := range append(first.Artwork, second.Artwork...) {
		byName[summary.Filename] = summary
	}
	treeSummary := byName[tree.Filename]
	if !treeSummary.SelectedToday || !slices.Equal(treeSummary.Collections, []string{"christmas"}) ||
		!slices.Equal(treeSummary.OnTV, []string{"192.0.2.10"}) || treeSummary.Title != "Tree" || treeSummary.Artist != "Model" {
		t.Fatalf("tree summary = %+v", treeSummary)
	}
	if everydaySummary := byName[everyday.Filename]; everydaySummary.SelectedToday || len(everydaySummary.Collections) != 0 {
		t.Fatalf("everyday summary = %+v, want hidden during the exclusive Christmas window", everydaySummary)
	}

	var filtered listArtworkOutput
	fixture.call(t, "list_artwork", map[string]any{"collection": "Christmas"}, &filtered)
	if filtered.Total != 2 {
		t.Fatalf("filtered list = %+v, want the two Christmas pictures", filtered)
	}
	for _, arguments := range []map[string]any{{"limit": 500}, {"cursor": "abc"}, {"collection": "Easter"}} {
		fixture.callError(t, "list_artwork", arguments)
	}
}

func TestRemoveFromCollectionKeepsTheFile(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	var added addArtworkOutput
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Christmas", "image_base64": base64Of(paintedJPEG(t, 64, 36, 40)),
	}, &added)
	var removed removeOutput
	fixture.call(t, "remove_from_collection", map[string]any{"collection": "Christmas", "artwork": added.Filename}, &removed)
	if removed.Removed != 1 || !strings.Contains(removed.Message, "file is kept") {
		t.Fatalf("remove_from_collection = %+v", removed)
	}
	if names := folderArtwork(t, fixture.cfg.ArtworkDir); !slices.Equal(names, []string{added.Filename}) {
		t.Fatalf("artwork folder = %v, want the file kept", names)
	}
	if text := fixture.callError(t, "remove_from_collection", map[string]any{
		"collection": "Christmas", "artwork": added.Filename,
	}); !strings.Contains(text, "is not in collection") {
		t.Fatalf("second remove error = %q", text)
	}
}

func TestUpdateCollectionReportsSuccessWhenTheSummaryCannotBeBuilt(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	fixture.call(t, "create_collection", map[string]any{"name": "Dutch Winter"}, nil)
	fixture.artwork.failPrepare(errors.New("artwork folder is offline"))

	var updated collectionChangeOutput
	fixture.call(t, "update_collection", map[string]any{
		"collection": "Dutch Winter", "activation": map[string]any{"mode": "always"},
	}, &updated)
	if updated.Collection.Slug != "dutch-winter" || updated.Collection.Activation.Mode != "always" || !updated.Collection.ActiveToday {
		t.Fatalf("update_collection = %+v, want the saved schedule", updated)
	}
	if !strings.Contains(updated.Message, "change was saved") || !strings.Contains(updated.Message, "unavailable") {
		t.Fatalf("message = %q, want it to say the change was saved and the summary is unavailable", updated.Message)
	}
	file, err := fixture.themes.Load(context.Background())
	if err != nil || len(file.Collections) != 1 || file.Collections[0].Activation.Mode != themes.ModeAlways {
		t.Fatalf("saved collections = %+v, %v; the reported change must be the live one", file, err)
	}
}

func TestAddArtworkReportsSuccessWhenTheStoredImageCannotBeDescribed(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t, fixtureOptions{})
	fixture.artwork.importResult = func(snapshot collection.Snapshot) collection.Snapshot {
		snapshot.Items = nil
		return snapshot
	}

	var added addArtworkOutput
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Christmas", "image_base64": base64Of(paintedJPEG(t, 640, 360, 9)), "title": "Skaters",
	}, &added)
	if added.Collection != "christmas" || !added.CollectionCreated || added.AlreadyInCollection ||
		!strings.HasPrefix(added.Filename, "skaters") || len(added.Digest) != 64 {
		t.Fatalf("add_artwork = %+v, want the committed collection, filename, and digest", added)
	}
	if warnings := strings.Join(added.Warnings, " "); !strings.Contains(warnings, "was saved") || !strings.Contains(warnings, "unavailable") {
		t.Fatalf("warnings = %q, want them to say the image was saved and its details are unavailable", warnings)
	}
	if names := folderArtwork(t, fixture.cfg.ArtworkDir); !slices.Equal(names, []string{added.Filename}) {
		t.Fatalf("artwork folder = %v, want the stored %s", names, added.Filename)
	}
	var listed listArtworkOutput
	fixture.call(t, "list_artwork", map[string]any{"collection": "christmas"}, &listed)
	if listed.Total != 1 || listed.Artwork[0].Filename != added.Filename {
		t.Fatalf("list_artwork = %+v, want the stored image in its collection", listed)
	}
}
