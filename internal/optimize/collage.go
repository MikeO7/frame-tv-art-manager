package optimize

import (
	"context"
	"image"
	"image/color"
	std_draw "image/draw"
	"log/slog"
)

type collageRenderRequest struct {
	ctx                       context.Context
	cfg                       Config
	logger                    *slog.Logger
	leftSharpen, rightSharpen float64
	sharpenThreshold, workers int
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
