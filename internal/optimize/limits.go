package optimize

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"

	"github.com/MikeO7/frame-tv-art-manager/internal/jpegenc"
)

const (
	maxInputPixels         = int64(40_000_000)
	defaultMaxOutputPixels = int64(12_000_000)
)

func decodeInputConfig(ctx context.Context, reader io.Reader) (image.Config, error) {
	config, _, err := image.DecodeConfig(&contextReader{ctx: ctx, reader: reader})
	if err != nil {
		return image.Config{}, fmt.Errorf("decode image config: %w", err)
	}
	if err := validateInputDimensions(config.Width, config.Height); err != nil {
		return image.Config{}, err
	}
	return config, nil
}

func validateOutputDimensions(width, height int, maxPixels int64) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid target dimensions %dx%d", width, height)
	}
	if maxPixels <= 0 {
		maxPixels = defaultMaxOutputPixels
	}
	if pixels := int64(width) * int64(height); pixels > maxPixels {
		return fmt.Errorf("target dimensions %dx%d exceed %d-pixel output limit", width, height, maxPixels)
	}
	return nil
}

// workingInput describes the decoded sources one transform holds.
type workingInput struct {
	pixels      int64
	sourceBytes int64
	// extent is the summed width and height of the sources: the most source
	// pixels a resize can span along its two axes.
	extent int64
}

// Decoded sample sizes at the two ends of what the standard decoders produce.
const (
	smallestDecodedBytesPerPixel = 1
	largestDecodedBytesPerPixel  = 8
)

// workingInputFor accounts for the larger of two full-size phases: the
// decoder's image alongside its RGBA working copy, or the working copy
// alongside its oriented copy.
func workingInputFor(config image.Config) workingInput {
	return workingInputOfSize(config.Width, config.Height, decodedBytesPerPixel(config.ColorModel))
}

func workingInputOfSize(width, height int, decodedBytes int64) workingInput {
	pixels := int64(width) * int64(height)
	return workingInput{
		pixels: pixels, sourceBytes: pixels * max(8, 4+decodedBytes), extent: int64(width) + int64(height),
	}
}

func (input workingInput) plus(other workingInput) workingInput {
	return workingInput{
		pixels: input.pixels + other.pixels, sourceBytes: input.sourceBytes + other.sourceBytes,
		extent: input.extent + other.extent,
	}
}

// decodedBytesPerPixel is the standard decoders' in-memory sample size for a
// color model. Chroma-subsampled YCbCr is charged as unsubsampled with alpha,
// and unrecognized models as 16-bit RGBA.
func decodedBytesPerPixel(model color.Model) int64 {
	if _, paletted := model.(color.Palette); paletted {
		return 1
	}
	switch model {
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.Gray16Model, color.Alpha16Model:
		return 2
	case color.RGBAModel, color.NRGBAModel, color.CMYKModel, color.YCbCrModel, color.NYCbCrAModel:
		return 4
	default:
		return 8
	}
}

// errWorkingSetLimit marks an image, or pair of images, too large to
// transform within the configured memory budget.
var errWorkingSetLimit = errors.New("image working set exceeds the memory limit")

// workingByteLimit is the configured working-set budget, or its default.
func workingByteLimit(cfg Config) int64 {
	if cfg.MaxWorkingBytes <= 0 {
		return 512 * 1024 * 1024
	}
	return cfg.MaxWorkingBytes
}

func validateWorkingSet(input workingInput, cfg Config) error {
	estimatedBytes := estimatedWorkingBytes(input, cfg)
	maxBytes := workingByteLimit(cfg)
	if estimatedBytes > maxBytes {
		return fmt.Errorf(
			"estimated image working set %d MiB exceeds %d MiB limit: %w",
			(estimatedBytes+(1024*1024-1))/(1024*1024), maxBytes/(1024*1024), errWorkingSetLimit,
		)
	}
	return nil
}

