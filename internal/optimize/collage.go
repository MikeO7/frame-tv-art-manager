package optimize

import (
	"context"
	"image"
	"image/color"
	std_draw "image/draw"
	"log/slog"
)

const (
	collageWidth  = 3840
	collageHeight = 2160
)

type collageRenderRequest struct {
	ctx                       context.Context
	cfg                       Config
	logger                    *slog.Logger
	leftSharpen, rightSharpen float64
	sharpenThreshold, workers int
}

// CreateCollage joins two upright portrait images side-by-side into a single
// 4K (3840x2160) landscape canvas, center-cropping each to a half-width panel
// and drawing a clean dark divider line down the seam.
func CreateCollage(img1, img2 *image.RGBA, smart bool) *image.RGBA {
	return CreateCollageForTarget(img1, img2, collageWidth, collageHeight, smart)
}

// CreateCollageForTarget joins two upright portraits into the requested
// landscape pixel contract.
func CreateCollageForTarget(img1, img2 *image.RGBA, targetWidth, targetHeight int, smart bool) *image.RGBA {
	return createCollageForTarget(img1, img2, targetWidth, targetHeight, smart, 0.03, true)
}

//nolint:revive // explicit target and crop-quality controls keep this renderer independent of global config
func createCollageForTarget(
	img1, img2 *image.RGBA,
	targetWidth, targetHeight int,
	smart bool,
	minGain float64,
	linearLight bool,
) *image.RGBA {
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = targetWidth, targetHeight
	cfg.SmartCropEnabled, cfg.SmartCropMinGain, cfg.LinearLightResize = smart, minGain, linearLight
	return renderCollage(img1, img2, collageRenderRequest{
		ctx: context.Background(), cfg: cfg, logger: slog.New(slog.DiscardHandler), workers: defaultPixelWorkers(),
	})
}

func renderCollage(img1, img2 *image.RGBA, request collageRenderRequest) *image.RGBA {
	targetWidth, targetHeight := request.cfg.MaxWidth, request.cfg.MaxHeight
	canvas := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	leftWidth := targetWidth / 2
	rightWidth := targetWidth - leftWidth

	left := centerCropConfigured(request.ctx, img1, leftWidth, targetHeight, request.cfg, request.logger)
	right := centerCropConfigured(request.ctx, img2, rightWidth, targetHeight, request.cfg, request.logger)
	left = sharpenWithOptions(left, request.leftSharpen, request.sharpenThreshold, request.workers)
	right = sharpenWithOptions(right, request.rightSharpen, request.sharpenThreshold, request.workers)

	std_draw.Draw(canvas, image.Rect(0, 0, leftWidth, targetHeight), left, left.Bounds().Min, std_draw.Src)
	std_draw.Draw(canvas, image.Rect(leftWidth, 0, targetWidth, targetHeight), right, right.Bounds().Min, std_draw.Src)

	// Dark charcoal / off-black seam centered on the panel boundary.
	dividerColor := color.RGBA{R: 18, G: 18, B: 18, A: 255}
	dividerHalfWidth := max(1, targetWidth/1920)
	for y := 0; y < targetHeight; y++ {
		for x := max(0, leftWidth-dividerHalfWidth); x < min(targetWidth, leftWidth+dividerHalfWidth); x++ {
			canvas.Set(x, y, dividerColor)
		}
	}

	return canvas
}
