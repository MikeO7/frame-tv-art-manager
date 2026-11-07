package optimize

import (
	"image"
	"image/color"
	std_draw "image/draw"
	"math"

	"golang.org/x/image/draw"
)

const (
	// matMarginFraction is the mat border as a share of the frame's short side.
	matMarginFraction = 0.07
	// matTopShare places the opening slightly above center, as framers do, so
	// the art does not look like it is sliding down the mat.
	matTopShare = 0.47
	// matBevelFraction is the width of the lighter cut-core bevel around each
	// opening as a share of the frame's short side.
	matBevelFraction = 0.0025
	// matMinContrast is the minimum Oklab distance between the mat and the
	// image's outer edge, so the art never blends into its mat.
	matMinContrast = 0.1
	matProxySize   = 128
	// matLightL is a soft greige rather than paper white: a large near-white
	// area glows on an emissive panel in a dark room.
	matLightL    = 0.8
	matDarkL     = 0.22
	matToneAuto  = "auto"
	matToneLight = "light"
	matToneDark  = "dark"
)

// oklab is a color in Björn Ottosson's Oklab perceptual space.
type oklab struct{ l, a, b float64 }

func (c oklab) distance(other oklab) float64 {
	return math.Sqrt((c.l-other.l)*(c.l-other.l) + (c.a-other.a)*(c.a-other.a) + (c.b-other.b)*(c.b-other.b))
}

// oklabFromLinear converts linear-light sRGB to Oklab.
func oklabFromLinear(red, green, blue float64) oklab {
	l := math.Cbrt(0.4122214708*red + 0.5363325363*green + 0.0514459929*blue)
	m := math.Cbrt(0.2119034982*red + 0.6806995451*green + 0.1073969566*blue)
	s := math.Cbrt(0.0883024619*red + 0.2817188376*green + 0.6299787005*blue)
	return oklab{
		l: 0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		a: 1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		b: 0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}
}

// linear converts an Oklab color back to linear-light sRGB.
func (c oklab) linear() (float64, float64, float64) {
	l := c.l + 0.3963377774*c.a + 0.2158037573*c.b
	m := c.l - 0.1055613458*c.a - 0.0638541728*c.b
	s := c.l - 0.0894841775*c.a - 1.2914855480*c.b
	l, m, s = l*l*l, m*m*m, s*s*s
	return 4.0767416621*l - 3.3077115913*m + 0.2309699292*s,
		-1.2684380046*l + 2.6097574011*m - 0.3413193965*s,
		-0.0041960863*l - 0.7034186147*m + 1.7076147010*s
}

// rgba encodes an Oklab color as opaque 8-bit sRGB.
func (c oklab) rgba() color.RGBA {
	tables := srgbLookup()
	red, green, blue := c.linear()
	return color.RGBA{R: tables.encode(red), G: tables.encode(green), B: tables.encode(blue), A: 255}
}

// paletteStats summarizes the colors of one or more images in Oklab.
type paletteStats struct {
	mean, border oklab
}

// measurePalette returns the mean color of every opaque pixel and the mean of
// the outer ring that will touch the mat, computed on small proxies.
func measurePalette(images []*image.RGBA) paletteStats {
	decode := &srgbLookup().decode
	var all, edge oklab
	var allCount, edgeCount float64
	for _, src := range images {
		proxy := paletteProxy(src)
		width, height := proxy.Bounds().Dx(), proxy.Bounds().Dy()
		ring := max(1, min(width, height)/16)
		for y := range height {
			for x := range width {
				pixel := proxy.RGBAAt(x, y)
				if pixel.A < 128 {
					continue
				}
				c := oklabFromLinear(decode[pixel.R], decode[pixel.G], decode[pixel.B])
				all = oklab{all.l + c.l, all.a + c.a, all.b + c.b}
				allCount++
				if x < ring || y < ring || x >= width-ring || y >= height-ring {
					edge = oklab{edge.l + c.l, edge.a + c.a, edge.b + c.b}
					edgeCount++
				}
			}
		}
	}
	average := func(sum oklab, count float64) oklab {
		if count == 0 {
			return oklab{l: 0.5}
		}
		return oklab{sum.l / count, sum.a / count, sum.b / count}
	}
	return paletteStats{mean: average(all, allCount), border: average(edge, edgeCount)}
}

func paletteProxy(src *image.RGBA) *image.RGBA {
	width, height := src.Bounds().Dx(), src.Bounds().Dy()
	if width <= matProxySize && height <= matProxySize {
		return src
	}
	scale := float64(matProxySize) / float64(max(width, height))
	proxy := image.NewRGBA(image.Rect(0, 0, max(int(float64(width)*scale), 1), max(int(float64(height)*scale), 1)))
	draw.ApproxBiLinear.Scale(proxy, proxy.Bounds(), src, src.Bounds(), draw.Src, nil)
	return proxy
}

