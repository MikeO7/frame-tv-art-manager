package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MikeO7/frame-tv-art-manager/internal/collection"
	"github.com/MikeO7/frame-tv-art-manager/internal/config"
	"github.com/MikeO7/frame-tv-art-manager/internal/health"
	"github.com/MikeO7/frame-tv-art-manager/internal/optimize"
	"github.com/MikeO7/frame-tv-art-manager/internal/themes"
)

// christmasEve is the fixed clock used by every MCP test.
var christmasEve = time.Date(2026, time.December, 24, 18, 0, 0, 0, time.UTC)

type testArtwork struct {
	collection.Store
	cfg        *config.Config
	mu         gosync.Mutex
	requests   int
	pending    bool
	placements map[string][]string
}

func (artwork *testArtwork) RenderPreview(ctx context.Context, item collection.Item) (*image.RGBA, error) {
	return optimize.RenderPreview(ctx, optimize.PreviewInput{Path: item.Path}, artwork.cfg.OptimizeOptions(), nil)
}

func (artwork *testArtwork) Placements() map[string][]string {
	artwork.mu.Lock()
	defer artwork.mu.Unlock()
	return artwork.placements
}

func (artwork *testArtwork) RequestSync() bool {
	artwork.mu.Lock()
	defer artwork.mu.Unlock()
	artwork.requests++
	if artwork.pending {
		return false
	}
	artwork.pending = true
	return true
}

type fixture struct {
	cfg     *config.Config
	artwork *testArtwork
	themes  *themes.Store
	status  *health.Status
	session *mcp.ClientSession
}

type fixtureOptions struct {
	environ  []string
	fetcher  *fetcher
	searcher artSearch
	// noSearcher builds the handler without a museum searcher.
	noSearcher bool
}

func newFixture(t *testing.T, options fixtureOptions) *fixture {
	t.Helper()
	environ := append([]string{
		"TV_IPS=192.0.2.10", "ARTWORK_DIR=" + t.TempDir(), "TOKEN_DIR=" + t.TempDir(), "MAX_DOWNLOAD_SIZE_MB=2",
	}, options.environ...)
	cfg, _, err := config.LoadEnviron(environ)
	if err != nil {
		t.Fatalf("LoadEnviron() error = %v", err)
	}
	store, err := collection.New(collection.Config{Root: cfg.ArtworkDir, MaxImportBytes: 20 << 20})
	if err != nil {
		t.Fatal(err)
	}
	themeStore, err := themes.NewStore(cfg.ArtworkDir, cfg.DryRun)
	if err != nil {
		t.Fatal(err)
	}
	artwork := &testArtwork{Store: dryRunStore{Store: store, dryRun: cfg.DryRun}, cfg: cfg, placements: map[string][]string{}}
	status := health.NewStatus()
	searcher := options.searcher
	if searcher == nil && !options.noSearcher {
		searcher = &fakeSearcher{}
	}
	handler, err := NewHandler(Options{
		Config: cfg, Logger: slog.New(slog.DiscardHandler), Artwork: artwork, Themes: themeStore, Status: status,
		Fetcher: options.fetcher, Searcher: searcher, Now: func() time.Time { return christmasEve }, Version: "test",
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-assistant", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: server.URL, HTTPClient: server.Client(), DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &fixture{cfg: cfg, artwork: artwork, themes: themeStore, status: status, session: session}
}

// dryRunStore applies DRY_RUN to imports the way the managed engine does.
type dryRunStore struct {
	collection.Store
	dryRun bool
}

func (store dryRunStore) Import(ctx context.Context, request collection.ImportRequest) (collection.Snapshot, error) {
	request.DryRun = request.DryRun || store.dryRun
	return store.Store.Import(ctx, request)
}

func (store dryRunStore) Prepare(ctx context.Context, request collection.PrepareRequest) (collection.Snapshot, error) {
	request.DryRun = request.DryRun || store.dryRun
	return store.Store.Prepare(ctx, request)
}

// call invokes a tool and decodes its structured result into output. It
// fails the test when the tool reports an error.
func (fixture *fixture) call(t *testing.T, name string, arguments map[string]any, output any) *mcp.CallToolResult {
	t.Helper()
	result := fixture.callRaw(t, name, arguments)
	if result.IsError {
		t.Fatalf("%s returned a tool error: %s", name, toolText(result))
	}
	if output != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, output); err != nil {
			t.Fatalf("decode %s output: %v", name, err)
		}
	}
	return result
}

// callError invokes a tool that must fail and returns its error text.
func (fixture *fixture) callError(t *testing.T, name string, arguments map[string]any) string {
	t.Helper()
	result := fixture.callRaw(t, name, arguments)
	if !result.IsError {
		t.Fatalf("%s succeeded, want a tool error", name)
	}
	return toolText(result)
}

func (fixture *fixture) callRaw(t *testing.T, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := fixture.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("CallTool(%s) protocol error = %v", name, err)
	}
	return result
}

func toolText(result *mcp.CallToolResult) string {
	var text strings.Builder
	for _, content := range result.Content {
		if textContent, ok := content.(*mcp.TextContent); ok {
			text.WriteString(textContent.Text)
		}
	}
	return text.String()
}

// paintedJPEG returns a distinct, fully decodable JPEG of the given size.
func paintedJPEG(t *testing.T, width, height int, seed uint8) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			canvas.Set(x, y, color.RGBA{R: uint8(x*7) + seed, G: uint8(y*5) + seed, B: seed, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, canvas, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func paintedPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for index := range canvas.Pix {
		canvas.Pix[index] = uint8(index)
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func base64Of(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
