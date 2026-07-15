package optimize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	inputs := append([]StageInput(nil), request.Inputs...)
	sort.Slice(inputs, func(left, right int) bool { return inputs[left].Name < inputs[right].Name })

	rawPortraits, uncertain, err := preflightRawPortraits(ctx, inputs, request.Config)
	if err != nil {
		return false, err
	}
	if uncertain || len(rawPortraits) >= 2 {
		return true, nil
	}
	logger := request.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return requiresIndividualStage(ctx, inputs, rawPortraits, request.Config, logger)
}

func preflightRawPortraits(ctx context.Context, inputs []StageInput, cfg Config) ([]string, bool, error) {
	rawPortraits := make([]string, 0)
	for _, input := range inputs {
		if !isRawCollageCandidate(input, cfg) {
			continue
		}
		portrait, err := preflightPortrait(ctx, input)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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

//nolint:gocognit,gocyclo // conservative preflight keeps every fail-closed decision visible in one pass
func requiresIndividualStage(
	ctx context.Context,
	inputs []StageInput,
	rawPortraits []string,
	cfg Config,
	logger *slog.Logger,
) (bool, error) {
	var unpairedPortrait string
	if len(rawPortraits) == 1 {
		unpairedPortrait = rawPortraits[0]
	}
	if !cfg.Enabled {
		return false, nil
	}
	transformKey := TransformKey(cfg)
	for _, input := range inputs {
		if input.Name == unpairedPortrait || !isOptimizableImage(strings.ToLower(filepath.Ext(input.Name)), cfg) {
			continue
		}
		if input.Derivative != "" {
			if input.TransformKey != transformKey || input.Width != cfg.MaxWidth || input.Height != cfg.MaxHeight {
				return true, nil
			}
			continue
		}
		if input.Width != cfg.MaxWidth || input.Height != cfg.MaxHeight || cfg.MuseumModeEnabled {
			return true, nil
		}
		colorMetadata, err := preflightColorMetadata(ctx, input, cfg.ColorProfilePolicy)
		if err != nil {
			return false, fmt.Errorf("preflight color metadata for %q: %w", input.Name, err)
		}
		if colorMetadata.description != "" && colorMetadata.hdr == nil && cfg.ColorProfilePolicy == profileAssumeSRGB {
			logger.Warn(
				"embedded color metadata is not transformed; treating decoded samples as sRGB",
				"file", input.Name, "metadata", colorMetadata.description,
			)
		}
		if colorMetadata.description != "" && cfg.ColorProfilePolicy == profileConvertSRGB &&
			len(colorMetadata.icc) == 0 && colorMetadata.hdr == nil {
			logger.Warn(
				"embedded color metadata is unsupported for conversion; assuming sRGB",
				"file", input.Name, "metadata", colorMetadata.description,
			)
		}
		if (len(colorMetadata.icc) > 0 && cfg.ColorProfilePolicy == profileConvertSRGB) ||
			(colorMetadata.hdr != nil && cfg.HDRToneMap) {
			return true, nil
		}
		orientation, err := preflightOrientation(ctx, input)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return false, err
			}
			return true, nil
		}
		if orientation != 1 {
			return true, nil
		}
	}
	return false, nil
}

func preflightColorMetadata(ctx context.Context, input StageInput, policy string) (embeddedColorData, error) {
	if err := ctx.Err(); err != nil {
		return embeddedColorData{}, err
	}
	file, before, err := openPreflightInput(input)
	if err != nil {
		return embeddedColorData{}, err
	}
	extension := strings.ToLower(filepath.Ext(input.Name))
	metadata, readErr := readEmbeddedColorData(ctx, file, extension)
	if readErr == nil && metadata.description != "" && policy == profileRejectEmbedded {
		readErr = fmt.Errorf("unsupported embedded color metadata %s", metadata.description)
	}
	opened, statErr := file.Stat()
	closeErr := file.Close()
	after, pathErr := os.Lstat(input.Path)
	if err := errors.Join(readErr, statErr, closeErr, pathErr); err != nil {
		return embeddedColorData{}, err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return embeddedColorData{}, fmt.Errorf("artwork %q changed while reading color metadata", input.Name)
	}
	return metadata, nil
}

func isRawCollageCandidate(input StageInput, cfg Config) bool {
	if input.Derivative != "" {
		return false
	}
	return cfg.PortraitMode == portraitModeCollage
}

func preflightOrientation(ctx context.Context, input StageInput) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	file, before, err := openPreflightInput(input)
	if err != nil {
		return 0, err
	}
	extension := strings.ToLower(filepath.Ext(input.Name))
	orientation, readErr := readOrientationForExtension(ctx, file, extension)
	opened, statErr := file.Stat()
	closeErr := file.Close()
	after, pathErr := os.Lstat(input.Path)
	if err := errors.Join(readErr, statErr, closeErr, pathErr); err != nil {
		return 0, fmt.Errorf("read orientation for %q: %w", input.Name, err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return 0, fmt.Errorf("artwork %q changed while reading metadata", input.Name)
	}
	return orientation, nil
}

//nolint:gocyclo // file identity, metadata, and orientation checks form one atomic observation
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

	file, before, err := openPreflightInput(input)
	if err != nil {
		return false, err
	}
	orientation, readErr := readOrientationForExtension(ctx, file, extension)
	opened, statErr := file.Stat()
	closeErr := file.Close()
	after, pathErr := os.Lstat(input.Path)
	if err := errors.Join(readErr, statErr, closeErr, pathErr); err != nil {
		return false, fmt.Errorf("read collage metadata for %q: %w", input.Name, err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return false, fmt.Errorf("collage candidate %q changed while reading metadata", input.Name)
	}
	if orientation >= 5 && orientation <= 8 {
		w, h = h, w
	}
	return h > w, nil
}

func openPreflightInput(input StageInput) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(input.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect collage candidate %q: %w", input.Name, err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("collage candidate %q is not a regular non-symlink file", input.Name)
	}
	file, err := os.Open(input.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("open collage candidate %q: %w", input.Name, err)
	}
	return file, before, nil
}