// estimatedWorkingBytes bounds the transform's peak heap: the source phase,
// two target-sized RGBA buffers (crop or pad output and sharpen or canvas
// copy), a third for the larger of the JPEG encoder's kept coefficients and
// the decoded output during validation, the resampler's tap tables and
// scratch, optional smart-crop analysis, and a quarter for codec and
// allocator overhead.
func estimatedWorkingBytes(input workingInput, cfg Config) int64 {
	targetPixels := int64(cfg.MaxWidth) * int64(cfg.MaxHeight)
	// The encoder's coefficients are released before the output is decoded
	// for validation, so the two share the third buffer's place and only the
	// larger is charged.
	outputBytes := max(targetPixels*4, jpegenc.WorkingBytes(cfg.MaxWidth, cfg.MaxHeight, cfg.JPEGSubsampling))
	estimatedBytes := input.sourceBytes + targetPixels*8 + outputBytes + resampleTableBytes(input.extent, cfg)
	if cfg.LinearLightResize {
		estimatedBytes += int64(defaultPixelWorkers()) * linearResampleWorkerBytes(input.pixels, cfg)
	} else {
		// golang.org/x/image/draw holds a [4]float64 per destination column and
		// source row; for a target-aspect crop that is sqrt(input*target) cells.
		estimatedBytes += 32 * ceilSqrt(float64(input.pixels)*float64(targetPixels))
	}
	if cfg.SmartCropEnabled {
		estimatedBytes += smartCropAnalysisBytes(input.pixels, cfg)
	}
	return estimatedBytes * 5 / 4
}

// resampleTableBytes bounds the tap tables one resize builds for its two
// axes. An output index reads at most 4*max(shrink, 1)+2 source pixels, so an
// axis holds at most four taps per source pixel plus six per output pixel,
// however the source is shaped: a very long, thin source is all table. A tap
// is a float32 in the linear-light resampler and a 16-byte coordinate and
// weight in golang.org/x/image/draw; both keep up to 24 bytes of bookkeeping
// per output index.
func resampleTableBytes(sourceExtent int64, cfg Config) int64 {
	targetExtent := int64(cfg.MaxWidth) + int64(cfg.MaxHeight)
	tapBytes := int64(16)
	if cfg.LinearLightResize {
		tapBytes = 4
	}
	return (4*sourceExtent+6*targetExtent)*tapBytes + 24*targetExtent
}

// linearResampleWorkerBytes bounds one resampler worker: a band of
// intermediate rows within resampleBandBudget, or one output row's taps when
// a single row is larger, plus its accumulator row. One output row reads at
// most 4*shrink+3 rows, and width*shrink never exceeds the crop width, which
// is at most sqrt(input pixels * aspect).
func linearResampleWorkerBytes(inputPixels int64, cfg Config) int64 {
	width := int64(cfg.MaxWidth)
	rowBytes := width * resampleChannels * 4
	cropWidth := ceilSqrt(float64(inputPixels) * max(targetAspect(cfg), 1))
	singleRow := (4*cropWidth + 3*width) * resampleChannels * 4
	return max(resampleBandBudget, singleRow) + rowBytes
}

// smartCropAnalysisBytes covers the 256-pixel analysis scale's kernel
// temporary, which spans the full source extent of the cropped axis, plus the
// fixed-size saliency maps.
func smartCropAnalysisBytes(inputPixels int64, cfg Config) int64 {
	aspect := targetAspect(cfg)
	sourceExtent := ceilSqrt(float64(inputPixels) * max(aspect, 1/aspect))
	return 32*256*sourceExtent + 16<<20
}

func targetAspect(cfg Config) float64 {
	return float64(cfg.MaxWidth) / float64(cfg.MaxHeight)
}

func ceilSqrt(value float64) int64 {
	return int64(math.Ceil(math.Sqrt(value)))
}

func validateInputDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid image dimensions %dx%d", width, height)
	}
	if pixels := int64(width) * int64(height); pixels > maxInputPixels {
		return fmt.Errorf("image dimensions %dx%d exceed %d-pixel limit", width, height, maxInputPixels)
	}
	return nil
}
