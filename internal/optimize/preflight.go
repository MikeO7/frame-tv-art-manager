package optimize

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// RequiresStage reports whether a verified collection snapshot can produce a
// transformation or collage effect. Durable manifest metadata decides whether
// a derivative is current; filenames never authorize a skip.
//
// An uncertain file observation conservatively requires the full StageCatalog
// path, which owns complete digest and pixel verification. Cancellation is
// returned immediately instead of being converted into work.
func RequiresStage(ctx context.Context, request StageRequest) (bool, error) {
	if err := validateStageRequest(ctx, request.Inputs); err != nil {
		return false, err
	}
	inputs := slices.SortedFunc(slices.Values(request.Inputs), func(left, right StageInput) int {
		return strings.Compare(left.Name, right.Name)
	})

	rawPortraits, uncertain, err := preflightRawPortraits(ctx, inputs, request.Config)
	if err != nil {
		return false, err
	}
	if uncertain || len(rawPortraits) >= 2 {
		return true, nil
	}
	if !request.Config.Enabled {
		return false, nil
	}
	var unpairedPortrait string
	if len(rawPortraits) == 1 {
		unpairedPortrait = rawPortraits[0]
	}
	logger := request.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	transformKey := TransformKey(request.Config)
	for _, input := range inputs {
		if input.Name == unpairedPortrait || !isOptimizableImage(strings.ToLower(filepath.Ext(input.Name)), request.Config) {
			continue
		}
		required, err := inputRequiresStage(ctx, input, request.Config, transformKey, logger)
		if err != nil || required {
			return required, err
		}
	}
	return false, nil
}

func preflightRawPortraits(ctx context.Context, inputs []StageInput, cfg Config) ([]string, bool, error) {
	rawPortraits := make([]string, 0)
	for _, input := range inputs {
		if !isRawCollageCandidate(input, cfg) {
			continue
		}
		// A portrait too large to transform is never paired by the stage.
		skipped, err := transformSkipsForSize(ctx, input, cfg)
		if err != nil {
			return nil, false, err
		}
		if skipped {
			continue
		}
		portrait, err := preflightPortrait(ctx, input)
		if err != nil {
			if isCancellation(err) {
				return nil, false, err
			}
			return nil, true, nil
		}
		if portrait {
			rawPortraits = append(rawPortraits, input.Name)
		}
	}
	return rawPortraits, false, nil
}

func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// inputRequiresStage reports whether one optimizable input can change. A raw
// input the transform would only skip cannot change, so it never requires the
// stage: otherwise every sync would copy the whole collection to skip it
// again. Unreadable orientation or color metadata conservatively requires the
// stage, which owns the verdict on the file; only cancellation and the
// deliberate reject-embedded color policy are errors.
func inputRequiresStage(ctx context.Context, input StageInput, cfg Config, transformKey string, logger *slog.Logger) (bool, error) {
	if input.Derivative != "" {
		return input.TransformKey != transformKey || input.Width != cfg.MaxWidth || input.Height != cfg.MaxHeight, nil
	}
	skipped, err := transformSkipsForSize(ctx, input, cfg)
	if err != nil || skipped {
		if skipped {
			logger.Debug("artwork is too large to transform within the limits; leaving it as it is",
				"file", input.Name, "width", input.Width, "height", input.Height)
		}
		return false, err
	}
	if input.Width != cfg.MaxWidth || input.Height != cfg.MaxHeight || cfg.MuseumModeEnabled {
		return true, nil
	}
	colorMetadata, err := readPreflightMetadata(ctx, input, func(file *os.File, extension string) (embeddedColorData, error) {
		return readEmbeddedColorData(ctx, file, extension)
	})
	if err == nil {
		err = checkColorPolicy(colorMetadata, cfg, logger, input.Name)
	}
	if err != nil {
		if isCancellation(err) || errors.Is(err, errEmbeddedColorRejected) {
			return false, fmt.Errorf("preflight color metadata for %q: %w", input.Name, err)
		}
		return true, nil
	}
	if (len(colorMetadata.icc) > 0 && cfg.ColorProfilePolicy == profileConvertSRGB) ||
		(colorMetadata.hdr != nil && cfg.HDRToneMap) {
		return true, nil
	}
	orientation, err := preflightOrientation(ctx, input)
	if err != nil {
		if isCancellation(err) {
			return false, err
		}
		return true, nil
	}
	return orientation != 1, nil
}

