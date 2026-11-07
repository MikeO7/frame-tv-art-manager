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
}

// renderCollage joins two portraits. The mat style shows both whole, at equal
// height, in a double-opening mat; the blur style fills each half of the frame
// by cropping and separates the halves with a thin seam.
func renderCollage(img1, img2 *image.RGBA, request collageRenderRequest) *image.RGBA {
	if request.cfg.PadStyle != padStyleBlur {
		return sharpen(renderMat([]*image.RGBA{img1, img2}, request.cfg), max(request.leftSharpen, request.rightSharpen), request.cfg.SharpenThreshold)
	}
	targetWidth, targetHeight := request.cfg.MaxWidth, request.cfg.MaxHeight
	canvas := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	leftWidth := targetWidth / 2
	rightWidth := targetWidth - leftWidth

	left := centerCropConfigured(request.ctx, img1, leftWidth, targetHeight, request.cfg, request.logger)
	right := centerCropConfigured(request.ctx, img2, rightWidth, targetHeight, request.cfg, request.logger)
	left = sharpen(left, request.leftSharpen, request.cfg.SharpenThreshold)
	right = sharpen(right, request.rightSharpen, request.cfg.SharpenThreshold)

	std_draw.Draw(canvas, image.Rect(0, 0, leftWidth, targetHeight), left, left.Bounds().Min, std_draw.Src)
	std_draw.Draw(canvas, image.Rect(leftWidth, 0, targetWidth, targetHeight), right, right.Bounds().Min, std_draw.Src)

	// Dark charcoal / off-black seam centered on the panel boundary.
	dividerColor := color.RGBA{R: 18, G: 18, B: 18, A: 255}
	dividerHalfWidth := max(1, targetWidth/1920)
	divider := image.Rect(leftWidth-dividerHalfWidth, 0, leftWidth+dividerHalfWidth, targetHeight)
	std_draw.Draw(canvas, divider, image.NewUniform(dividerColor), image.Point{}, std_draw.Src)

	return canvas
}
