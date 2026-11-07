package optimize

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func TestOklabRoundTrip(t *testing.T) {
	t.Parallel()
	for _, rgb := range [][3]float64{{0, 0, 0}, {1, 1, 1}, {1, 0, 0}, {0.2, 0.5, 0.8}, {0.9, 0.85, 0.1}} {
		r, g, b := oklabFromLinear(rgb[0], rgb[1], rgb[2]).linear()
		if math.Abs(r-rgb[0]) > 1e-6 || math.Abs(g-rgb[1]) > 1e-6 || math.Abs(b-rgb[2]) > 1e-6 {
			t.Errorf("round trip %v = %v %v %v", rgb, r, g, b)
		}
	}
	if white := oklabFromLinear(1, 1, 1); math.Abs(white.l-1) > 1e-6 || math.Abs(white.a) > 1e-6 || math.Abs(white.b) > 1e-6 {
		t.Errorf("white = %+v, want L=1 a=b=0", white)
	}
}

func TestMatColorsFollowArt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		stats    paletteStats
		wantDark bool
	}{
		{name: "mid-tone art gets the dark mat", stats: paletteStats{mean: oklab{l: 0.6}, border: oklab{l: 0.6}}, wantDark: true},
		{name: "bright art gets the dark mat", stats: paletteStats{mean: oklab{l: 0.85}, border: oklab{l: 0.9}}, wantDark: true},
		{name: "near-black edge switches to the light mat", stats: paletteStats{mean: oklab{l: 0.3}, border: oklab{l: 0.2}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mat, bevel := matColors(test.stats, matToneAuto)
			if dark := mat.l < 0.5; dark != test.wantDark {
				t.Fatalf("mat lightness %.2f, want dark=%t", mat.l, test.wantDark)
			}
			if bevel.l <= mat.l {
				t.Errorf("bevel lightness %.2f must exceed mat %.2f", bevel.l, mat.l)
			}
			if chroma := math.Hypot(mat.a, mat.b); chroma > 0.0121 {
				t.Errorf("mat chroma %.4f too strong for a neutral mat", chroma)
			}
		})
	}
}

func TestMatToneOverridesAndNightBrightness(t *testing.T) {
	t.Parallel()
	lowKeyEdgeDark := paletteStats{mean: oklab{l: 0.25}, border: oklab{l: 0.22}}
	if mat, _ := matColors(lowKeyEdgeDark, matToneDark); mat.l != matDarkL {
		t.Errorf("dark tone mat lightness = %.2f, want %.2f even when the edge is dark", mat.l, matDarkL)
	}
	brightEdge := paletteStats{mean: oklab{l: 0.7}, border: oklab{l: 0.8}}
	if mat, _ := matColors(brightEdge, matToneLight); mat.l != matLightL {
		t.Errorf("light tone mat lightness = %.2f, want %.2f", mat.l, matLightL)
	}
	light, _ := matColors(paletteStats{mean: oklab{l: 0.6}, border: oklab{l: 0.6}}, matToneLight)
	if pixel := light.rgba(); max(pixel.R, pixel.G, pixel.B) > 200 {
		t.Errorf("light mat %+v is near-white; it must stay soft enough for a dark room", pixel)
	}
}

func TestMatTintIsBoundedTowardDominantHue(t *testing.T) {
	t.Parallel()
	mat, _ := matColors(paletteStats{mean: oklab{l: 0.6, a: 0, b: -0.2}, border: oklab{l: 0.6}}, matToneLight)
	if mat.b >= 0 || math.Hypot(mat.a, mat.b) > 0.0081 {
		t.Fatalf("light mat = %+v, want a faint blue tint of at most 0.008 chroma", mat)
	}
}

func TestRenderMatLayout(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 1920, 1080
	red := color.RGBA{R: 220, A: 255}

	single := renderMat([]*image.RGBA{solidImage(1000, 1000, red)}, cfg)
	margin := int(math.Round(matMarginFraction * 1080))
	top := firstRedRow(single, 960)
	if top < margin*9/10 {
		t.Fatalf("opening top %d leaves less than 90%% of the %dpx mat margin", top, margin)
	}
	height := 0
	for y := range 1080 {
		if isRed(single.RGBAAt(960, y)) {
			height++
		}
	}
	if bottom := 1080 - top - height; bottom < top {
		t.Errorf("opening is not optically centered: top %d, bottom %d", top, bottom)
	}
	if got := redSpan(single, 540); got != height {
		t.Errorf("square art rendered %dx%d, want square", got, height)
	}

	small := renderMat([]*image.RGBA{solidImage(100, 50, red)}, cfg)
	if got := redSpan(small, 540); got != 300 {
		t.Errorf("small art width = %d, want 300 (capped at 3x)", got)
	}

	pair := renderMat([]*image.RGBA{solidImage(200, 300, red), solidImage(100, 200, red)}, cfg)
	row := firstRedRow(pair, 700) + 5
	var runs []int
	run := 0
	for x := range 1920 {
		if isRed(pair.RGBAAt(x, row)) {
			run++
		} else if run > 0 {
			runs = append(runs, run)
			run = 0
		}
	}
	if len(runs) != 2 {
		t.Fatalf("double mat shows %d openings on row %d, want 2", len(runs), row)
	}
	if left, right := firstRedRow(pair, 700), firstRedRow(pair, 1220); left != right {
		t.Errorf("openings not aligned: tops %d and %d", left, right)
	}
}
