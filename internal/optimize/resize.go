// Package optimize provides image resizing and quality optimization
// for Samsung Frame TV artwork. Frame TVs are 4K (3840×2160), so
// uploading larger images wastes bandwidth and transfer time.
package optimize

import (
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // Needed for decoding PNG images
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

type Config struct {
	Enabled             bool
	SmartCropEnabled    bool
	MaxWidth            int
	MaxHeight           int
	OptimizeJPEGQuality int
	NormalizeLuminance  bool
	MuseumModeEnabled   bool
	MuseumModeIntensity int
	PortraitMode        string
}

// DefaultConfig returns sensible defaults for Frame TV display.
func DefaultConfig() Config {
	return Config{
		Enabled:             true,
		SmartCropEnabled:    false,
		MaxWidth:            3840,
		MaxHeight:           2160,
		OptimizeJPEGQuality: 95,
		NormalizeLuminance:  true,
		MuseumModeEnabled:   false,
		MuseumModeIntensity: 1,
		PortraitMode:        "collage",
	}
}

// OptimizeFile checks if an image needs resizing and optimizes it
// in-place. It encapsulates the naming convention and returns the
// final path/filename and whether the file was modified.
//
// Parameters:
//   - path: The absolute or relative file path to the original image (must be a JPEG).
//   - cfg:  The configuration containing MaxWidth, MaxHeight, SmartCrop, and Quality preferences.
//   - logger: A structured logger for emitting processing stages and skipped file notifications.
//
// Returns:
//   - string: The final filename of the optimized image (e.g., "monet_opt.h_1a2b.jpg").
//   - bool:   True if the file was actively modified, false if skipped due to dimensions or config.
//   - error:  Any file I/O or decoding error encountered during the operation.
//
// Example:
//
//	finalName, modified, err := optimize.OptimizeFile("/data/artwork/monet.jpg", optimize.DefaultConfig(), logger)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	if modified {
//	    fmt.Printf("Optimized file into %s\n", finalName)
//	}
//
//nolint:funlen,goconst // image optimization pipeline; extension constant is local
func OptimizeFile(path string, cfg Config, logger *slog.Logger) (string, bool, error) {
	filename := filepath.Base(path)
	dir := filepath.Dir(path)
	if !cfg.Enabled {
		return filename, false, nil
	}

	// Only optimize JPEGs (Frame TV primary format).
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jpg" && ext != ".jpeg" {
		return filename, false, nil
	}

	if checkFastPath(filename, cfg, logger) {
		return filename, false, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return filename, false, fmt.Errorf("open image: %w", err)
	}
	defer func() { _ = f.Close() }()

	imgCfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return filename, false, fmt.Errorf("decode image config: %w", err)
	}

	width, height := imgCfg.Width, imgCfg.Height
	needsAdjustment := width != cfg.MaxWidth || height != cfg.MaxHeight

	var finalW, finalH int
	var modified bool

	if !needsAdjustment && !cfg.MuseumModeEnabled {
		finalW, finalH = width, height
		modified = false
	} else {
		finalW, finalH, err = rewriteImage(f, path, filename, width, height, needsAdjustment, cfg, logger)
		if err != nil {
			return filename, false, err
		}
		modified = true
	}

	return handleRename(path, filename, dir, modified, finalW, finalH, logger)
}

// checkFastPath returns true if the file is already optimized with matching dimensions.
func checkFastPath(filename string, cfg Config, logger *slog.Logger) bool {
	if !strings.Contains(filename, "_opt.h_") {
		return false
	}
	w, h, ok := artwork.ParseDimensions(filename)
	if ok && w <= cfg.MaxWidth && h <= cfg.MaxHeight {
		logger.Debug("skipping already optimized file", "file", filename, "dims", fmt.Sprintf("%dx%d", w, h))
		return true
	}
	return false
}

