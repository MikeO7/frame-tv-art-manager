package optimize

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

// Catalog is the seam defining capabilities required from the catalog index.
type Catalog interface {
	SupportedFiles() (map[string]struct{}, error)
	NoteFileRename(oldName, newName string)
}

type optContext struct {
	artworkDir string
	localFiles map[string]struct{}
	cfg        Config
	onRename   func(oldName, newName string)
	catalog    Catalog
	logger     *slog.Logger
	mu         sync.Mutex
}

func (o *optContext) recordDelete(filename string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.localFiles, filename)
}

func (o *optContext) recordRename(oldName, newName string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.localFiles, oldName)
	if newName != "" {
		o.localFiles[newName] = struct{}{}
	}
}

// OptimizeCatalog performs parallel image resizing/validation across the catalog files.
//
// Parameters:
//   - ctx:        Context for cancellation.
//   - artworkDir: Directory containing the artwork files.
//   - catalog:    The artwork catalog index that provides supported files and tracks renames.
//   - cfg:        Optimization configuration defining max dimensions, quality, and active modes.
//   - onRename:   A callback function invoked when a file is renamed during optimization.
//   - logger:     Structured logger for emitting processing events and errors.
//
// Returns:
//   - int:   The number of successfully optimized and updated image files.
//   - error: Any critical failure encountered during processing.
//
// Example:
//
//	count, err := optimize.OptimizeCatalog(ctx, "/data/artwork", catalog, cfg, func(old, new string) {
//	    fmt.Printf("Renamed %s to %s\n", old, new)
//	}, logger)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("Successfully optimized %d images\n", count)
//
//nolint:gocognit,revive,funlen,gocyclo // complexity, length, and argument count justified for parallel task processing
func OptimizeCatalog(
	ctx context.Context,
	artworkDir string,
	catalog Catalog,
	cfg Config,
	onRename func(oldName, newName string),
	logger *slog.Logger,
) (int, error) {
	localFiles, err := catalog.SupportedFiles()
	if err != nil {
		return 0, err
	}

	var optimizedCount int64

	// Collage pairing runs in two cases:
	//   1. Always for uploaded files (prefixed "upload"): iPhone/web uploads are
	//      personal photos and benefit from side-by-side collage layout.
	//   2. For all portrait files when PORTRAIT_MODE=collage is explicitly set.
	//
	// Remote source images (Unsplash, NASA, etc.) default to crop mode and are
	// excluded from auto-collage unless PORTRAIT_MODE=collage is set.
	processCollages(artworkDir, localFiles, cfg, catalog, onRename, logger, &optimizedCount)

	type job struct {
		filename string
	}
	jobs := make(chan job, len(localFiles))
	for filename := range localFiles {
		if strings.HasPrefix(filename, "._") {
			delete(localFiles, filename)
			continue
		}
		jobs <- job{filename: filename}
	}
	close(jobs)

	numWorkers := runtime.NumCPU()
	if numWorkers < 4 {
		numWorkers = 4
	}
	if numWorkers > 16 {
		numWorkers = 16
	}

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	o := &optContext{
		artworkDir: artworkDir,
		localFiles: localFiles,
		cfg:        cfg,
		onRename:   onRename,
		catalog:    catalog,
		logger:     logger,
	}

	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}

				wasModified, ok := handleSingleOptimization(j.filename, o)
				if ok && wasModified {
					atomic.AddInt64(&optimizedCount, 1)
				}
			}
		}()
	}

	wg.Wait()

	if err := ctx.Err(); err != nil {
		return int(optimizedCount), err
	}

	return int(optimizedCount), nil
}

func handleSingleOptimization(filename string, o *optContext) (bool, bool) {
	path := filepath.Join(o.artworkDir, filename)

	if !o.cfg.Enabled {
		if err := ValidateImage(path); err != nil {
			o.logger.Warn("skipping corrupt image", "file", filename, "error", err)
			o.recordDelete(filename)
			return false, false
		}
		return false, true
	}

	newFilename, modified, err := OptimizeFile(path, o.cfg, o.logger)
	if err != nil {
		o.logger.Warn("skipping bad or unsupported image", "file", filename, "error", err)
		o.recordDelete(filename)
		return false, false
	}

	if modified && newFilename != filename {
		if o.onRename != nil {
			o.onRename(filename, newFilename)
		}
		o.catalog.NoteFileRename(filename, newFilename)
		o.recordRename(filename, newFilename)
	}

	return modified, true
}

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

//nolint:funlen,goconst // functional length is necessary for image processing pipeline; constant is local
func processCollagePair(
	artworkDir string,
	f1, f2 string,
	cfg Config,
	catalog Catalog,
	onRename func(oldName, newName string),
	logger *slog.Logger,
) (string, error) {
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
	}
	collage = dither(collage)

	stem1, hash1, ext1 := artwork.ExtractStemAndHash(f1)
	stem2, hash2, _ := artwork.ExtractStemAndHash(f2)

	ext := strings.ToLower(ext1)
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		ext = ".jpg"
	}

	combinedStem := "collage_" + stem1 + "_" + stem2
	combinedHash := hash1 + "_" + hash2
	collageName := artwork.BuildOptimizedName(combinedStem, cfg.MaxWidth, cfg.MaxHeight, combinedHash, ext)
	collagePath := filepath.Join(artworkDir, collageName)

	// 0o644 is intentional — artwork files must be world-readable so they
	// can be accessed over SMB/NFS network shares. Do NOT tighten to 0o600.
	out, err := os.OpenFile(collagePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("create collage output: %w", err)
	}
	defer out.Close()

	err = jpeg.Encode(out, collage, &jpeg.Options{Quality: cfg.OptimizeJPEGQuality})
	if err != nil {
		return "", fmt.Errorf("encode collage: %w", err)
	}
	_ = out.Close()
	// Explicit chmod to 0o644 is required to override restrictive system umasks (e.g. 0077)
	// so files are readable over SMB/NFS network shares. Do NOT tighten to 0o600.
	_ = os.Chmod(collagePath, 0o644)

	// Delete source raw files
	_ = os.Remove(p1)
	_ = os.Remove(p2)

	// Notify catalog and caller
	if onRename != nil {
		onRename(f1, collageName)
		onRename(f2, "")
	}
	catalog.NoteFileRename(f1, collageName)
	catalog.NoteFileRename(f2, "")

	return collageName, nil
}

func processCollages(
	artworkDir string,
	localFiles map[string]struct{},
	cfg Config,
	catalog Catalog,
	onRename func(oldName, newName string),
	logger *slog.Logger,
	optimizedCount *int64,
) {
	wantsCollageAll := cfg.PortraitMode == "collage"
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
		if !strings.Contains(filename, "_opt.h_") {
			path := filepath.Join(artworkDir, filename)
			if isPortrait, err := isPortraitFile(path); err == nil && isPortrait {
				rawPortraits = append(rawPortraits, filename)
			}
		}
	}

	for i := 0; i < len(rawPortraits)-1; i += 2 {
		f1 := rawPortraits[i]
		f2 := rawPortraits[i+1]

		logger.Info("pairing portrait images into collage", "file1", f1, "file2", f2)
		collageFilename, err := processCollagePair(artworkDir, f1, f2, cfg, catalog, onRename, logger)
		if err != nil {
			logger.Error("failed to create collage pair", "file1", f1, "file2", f2, "error", err)
			continue
		}

		// Update localFiles map
		delete(localFiles, f1)
		delete(localFiles, f2)
		localFiles[collageFilename] = struct{}{}
		atomic.AddInt64(optimizedCount, 1)
	}
}
