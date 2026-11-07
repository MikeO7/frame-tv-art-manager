package sync

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MikeO7/frame-tv-art-manager/internal/assistant"
	collectionpkg "github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
)

// TestAssistantChristmasCollectionBecomesTheDesiredSet drives the real MCP
// handler against a real managed engine: two images added to "christmas"
// and scheduled for Dec 1 to Jan 6 are exactly what a Christmas Eve Sync
// Cycle wants on the TV, and in July only the everyday picture is wanted.
func TestAssistantChristmasCollectionBecomesTheDesiredSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg, _, err := config.LoadEnviron([]string{
		"TV_IPS=192.0.2.10", "ARTWORK_DIR=" + t.TempDir(), "TOKEN_DIR=" + t.TempDir(),
		"LOCATION_TIMEZONE=America/New_York",
	})
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	logger := slog.New(slog.DiscardHandler)
	managed, err := NewManagedEngine(ctx, cfg, logger, health.NewStatus())
	if err != nil {
		t.Fatalf("NewManagedEngine() error = %v", err)
	}
	t.Cleanup(func() { _ = managed.Close(context.Background()) })
	if _, err := managed.Import(ctx, collectionpkg.ImportRequest{
		Hint: "everyday-harbor", Reader: bytes.NewReader(e2eJPEG(t, 3)),
	}); err != nil {
		t.Fatalf("import everyday art: %v", err)
	}

	handler, err := assistant.NewHandler(assistant.Options{
		Config: cfg, Logger: logger, Artwork: managed, Themes: managed.Themes(), Status: health.NewStatus(), Version: "e2e",
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-assistant", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL, HTTPClient: server.Client(), DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	callTool := func(name string, arguments map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("%s = %+v, %v", name, result, err)
		}
		return result
	}
	added := make([]string, 0, 2)
	for index, title := range []string{"Skaters at Dusk", "Snowy Canal"} {
		result := callTool("add_artwork", map[string]any{
			"collection": "christmas", "title": title,
			"image_base64": base64.StdEncoding.EncodeToString(e2eJPEG(t, uint8(index+10))),
		})
		structured, ok := result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("add_artwork structured content = %T", result.StructuredContent)
		}
		filename, _ := structured["filename"].(string)
		added = append(added, filename)
	}
	callTool("update_collection", map[string]any{
		"collection": "christmas", "activation": map[string]any{"mode": "window", "start": "12-01", "end": "01-06"},
	})

	snapshot, err := managed.Prepare(ctx, collectionpkg.PrepareRequest{DryRun: true})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if len(snapshot.Items) != 3 {
		t.Fatalf("collection holds %d items, want the everyday picture and two Christmas pictures", len(snapshot.Items))
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		now  time.Time
		want []string
	}{
		"christmas eve": {now: time.Date(2026, time.December, 24, 20, 0, 0, 0, newYork), want: added},
		// 04:30 UTC on Dec 1 is still Nov 30 in New York, before the window.
		"the evening before the window opens": {
			now: time.Date(2026, time.December, 1, 4, 30, 0, 0, time.UTC), want: everydayNames(snapshot, added),
		},
		"july": {now: time.Date(2027, time.July, 4, 12, 0, 0, 0, newYork), want: everydayNames(snapshot, added)},
	} {
		desired, err := managed.engine.selectThemedArtwork(ctx, snapshot, test.now, logger)
		if err != nil {
			t.Fatalf("%s: selectThemedArtwork() error = %v", name, err)
		}
		got := make([]string, 0, len(desired.snapshot.Items))
		for _, item := range desired.snapshot.Items {
			got = append(got, item.Name)
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(test.want))
		if !slices.Equal(got, want) {
			t.Fatalf("%s: desired = %v, want %v", name, got, want)
		}
	}
}

func everydayNames(snapshot collectionpkg.Snapshot, themed []string) []string {
	var names []string
	for _, item := range snapshot.Items {
		if !slices.Contains(themed, item.Name) {
			names = append(names, item.Name)
		}
	}
	return names
}

// e2eJPEG returns a distinct 16:9 JPEG.
func e2eJPEG(t *testing.T, seed uint8) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := range 36 {
		for x := range 64 {
			canvas.Set(x, y, color.RGBA{R: uint8(x*3) + seed, G: uint8(y*5) + seed, B: seed, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, canvas, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