// handleRename renames the file according to optimized names if needed.
func handleRename(path, filename, dir string, modified bool, finalW, finalH int, logger *slog.Logger) (string, bool, error) {
	currentW, currentH, _ := artwork.ParseDimensions(filename)
	isOpt := strings.Contains(filename, "_opt.h_")

	if !modified && isOpt && currentW == finalW && currentH == finalH {
		return filename, false, nil
	}

	newFilename, changed := artwork.BuildOptimizedNameFromFile(filename, finalW, finalH)
	if !changed {
		return filename, modified, nil
	}

	newPath := filepath.Join(dir, newFilename)
	if err := os.Rename(path, newPath); err != nil {
		return filename, modified, fmt.Errorf("rename to optimized: %w", err)
	}

	logger.Info("updated optimized filename", "old", filename, "new", newFilename)
	return newFilename, true, nil
}

// ValidateImage performs a low-cost check to ensure an image file is not corrupt.
// It opens the file and attempts to decode its configuration header without fully loading
// the pixel data into memory.
//
// Parameters:
//   - path: The absolute or relative file path to the image to validate.
//
// Returns:
//   - error: Returns an error if the file cannot be opened or if the image header is corrupt.
//
// Example:
//
//	if err := optimize.ValidateImage("/data/artwork/download.jpg"); err != nil {
//	    log.Printf("Invalid image format: %v", err)
//	    os.Remove("/data/artwork/download.jpg")
//	}
func ValidateImage(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, _, err = image.DecodeConfig(f)
	return err
}

// rewriteImage handles the actual decoding, pixel modifications (cropping, sharpening, dithering),
// and re-encoding of the image back to disk.
func rewriteImage(f *os.File, path, filename string, width, height int, needsAdjustment bool, cfg Config, logger *slog.Logger) (int, int, error) {
	_ = needsAdjustment // recalculated after rotation

	if _, err := f.Seek(0, 0); err != nil {
		return 0, 0, fmt.Errorf("seek to start: %w", err)
	}
	orientation, _ := ReadOrientation(f)

	if _, err := f.Seek(0, 0); err != nil {
		return 0, 0, fmt.Errorf("seek to start: %w", err)
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, 0, fmt.Errorf("decode image: %w", err)
	}

	logger.Info("optimizing image", "file", filename, "original_dims", fmt.Sprintf("%dx%d", width, height))

	img = RotateImage(img, orientation)
	rgba := toRGBA(img)

	newW, newH := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	if newW != cfg.MaxWidth || newH != cfg.MaxHeight {
		if newH > newW && cfg.PortraitMode != "crop" {
			rgba = padPortrait(rgba, cfg.MaxWidth, cfg.MaxHeight)
		} else {
			rgba = centerCrop(rgba, cfg.MaxWidth, cfg.MaxHeight, cfg.SmartCropEnabled)
		}
	}

	rgba = sharpen(rgba)
	if cfg.MuseumModeEnabled {
		rgba = applyMuseumMode(rgba, cfg.MuseumModeIntensity)
	}
	rgba = dither(rgba)

	// Close original file so we can overwrite or rename it.
	_ = f.Close()

	// Save back to disk.
	// 0o644 is intentional — artwork files must be world-readable so they
	// can be accessed over SMB/NFS network shares. Do NOT tighten to 0o600.
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, 0, fmt.Errorf("create optimized file: %w", err)
	}
	defer func() { _ = out.Close() }()

	err = jpeg.Encode(out, rgba, &jpeg.Options{Quality: cfg.OptimizeJPEGQuality})
	if err != nil {
		return 0, 0, fmt.Errorf("encode jpeg: %w", err)
	}
	_ = out.Close()

	newBounds := rgba.Bounds()
	return newBounds.Dx(), newBounds.Dy(), nil
}

// ReadOrientation reads the EXIF orientation tag from an image file if available.
//
//nolint:gocognit,goconst,gocyclo // custom EXIF parser logic needs to handle raw JPEG marker bounds checks; constant is local
func ReadOrientation(r io.Reader) (int, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:2]); err != nil {
		return 1, err
	}
	if buf[0] != 0xFF || buf[1] != 0xD8 {
		return 1, fmt.Errorf("not a JPEG (SOI missing)")
	}

	for {
		if _, err := io.ReadFull(r, buf[:2]); err != nil {
			return 1, err
		}
		for buf[0] == 0xFF && buf[1] == 0xFF {
			if _, err := io.ReadFull(r, buf[1:2]); err != nil {
				return 1, err
			}
		}
		if buf[0] != 0xFF {
			return 1, fmt.Errorf("invalid marker prefix")
		}

		marker := buf[1]
		if marker == 0xD9 || marker == 0xDA { // EOI or SOS
			break
		}

		if _, err := io.ReadFull(r, buf[:2]); err != nil {
			return 1, err
		}
		length := int(buf[0])<<8 | int(buf[1])
		if length < 2 {
			return 1, fmt.Errorf("invalid marker length")
		}

		if marker == 0xE1 {
			payload := make([]byte, length-2)
			if _, err := io.ReadFull(r, payload); err != nil {
				return 1, err
			}
			if len(payload) >= 6 && string(payload[:6]) == "Exif\x00\x00" {
				return parseExif(payload[6:])
			}
			continue
		}

		discardBytes := int64(length - 2)
		if _, err := io.CopyN(io.Discard, r, discardBytes); err != nil {
			return 1, err
		}
	}
	return 1, nil
}

