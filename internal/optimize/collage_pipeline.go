package optimize

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

func isPortraitFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	imgCfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return false, err
	}

	if _, err := f.Seek(0, 0); err != nil {
		return false, err
	}

	orientation, _ := ReadOrientation(f)
	w, h := imgCfg.Width, imgCfg.Height
	if orientation >= 5 && orientation <= 8 {
		w, h = h, w
	}
	return h > w, nil
}

func loadAndRotateImage(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	orientation, _ := ReadOrientation(f)
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}

	rotated := RotateImage(img, orientation)
	return toRGBA(rotated), nil
}

// collageJob bundles the inputs needed to fuse a single pair of portrait
// images into one landscape collage.
type collageJob struct {
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

	logger.Debug("starting processCollagePair", "f1", f1, "f2", f2)
	p1 := filepath.Join(artworkDir, f1)
	p2 := filepath.Join(artworkDir, f2)

	img1, err := loadAndRotateImage(p1)
	if err != nil {
		return "", fmt.Errorf("load/rotate %s: %w", f1, err)
	}
	img2, err := loadAndRotateImage(p2)
	if err != nil {
		return "", fmt.Errorf("load/rotate %s: %w", f2, err)
	}

	collage := CreateCollage(img1, img2, cfg.SmartCropEnabled)
	collage = sharpen(collage)
	if cfg.MuseumModeEnabled {
		collage = applyMuseumMode(collage, cfg.MuseumModeIntensity)
	} else {
		collage = dither(collage)
	}

	stem1, hash1, ext1 := artwork.ExtractStemAndHash(f1)
	stem2, hash2, _ := artwork.ExtractStemAndHash(f2)

	ext := strings.ToLower(ext1)
	if ext != extJPG && ext != extJPEG && ext != extPNG {
		ext = extJPG
	}

	combinedStem := "collage_" + stem1 + "_" + stem2
	combinedHash := hash1 + "_" + hash2
	collageName := artwork.BuildOptimizedName(combinedStem, cfg.MaxWidth, cfg.MaxHeight, combinedHash, ext)
	collagePath := filepath.Join(artworkDir, collageName)

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
	if err := os.Rename(tmpPath, collagePath); err != nil {
		return "", fmt.Errorf("commit collage: %w", err)
	}

	// Delete source raw files
	_ = os.Remove(p1)
	_ = os.Remove(p2)

	// Update the local catalog after the on-disk commit. Observer failures are
	// reported without pretending the already-committed collection change did
	// not happen.
	catalog.NoteFileRename(f1, collageName)
	catalog.NoteFileRename(f2, "")

	if onRename != nil {
		if err := errors.Join(onRename(f1, collageName), onRename(f2, "")); err != nil {
			return collageName, fmt.Errorf("observe collage source renames: %w", err)
		}
	}

	return collageName, nil
}

// collectRawPortraits returns the un-optimized portrait files eligible for
// collage pairing, pruning AppleDouble ("._") entries from localFiles as it goes.
func collectRawPortraits(artworkDir string, localFiles map[string]struct{}, wantsCollageAll bool) []string {
	var rawPortraits []string
	for filename := range localFiles {
		if strings.HasPrefix(filename, "._") {
			delete(localFiles, filename)
			continue
		}
		isUpload := strings.HasPrefix(filename, "upload")
		if !wantsCollageAll && !isUpload {
			continue
		}
		if strings.Contains(filename, optimizedMarker) {
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
	artworkDir     string
	localFiles     map[string]struct{}
	cfg            Config
	catalog        Catalog
	onRename       RenameObserver
	logger         *slog.Logger
	optimizedCount *int64
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

	wantsCollageAll := cfg.PortraitMode == "collage"
	rawPortraits := collectRawPortraits(artworkDir, localFiles, wantsCollageAll)

	// Sort for deterministic pairing: map iteration order is random, so without
	// this the same set of uploads could pair differently across runs.
	sort.Strings(rawPortraits)

	for i := 0; i < len(rawPortraits)-1; i += 2 {
		f1 := rawPortraits[i]
		f2 := rawPortraits[i+1]

		logger.Info("pairing portrait images into collage", "file1", f1, "file2", f2)
		collageFilename, err := processCollagePair(collageJob{
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

	// If an odd number of raw portraits remains, the last one is unpaired.
	// Exclude it from this cycle's optimization so it stays raw on disk; once
	// optimized it would gain the "_opt.h_" marker and be permanently skipped by
	// the raw-portrait scan above, so it could never pair with a future upload.
	// Leaving it raw lets it wait for a partner on a later cycle. The file is
	// untouched on disk and is rediscovered when the catalog is rescanned.
	if len(rawPortraits)%2 == 1 {
		delete(localFiles, rawPortraits[len(rawPortraits)-1])
	}
	return errors.Join(observerErrors...)
}
