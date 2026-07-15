package optimize

import (
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/MikeO7/frame-tv-art-manager/internal/durablefs"
)

// collageJob bundles the inputs needed to fuse a single pair of portrait
// images into one landscape collage.
type collageJob struct {
	ctx        context.Context
	artworkDir string
	f1, f2     string
	cfg        Config
	catalog    Catalog
	onRename   RenameObserver
	logger     *slog.Logger
}

//nolint:funlen,gocognit,gocyclo // the ordered transactional image pipeline keeps cleanup local
func processCollagePair(job collageJob) (string, error) {
	artworkDir := job.artworkDir
	f1, f2 := job.f1, job.f2
	cfg := job.cfg
	catalog := job.catalog
	onRename := job.onRename
	logger := job.logger
	ctx := job.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	logger.Debug("starting processCollagePair", "f1", f1, "f2", f2)
	p1 := filepath.Join(artworkDir, f1)
	p2 := filepath.Join(artworkDir, f2)

	img1, err := loadAndRotateImageWithPolicy(p1, cfg.ColorProfilePolicy, logger)
	if err != nil {
		return "", fmt.Errorf("load/rotate %s: %w", f1, err)
	}
	img2, err := loadAndRotateImageWithPolicy(p2, cfg.ColorProfilePolicy, logger)
	if err != nil {
		return "", fmt.Errorf("load/rotate %s: %w", f2, err)
	}

	if err := validateOutputDimensions(cfg.MaxWidth, cfg.MaxHeight, cfg.MaxOutputPixels); err != nil {
		return "", err
	}
	inputPixels := int64(img1.Bounds().Dx())*int64(img1.Bounds().Dy()) +
		int64(img2.Bounds().Dx())*int64(img2.Bounds().Dy())
	if err := validateWorkingPixels(inputPixels, cfg); err != nil {
		return "", err
	}
	collage := createCollageForTarget(
		img1, img2, cfg.MaxWidth, cfg.MaxHeight, cfg.SmartCropEnabled, cfg.SmartCropMinGain, cfg.LinearLightResize,
	)
	collage = sharpenWithOptions(collage, cfg.SharpenAmount, cfg.SharpenThreshold, defaultPixelWorkers())
	if cfg.MuseumModeEnabled {
		collage = applyMuseumMode(collage, cfg.MuseumModeIntensity)
	} else if cfg.DitherEnabled {
		collage = dither(collage)
	}

	ext := strings.ToLower(filepath.Ext(f1))
	if ext != extJPG && ext != extJPEG && ext != extPNG {
		ext = extJPG
	}

	// 0o644 is intentional — artwork files must be world-readable so they
	// can be accessed over SMB/NFS network shares. Do NOT tighten to 0o600.
	out, err := os.CreateTemp(artworkDir, ".collage-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create collage output: %w", err)
	}
	tmpPath := out.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if ext == extPNG {
		err = png.Encode(out, collage)
	} else {
		err = jpeg.Encode(out, collage, &jpeg.Options{Quality: cfg.OptimizeJPEGQuality})
	}
	if err != nil {
		_ = out.Close()
		return "", fmt.Errorf("encode collage: %w", err)
	}
	if err := out.Chmod(0o644); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("chmod collage: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("sync collage: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close collage: %w", err)
	}
	if err := ValidateImage(tmpPath); err != nil {
		return "", fmt.Errorf("validate collage: %w", err)
	}
	digest, err := fileDigest(tmpPath)
	if err != nil {
		return "", fmt.Errorf("hash collage: %w", err)
	}
	collageName, err := availableContentName(artworkDir, "", "collage-"+f1+"-"+f2, digest, ext)
	if err != nil {
		return "", fmt.Errorf("choose collage output name: %w", err)
	}
	collagePath := filepath.Join(artworkDir, collageName)
	if err := durablefs.MoveExclusive(ctx, tmpPath, collagePath); err != nil {
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

	// Update the local catalog after the on-disk commit. Observer failures are
	// reported without pretending the already-committed collection change did
	// not happen.
	catalog.NoteFileRename(f1, collageName)
	catalog.NoteFileRename(f2, "")

	if onRename != nil {
		if err := errors.Join(onRename(f1, collageName), onRename(f2, collageName)); err != nil {
			return collageName, fmt.Errorf("observe collage source renames: %w", err)
		}
	}

	return collageName, nil
}

// collectRawPortraits returns the un-optimized portrait files eligible for
// collage pairing, pruning AppleDouble ("._") entries from localFiles as it goes.
func collectRawPortraits(
	artworkDir string,
	localFiles map[string]struct{},
	wantsCollageAll bool,
	inputMaps ...map[string]StageInput,
) []string {
	var inputs map[string]StageInput
	if len(inputMaps) > 0 {
		inputs = inputMaps[0]
	}
	var rawPortraits []string
	for filename := range localFiles {
		if strings.HasPrefix(filename, "._") {
			delete(localFiles, filename)
			continue
		}
		input := inputs[filename]
		if !wantsCollageAll {
			continue
		}
		if input.Derivative != "" {
			continue
		}
		path := filepath.Join(artworkDir, filename)
		if isPortrait, err := isPortraitFile(path); err == nil && isPortrait {
			rawPortraits = append(rawPortraits, filename)
		}
	}
	return rawPortraits
}

// collageBatch bundles the inputs needed to pair every eligible raw portrait
// in a catalog into collages during a single optimization pass.
type collageBatch struct {
	ctx            context.Context
	artworkDir     string
	localFiles     map[string]struct{}
	cfg            Config
	catalog        Catalog
	onRename       RenameObserver
	logger         *slog.Logger
	optimizedCount *int64
	inputs         map[string]StageInput
}

func processCollages(batch collageBatch) error {
	artworkDir := batch.artworkDir
	localFiles := batch.localFiles
	cfg := batch.cfg
	catalog := batch.catalog
	onRename := batch.onRename
	logger := batch.logger
	optimizedCount := batch.optimizedCount
	var observerErrors []error

	wantsCollageAll := cfg.PortraitMode == portraitModeCollage
	rawPortraits := collectRawPortraits(artworkDir, localFiles, wantsCollageAll, batch.inputs)

	// Sort for deterministic pairing: map iteration order is random, so without
	// this the same set of uploads could pair differently across runs.
	sort.Strings(rawPortraits)

	for i := 0; i < len(rawPortraits)-1; i += 2 {
		f1 := rawPortraits[i]
		f2 := rawPortraits[i+1]

		logger.Info("pairing portrait images into collage", "file1", f1, "file2", f2)
		collageFilename, err := processCollagePair(collageJob{
			ctx:        batch.ctx,
			artworkDir: artworkDir,
			f1:         f1,
			f2:         f2,
			cfg:        cfg,
			catalog:    catalog,
			onRename:   onRename,
			logger:     logger,
		})
		if collageFilename != "" {
			delete(localFiles, f1)
			delete(localFiles, f2)
			localFiles[collageFilename] = struct{}{}
			atomic.AddInt64(optimizedCount, 1)
		}
		if err != nil {
			logger.Error("failed to create collage pair", "file1", f1, "file2", f2, "error", err)
			observerErrors = append(observerErrors, err)
			continue
		}
	}

	// If an odd number remains, leave the last raw input untouched so it can pair
	// with a future upload. Derivative metadata, not its filename, preserves that
	// eligibility across cycles.
	if len(rawPortraits)%2 == 1 {
		delete(localFiles, rawPortraits[len(rawPortraits)-1])
	}
	return errors.Join(observerErrors...)
}