//nolint:gocognit,gocritic,gocyclo,funlen // byte order check structure, switch simplification, parse loops, and length
func parseExif(tiff []byte) (int, error) {
	if len(tiff) < 8 {
		return 1, fmt.Errorf("tiff header too short")
	}

	var isLittleEndian bool
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		isLittleEndian = true
	case tiff[0] == 'M' && tiff[1] == 'M':
		isLittleEndian = false
	default:
		return 1, fmt.Errorf("invalid byte order")
	}

	readUint16 := func(b []byte, offset int) uint16 {
		if isLittleEndian {
			return uint16(b[offset]) | uint16(b[offset+1])<<8
		}
		return uint16(b[offset])<<8 | uint16(b[offset+1])
	}

	readUint32 := func(b []byte, offset int) uint32 {
		if isLittleEndian {
			return uint32(b[offset]) | uint32(b[offset+1])<<8 | uint32(b[offset+2])<<16 | uint32(b[offset+3])<<24
		}
		return uint32(b[offset])<<24 | uint32(b[offset+1])<<16 | uint32(b[offset+2])<<8 | uint32(b[offset+3])
	}

	magic := readUint16(tiff, 2)
	if magic != 0x002A {
		return 1, fmt.Errorf("invalid tiff magic number")
	}

	ifdOffset := int(readUint32(tiff, 4))
	if ifdOffset < 8 || ifdOffset >= len(tiff) {
		return 1, fmt.Errorf("invalid ifd offset")
	}

	if len(tiff) < ifdOffset+2 {
		return 1, fmt.Errorf("tiff too short for IFD count")
	}
	numEntries := int(readUint16(tiff, ifdOffset))
	entryOffset := ifdOffset + 2

	for i := 0; i < numEntries; i++ {
		if len(tiff) < entryOffset+12 {
			break
		}
		tag := readUint16(tiff, entryOffset)
		if tag == 0x0112 {
			valType := readUint16(tiff, entryOffset+2)
			switch valType {
			case 3:
				return int(readUint16(tiff, entryOffset+8)), nil
			case 4:
				return int(readUint32(tiff, entryOffset+8)), nil
			default:
				return 1, fmt.Errorf("unexpected orientation tag type: %d", valType)
			}
		}
		entryOffset += 12
	}

	return 1, nil
}

// RotateImage rotates the image according to the EXIF orientation tag (values 1-8).
func RotateImage(img image.Image, orientation int) image.Image {
	switch orientation {
	case 3: // 180 degrees
		return rotate180(img)
	case 6: // 90 degrees CW
		return rotate90(img)
	case 8: // 270 degrees CW (90 CCW)
		return rotate270(img)
	default:
		return img
	}
}

func rotate90(img image.Image) *image.RGBA {
	bounds := img.Bounds()
	dest := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dest.Set(bounds.Max.Y-1-y, x, img.At(x, y))
		}
	}
	return dest
}

func rotate180(img image.Image) *image.RGBA {
	bounds := img.Bounds()
	dest := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dest.Set(bounds.Max.X-1-x, bounds.Max.Y-1-y, img.At(x, y))
		}
	}
	return dest
}

func rotate270(img image.Image) *image.RGBA {
	bounds := img.Bounds()
	dest := image.NewRGBA(image.Rect(0, 0, bounds.Dy(), bounds.Dx()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dest.Set(y, bounds.Max.X-1-x, img.At(x, y))
		}
	}
	return dest
}
