package optimize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

// processCollagePair fuses two portrait files into one landscape collage,
// publishes it, and removes both sources.
func (p *catalogPass) processCollagePair(ctx context.Context, f1, f2 string) (string, error) {
	cfg := p.cfg
	p1 := filepath.Join(p.artworkDir, f1)
	p2 := filepath.Join(p.artworkDir, f2)

	if err := validateOutputDimensions(cfg.MaxWidth, cfg.MaxHeight, cfg.MaxOutputPixels); err != nil {
		return "", err
	}
	inputs, err := inspectCollageInputs(ctx, p1, p2)
	if err != nil {
		return "", err
	}
	if err := validateWorkingSet(inputs, cfg); err != nil {
		return "", err
	}

	img1, err := loadAndRotateImageWithConfig(ctx, p1, cfg, p.logger)
	if err != nil {
		return "", fmt.Errorf("load/rotate %s: %w", f1, err)
	}
	img2, err := loadAndRotateImageWithConfig(ctx, p2, cfg, p.logger)
	if err != nil {
		return "", fmt.Errorf("load/rotate %s: %w", f2, err)
	}

	leftWidth := cfg.MaxWidth / 2
	collage := renderCollage(img1, img2, collageRenderRequest{
		ctx: ctx, cfg: cfg, logger: p.logger,
		leftSharpen: scaleAwareSharpenAmount(
			cfg.SharpenAmount, img1.Bounds().Dx(), img1.Bounds().Dy(), leftWidth, cfg.MaxHeight,
		),
		rightSharpen: scaleAwareSharpenAmount(
			cfg.SharpenAmount, img2.Bounds().Dx(), img2.Bounds().Dy(), cfg.MaxWidth-leftWidth, cfg.MaxHeight,
		),
	})
	if cfg.MuseumModeEnabled {
		collage = applyMuseumMode(collage, cfg.MuseumModeIntensity)
	}

	ext := collageOutputExtension(f1, f2)
	out, err := os.CreateTemp(p.artworkDir, ".collage-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create collage output: %w", err)
	}
	defer func() { _ = os.Remove(out.Name()) }()
	if err := writeValidatedOutput(ctx, out, ext, collage, cfg); err != nil {
		return "", fmt.Errorf("write collage: %w", err)
	}
	collageName, err := moveToContentName(ctx, out.Name(), contentNameRequest{
		directory: p.artworkDir, label: "collage-" + f1 + "-" + f2, extension: ext,
	})
	if err != nil {
		return "", fmt.Errorf("commit collage: %w", err)
	}

	// The durable collage is published before either input is removed, so a
	// crash or deletion failure always leaves at least one complete artwork.
	if err := durablefs.Remove(ctx, p1); err != nil {
		return collageName, fmt.Errorf("remove first collage source: %w", err)
	}
	if err := durablefs.Remove(ctx, p2); err != nil {
		return collageName, fmt.Errorf("remove second collage source: %w", err)
	}

	p.renames = append(p.renames, Rename{OldName: f1, NewName: collageName}, Rename{OldName: f2, NewName: collageName})
	return collageName, nil
}

func inspectCollageInputs(ctx context.Context, paths ...string) (workingInput, error) {
	var inputs workingInput
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return workingInput{}, err
		}
		file, err := os.Open(path)
		if err != nil {
			return workingInput{}, fmt.Errorf("open collage input %s: %w", filepath.Base(path), err)
		}
		config, decodeErr := decodeInputConfig(ctx, file)
		closeErr := file.Close()
		if err := errors.Join(decodeErr, closeErr); err != nil {
			return workingInput{}, fmt.Errorf("inspect collage input %s: %w", filepath.Base(path), err)
		}
		inputs = inputs.plus(workingInputFor(config))
	}
	return inputs, nil
}

func collageOutputExtension(first, second string) string {
	if strings.EqualFold(filepath.Ext(first), extPNG) || strings.EqualFold(filepath.Ext(second), extPNG) {
		return extPNG
	}
	return extJPG
}

// collectRawPortraits returns, sorted, the un-derived portrait files eligible
// for collage pairing. Only PORTRAIT_MODE=collage pairs portraits.
func (p *catalogPass) collectRawPortraits(ctx context.Context, localFiles map[string]struct{}) []string {
	if p.cfg.PortraitMode != portraitModeCollage {
		return nil
	}
	var rawPortraits []string
	for filename := range localFiles {
		if p.inputs[filename].Derivative != "" {
			continue
		}
		if isPortrait, err := isPortraitFile(ctx, filepath.Join(p.artworkDir, filename)); err == nil && isPortrait {
			rawPortraits = append(rawPortraits, filename)
		}
	}
	// Sorting makes pairing deterministic despite random map iteration order.
	slices.Sort(rawPortraits)
	return rawPortraits
}

// processCollages pairs every eligible raw portrait into collages, replacing
// the pairs with their collage in localFiles.
func (p *catalogPass) processCollages(ctx context.Context, localFiles map[string]struct{}) error {
	rawPortraits := p.collectRawPortraits(ctx, localFiles)
	var collageErrors []error
	for i := 0; i+1 < len(rawPortraits); i += 2 {
		f1, f2 := rawPortraits[i], rawPortraits[i+1]
		p.logger.Info("pairing portrait images into collage", "file1", f1, "file2", f2)
		collageFilename, err := p.processCollagePair(ctx, f1, f2)
		if collageFilename != "" {
			delete(localFiles, f1)
			delete(localFiles, f2)
			localFiles[collageFilename] = struct{}{}
			p.optimized++
		}
		if err != nil {
			p.logger.Error("failed to create collage pair", "file1", f1, "file2", f2, "error", err)
			collageErrors = append(collageErrors, err)
		}
	}

	// If an odd number remains, leave the last raw input untouched so it can pair
	// with a future upload. Derivative metadata, not its filename, preserves that
	// eligibility across cycles.
	if len(rawPortraits)%2 == 1 {
		delete(localFiles, rawPortraits[len(rawPortraits)-1])
	}
	return errors.Join(collageErrors...)
}
