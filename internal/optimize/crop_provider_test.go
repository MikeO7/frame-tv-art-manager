package optimize

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// cropProviderServer answers every request with proposal and counts the TCP
// connections opened to it.
func cropProviderServer(t *testing.T, proposal cropProposal) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var connections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(writer).Encode(proposal); err != nil {
			t.Errorf("encode crop proposal: %v", err)
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, &connections
}

func providerConfig(url string, width, height int) Config {
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = width, height
	cfg.SmartCropEnabled, cfg.SmartCropProvider, cfg.SmartCropProviderURL = true, smartCropProviderHTTP, url
	cfg.SmartCropProviderTimeout = 5 * time.Second
	cfg.SmartCropProviderMinConfidence = 0.5
	cfg.MaxCropPercent, cfg.MaxUpscale = 100, 0
	return cfg
}

func TestProviderCropIsSharpenedForTheScaleOfItsBox(t *testing.T) {
	// The box is a quarter of the source's width, so filling a 200x200 frame
	// enlarges it twice: past the point where sharpening adds anything. The
	// whole 400x400 source would have been halved, which is boosted.
	server, _ := cropProviderServer(t, cropProposal{X: 150, Y: 150, Width: 100, Height: 100, Confidence: 0.9})
	source := image.NewRGBA(image.Rect(0, 0, 400, 400))
	for y := range 400 {
		for x := range 400 {
			level := uint8(60 + 130*((x/3+y/5)%2))
			offset := source.PixOffset(x, y)
			source.Pix[offset], source.Pix[offset+1], source.Pix[offset+2], source.Pix[offset+3] = level, level, level, 255
		}
	}
	render := func(amount float64) *image.RGBA {
		cfg := providerConfig(server.URL, 200, 200)
		cfg.SharpenAmount = amount
		env := newRenderEnv(cfg, discardLogger())
		defer env.close()
		return fitImage(context.Background(), source, env)
	}
	if plain, sharpened := render(0), render(1); !bytes.Equal(plain.Pix, sharpened.Pix) {
		t.Fatal("a provider crop enlarged twice was sharpened as if the whole source had been reduced")
	}
}

func TestCropScaleOfALocalCropIsTheSourceFillScale(t *testing.T) {
	targets := []image.Point{{X: 3840, Y: 2160}, {X: 1920, Y: 2160}, {X: 100, Y: 100}, {X: 1921, Y: 1081}}
	sources := []image.Point{
		{X: 4000, Y: 3000},
		{X: 3000, Y: 4000},
		{X: 5001, Y: 1777},
		{X: 1777, Y: 5001},
		{X: 7680, Y: 4320},
		{X: 641, Y: 479},
		{X: 1, Y: 50},
		{X: 50, Y: 1},
		{X: 3841, Y: 2161},
	}
	for _, target := range targets {
		for _, size := range sources {
			source := image.NewRGBA(image.Rectangle{Max: size})
			crop := cropRectForAspectWithProtection(source, float64(target.X)/float64(target.Y), false, 0, false, 0)
			got := cropScale(source.Bounds(), crop, target.X, target.Y)
			if want := fillScale(size.X, size.Y, target.X, target.Y); got != want {
				t.Errorf("cropScale(%v source, %v crop, %v target) = %v, want exactly %v", size, crop, target, got, want)
			}
		}
	}
	// A provider box inside the source is scaled on its own size.
	source, box := image.Rect(0, 0, 7680, 4320), image.Rect(100, 100, 2020, 1180)
	if got := cropScale(source, box, 3840, 2160); got != 2 {
		t.Errorf("cropScale(provider box) = %v, want 2", got)
	}
}

func TestCatalogPassReusesOneCropProviderConnection(t *testing.T) {
	server, connections := cropProviderServer(t, cropProposal{X: 0, Y: 0, Width: 20, Height: 20, Confidence: 0.9})
	dir := t.TempDir()
	names := []string{"one.jpg", "two.jpg", "three.jpg"}
	for _, name := range names {
		writeTestImage(t, filepath.Join(dir, name), 40, 24)
	}
	pass, err := runTestPass(context.Background(), dir, providerConfig(server.URL, 20, 20), names...)
	if err != nil || pass.optimized != len(names) {
		t.Fatalf("run() optimized %d, error %v; want %d and nil", pass.optimized, err, len(names))
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("crop provider saw %d connections for %d pictures, want one reused connection", got, len(names))
	}
}

func TestAdvancedCropWithoutAClientFallsBackToTheLocalCrop(t *testing.T) {
	cfg := providerConfig("http://127.0.0.1:1", 100, 100)
	source := image.NewRGBA(image.Rect(0, 0, 200, 100))
	env := renderEnv{cfg: cfg, logger: discardLogger()}
	env.close()
	if got, want := cropRectWithPolicy(context.Background(), source, 100, 100, env).Size(), image.Pt(100, 100); got != want {
		t.Fatalf("crop size without a client = %v, want the local %v", got, want)
	}
}