// matColors picks a gallery mat and its bevel from the art's palette. Auto
// tone uses a deep charcoal mat, which stays calm in a dark room and frames
// art the way gallery lighting does, and switches to a soft warm greige only
// when the art's own edge is too dark to stand out against charcoal. Light and
// dark force one tone. Both are faintly tinted toward the art's dominant hue.
func matColors(stats paletteStats, tone string) (oklab, oklab) {
	tint := func(maxChroma float64, fallback oklab) (float64, float64) {
		chroma := math.Hypot(stats.mean.a, stats.mean.b)
		if chroma < 0.01 {
			return fallback.a, fallback.b
		}
		scale := min(chroma, maxChroma) / chroma
		return stats.mean.a * scale, stats.mean.b * scale
	}
	lightA, lightB := tint(0.008, oklab{a: 0.002, b: 0.01})
	darkA, darkB := tint(0.012, oklab{})
	light := oklab{l: matLightL, a: lightA, b: lightB}
	dark := oklab{l: matDarkL, a: darkA, b: darkB}

	preferred, other := dark, light
	if tone == matToneLight {
		preferred = light
	}
	if tone == matToneAuto && preferred.distance(stats.border) < matMinContrast &&
		other.distance(stats.border) > preferred.distance(stats.border) {
		preferred = other
	}
	bevel := oklab{l: min(preferred.l+0.05, 0.99), a: preferred.a / 2, b: preferred.b / 2}
	return preferred, bevel
}

// renderMat places one or two images, whole and at equal height, in openings
// cut into a single gallery mat that fills the frame.
func renderMat(images []*image.RGBA, cfg Config) *image.RGBA {
	width, height := cfg.MaxWidth, cfg.MaxHeight
	margin := max(1, int(math.Round(matMarginFraction*float64(min(width, height)))))
	bevelWidth := max(1, int(math.Round(matBevelFraction*float64(min(width, height)))))
	count := len(images)
	boxWidth := max(1, (width-2*margin-(count-1)*margin)/count)
	boxHeight := max(1, height-2*margin)

	sizes := make([]image.Point, count)
	openingHeight := boxHeight
	for index, src := range images {
		w, h := containedSize(src.Bounds().Dx(), src.Bounds().Dy(), boxWidth, boxHeight, cfg.MaxUpscale)
		sizes[index] = image.Pt(w, h)
		openingHeight = min(openingHeight, h)
	}
	groupWidth := (count - 1) * margin
	for index, src := range images {
		aspect := float64(src.Bounds().Dx()) / float64(src.Bounds().Dy())
		w := min(max(int(math.Round(aspect*float64(openingHeight))), 1), boxWidth)
		sizes[index] = image.Pt(w, openingHeight)
		groupWidth += w
	}

	matColor, bevelColor := matColors(measurePalette(images), cfg.MatTone)
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	std_draw.Draw(canvas, canvas.Bounds(), image.NewUniform(matColor.rgba()), image.Point{}, std_draw.Src)
	x := (width - groupWidth) / 2
	y := int(math.Round(float64(height-openingHeight) * matTopShare))
	for index, src := range images {
		opening := image.Rect(x, y, x+sizes[index].X, y+sizes[index].Y)
		drawBevel(canvas, opening, bevelWidth, bevelColor.rgba())
		scaled := resizeCrop(src, src.Bounds(), opening.Dx(), opening.Dy(), cfg.LinearLightResize)
		std_draw.Draw(canvas, opening, scaled, image.Point{}, std_draw.Over)
		x += opening.Dx() + margin
	}
	return canvas
}

// drawBevel paints a frame of the given width just outside opening.
func drawBevel(canvas *image.RGBA, opening image.Rectangle, width int, bevel color.RGBA) {
	outer := opening.Inset(-width).Intersect(canvas.Bounds())
	fill := image.NewUniform(bevel)
	for _, strip := range []image.Rectangle{
		image.Rect(outer.Min.X, outer.Min.Y, outer.Max.X, opening.Min.Y),
		image.Rect(outer.Min.X, opening.Max.Y, outer.Max.X, outer.Max.Y),
		image.Rect(outer.Min.X, opening.Min.Y, opening.Min.X, opening.Max.Y),
		image.Rect(opening.Max.X, opening.Min.Y, outer.Max.X, opening.Max.Y),
	} {
		std_draw.Draw(canvas, strip, fill, image.Point{}, std_draw.Src)
	}
}
