package optimize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RequiresStage reports whether a verified collection snapshot can produce a
// transformation or collage effect. It deliberately reads only JPEG metadata
// needed to classify raw collage candidates; all other decisions use verified
// snapshot facts and deterministic names.
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
	return requiresIndividualStage(inputs, rawPortraits, request.Config), nil
}

func preflightRawPortraits(ctx context.Context, inputs []StageInput, cfg Config) ([]string, bool, error) {
	rawPortraits := make([]string, 0)
	for _, input := range inputs {
		if !isRawCollageCandidate(input.Name, cfg) {
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

func requiresIndividualStage(inputs []StageInput, rawPortraits []string, cfg Config) bool {
	var unpairedPortrait string
	if len(rawPortraits) == 1 {
		unpairedPortrait = rawPortraits[0]
	}
	if !cfg.Enabled {
		return false
	}
	for _, input := range inputs {
		if input.Name == unpairedPortrait || !isOptimizableJPEG(strings.ToLower(filepath.Ext(input.Name))) {
			continue
		}
		if !isConfiguredOptimizedName(input.Name, cfg) {
			return true
		}
	}
	return false
}

func isRawCollageCandidate(name string, cfg Config) bool {
	if strings.Contains(name, optimizedMarker) {
		return false
	}
	return cfg.PortraitMode == portraitModeCollage || strings.HasPrefix(name, "upload")
}

func preflightPortrait(ctx context.Context, input StageInput) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateInputDimensions(input.Width, input.Height); err != nil {
		return false, fmt.Errorf("classify collage candidate %q: %w", input.Name, err)
	}
	w, h := input.Width, input.Height
	if !isOptimizableJPEG(strings.ToLower(filepath.Ext(input.Name))) {
		return h > w, nil
	}

	file, before, err := openPreflightInput(input)
	if err != nil {
		return false, err
	}
	orientation, readErr := ReadOrientation(file)
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