// transformSkipsForSize reports whether the transform would skip a raw input
// for its size alone: more pixels than the decoder accepts, or an estimated
// working set over the configured limit. It applies validateWorkingSet to the
// recorded dimensions exactly as the stage applies it to the decoded header.
// The estimate depends on the sample format only for sources deeper than
// eight bits, so the dimensions settle every size but a narrow band, and only
// there is the header read. A header that cannot be read is left for the
// stage to judge.
func transformSkipsForSize(ctx context.Context, input StageInput, cfg Config) (bool, error) {
	if input.Width <= 0 || input.Height <= 0 {
		return false, nil
	}
	overLimit := func(working workingInput) bool {
		return errors.Is(validateWorkingSet(working, cfg), errWorkingSetLimit)
	}
	if int64(input.Width)*int64(input.Height) > maxInputPixels ||
		overLimit(workingInputOfSize(input.Width, input.Height, smallestDecodedBytesPerPixel)) {
		return true, nil
	}
	if !overLimit(workingInputOfSize(input.Width, input.Height, largestDecodedBytesPerPixel)) {
		return false, nil
	}
	header, err := readPreflightMetadata(ctx, input, func(file *os.File, _ string) (image.Config, error) {
		return decodeInputConfig(ctx, file)
	})
	if err != nil {
		if isCancellation(err) {
			return false, err
		}
		return false, nil
	}
	return overLimit(workingInputFor(header)), nil
}

func isRawCollageCandidate(input StageInput, cfg Config) bool {
	return input.Derivative == "" && cfg.PortraitMode == portraitModeCollage
}

func preflightOrientation(ctx context.Context, input StageInput) (int, error) {
	return readPreflightMetadata(ctx, input, func(file *os.File, extension string) (int, error) {
		return readOrientationForExtension(ctx, file, extension)
	})
}

func preflightPortrait(ctx context.Context, input StageInput) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateInputDimensions(input.Width, input.Height); err != nil {
		return false, fmt.Errorf("classify collage candidate %q: %w", input.Name, err)
	}
	w, h := input.Width, input.Height
	extension := strings.ToLower(filepath.Ext(input.Name))
	if !isOptimizableJPEG(extension) && extension != extPNG {
		return h > w, nil
	}
	orientation, err := preflightOrientation(ctx, input)
	if err != nil {
		return false, fmt.Errorf("read collage metadata for %q: %w", input.Name, err)
	}
	if orientation >= 5 && orientation <= 8 {
		w, h = h, w
	}
	return h > w, nil
}

// readPreflightMetadata reads metadata from a regular, non-symlink input and
// fails when its path stops naming the opened file while reading.
func readPreflightMetadata[T any](ctx context.Context, input StageInput, read func(*os.File, string) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	before, err := os.Lstat(input.Path)
	if err != nil {
		return zero, fmt.Errorf("inspect artwork %q: %w", input.Name, err)
	}
	if !before.Mode().IsRegular() {
		return zero, fmt.Errorf("artwork %q is not a regular non-symlink file", input.Name)
	}
	file, err := os.Open(input.Path)
	if err != nil {
		return zero, fmt.Errorf("open artwork %q: %w", input.Name, err)
	}
	value, readErr := read(file, strings.ToLower(filepath.Ext(input.Name)))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	after, pathErr := os.Lstat(input.Path)
	if err := errors.Join(readErr, statErr, closeErr, pathErr); err != nil {
		return zero, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return zero, fmt.Errorf("artwork %q changed while reading metadata", input.Name)
	}
	return value, nil
}
