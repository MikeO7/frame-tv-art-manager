package optimize

import (
	"context"
	"image"
	"image/color"
	std_draw "image/draw"
	"log/slog"
	"net/http"
)

// renderEnv is what one catalog pass or preview renders with. cropClient
// calls the advanced crop provider and is shared by every picture of the
// pass, so its connections are reused; close releases them.
type renderEnv struct {
	cfg        Config
	logger     *slog.Logger
	cropClient *http.Client
}

// newRenderEnv builds the dependencies for one pass. The caller must call
// close when the pass ends.
func newRenderEnv(cfg Config, logger *slog.Logger) renderEnv {
	return renderEnv{cfg: cfg, logger: logger, cropClient: newCropClient(cfg)}
}

// close releases the crop provider's idle connections.
func (env renderEnv) close() {
	if env.cropClient != nil {
		env.cropClient.CloseIdleConnections()
	}
}

// renderCollage joins two portraits. The mat style shows both whole, at equal
// height, in a double-opening mat; the blur style fills each half of the frame
// by cropping and separates the halves with a thin seam.
func renderCollage(ctx context.Context, img1, img2 *image.RGBA, env renderEnv) *image.RGBA {
	if env.cfg.PadStyle != padStyleBlur {
		return renderMat([]*image.RGBA{img1, img2}, env.cfg)
	}
	targetWidth, targetHeight := env.cfg.MaxWidth, env.cfg.MaxHeight
	canvas := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	leftWidth := targetWidth / 2
	rightWidth := targetWidth - leftWidth

	left := cropAndSharpen(ctx, img1, leftWidth, targetHeight, env)
	right := cropAndSharpen(ctx, img2, rightWidth, targetHeight, env)

	std_draw.Draw(canvas, image.Rect(0, 0, leftWidth, targetHeight), left, left.Bounds().Min, std_draw.Src)
	std_draw.Draw(canvas, image.Rect(leftWidth, 0, targetWidth, targetHeight), right, right.Bounds().Min, std_draw.Src)

	// Dark charcoal / off-black seam centered on the panel boundary.
	dividerColor := color.RGBA{R: 18, G: 18, B: 18, A: 255}
	dividerHalfWidth := max(1, targetWidth/1920)
	divider := image.Rect(leftWidth-dividerHalfWidth, 0, leftWidth+dividerHalfWidth, targetHeight)
	std_draw.Draw(canvas, divider, image.NewUniform(dividerColor), image.Point{}, std_draw.Src)

	return canvas
}
