package optimize

import (
	"context"
	"encoding/json"
	"image"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProtectedCropWindowFlushWithTheImageEdgeIsNotPenalized(t *testing.T) {
	t.Parallel()
	const width, height, window, subject = 64, 36, 48, 8
	for _, side := range []string{"left", "right"} {
		t.Run(side, func(t *testing.T) {
			t.Parallel()
			first, flush := 0, 0
			if side == "right" {
				first, flush = width-subject, width-window
			}
			// The subject is protected detail that reaches the image edge.
			detail := make([]float64, width*height)
			for y := range height {
				for x := first; x < first+subject; x++ {
					detail[y*width+x] = 1
				}
			}
			windows := cropWindows{
				saliency:   calculateIntegralImage(detail, width, height),
				protection: calculateIntegralImage(detail, width, height),
				workW:      width, workH: height, windowW: window, windowH: height,
				horizontal: true, strength: 0.35,
			}
			if got, want := windows.score(flush), float64(subject*height); got != want {
				t.Fatalf("flush window score = %v, want the unpenalized %v", got, want)
			}
			if got := windows.best(); got != flush {
				t.Fatalf("best offset = %d, want the flush window at %d", got, flush)
			}
		})
	}
}

func TestCropProposalValidationRejectsOverflowingBoxes(t *testing.T) {
	t.Parallel()
	bounds := image.Rect(0, 0, 4000, 3000)
	aspect := 16.0 / 9.0
	for name, proposal := range map[string]cropProposal{
		"x overflows":    {X: math.MaxInt - 8, Width: 16, Height: 9, Confidence: 0.9},
		"y overflows":    {Y: math.MaxInt - 4, Width: 16, Height: 9, Confidence: 0.9},
		"wider than src": {Width: 4800, Height: 2700, Confidence: 0.9},
		"past the right": {X: 3990, Width: 16, Height: 9, Confidence: 0.9},
	} {
		if err := validateCropProposal(proposal, bounds, aspect); err == nil {
			t.Errorf("%s: proposal %+v was accepted", name, proposal)
		}
	}
	if err := validateCropProposal(cropProposal{X: 3984, Y: 2991, Width: 16, Height: 9, Confidence: 0.9}, bounds, aspect); err != nil {
		t.Fatalf("box flush with the far corner rejected: %v", err)
	}
}

func TestProviderCropIsSnappedToAspectAndBoundedByUpscale(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		proposal cropProposal
		want     image.Rectangle
	}{
		{
			// 4000x2272 is 0.98% taller than 16:9: accepted, then trimmed.
			name: "slightly tall box", proposal: cropProposal{Y: 300, Width: 4000, Height: 2272, Confidence: 0.9},
			want: image.Rect(0, 311, 4000, 2561),
		},
		{
			// A 16x9 box would be enlarged 240 times; the local centre crop is used.
			name: "tiny box", proposal: cropProposal{X: 10, Y: 10, Width: 16, Height: 9, Confidence: 0.9},
			want: image.Rect(0, 375, 4000, 2625),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(writer).Encode(test.proposal)
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.SmartCropEnabled, cfg.SmartCropProvider = true, smartCropProviderHTTP
			cfg.SmartCropProviderURL, cfg.SmartCropProviderTimeout = server.URL, 5*time.Second
			cfg.SmartCropMinGain = math.Inf(1)
			src := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
			got := cropRectWithPolicy(context.Background(), src, 3840, 2160, cfg, discardLogger())
			if got != test.want {
				t.Fatalf("crop = %v, want %v", got, test.want)
			}
			if aspect := float64(got.Dx()) / float64(got.Dy()); math.Abs(aspect/(16.0/9.0)-1) > 5e-4 {
				t.Fatalf("crop aspect = %.5f", aspect)
			}
		})
	}
}

func TestPaddedPictureIsSharpenedAtTheScaleItWasResizedBy(t *testing.T) {
	t.Parallel()
	// A portrait reduced to fit a mat opening or a blur pad. Filling the frame
	// would enlarge it almost twice, which used to switch sharpening off.
	portrait := resampleFixture(200, 300, false)
	for _, style := range []string{"mat", padStyleBlur} {
		t.Run(style, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			cfg.MaxWidth, cfg.MaxHeight = 384, 216
			cfg.PadStyle = style
			cfg.SmartCropEnabled = false
			cfg.SharpenThreshold = 0
			if fill := fillScale(200, 300, cfg.MaxWidth, cfg.MaxHeight); fill <= 1.5 {
				t.Fatalf("invalid case: fill scale %.2f would be sharpened anyway", fill)
			}
			sharpened := fitImage(t.Context(), portrait, cfg, discardLogger())
			cfg.SharpenAmount = 0
			plain := fitImage(t.Context(), portrait, cfg, discardLogger())
			changed := 0
			for index := range plain.Pix {
				if plain.Pix[index] != sharpened.Pix[index] {
					changed++
				}
			}
			if changed == 0 {
				t.Fatal("the downscaled picture was not sharpened")
			}
			// Nothing outside the picture changes: mats and backgrounds stay flat.
			for _, corner := range []image.Point{{2, 2}, {381, 2}, {2, 213}, {381, 213}} {
				if plain.RGBAAt(corner.X, corner.Y) != sharpened.RGBAAt(corner.X, corner.Y) {
					t.Fatalf("pixel %v outside the picture changed", corner)
				}
			}
		})
	}
}
