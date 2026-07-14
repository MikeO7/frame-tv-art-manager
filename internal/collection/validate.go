package collection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"path/filepath"
	"strings"
	"unicode"
)

type validatedImage struct {
	data   []byte
	digest [sha256.Size]byte
	typeID FileType
	width  int
	height int
	stem   string
}

func readAndValidate(ctx context.Context, reader io.Reader, hint string, maxBytes, maxPixels int64) (validatedImage, error) {
	if err := ctx.Err(); err != nil {
		return validatedImage{}, fmt.Errorf("read import before work: %w", err)
	}
	stem, hintedType, err := validateHint(hint)
	if err != nil {
		return validatedImage{}, err
	}
	data, err := readBounded(reader, maxBytes)
	if err != nil {
		return validatedImage{}, err
	}
	if err := ctx.Err(); err != nil {
		return validatedImage{}, fmt.Errorf("decode import: %w", err)
	}
	decoded, err := decodeConfiguration(data, hintedType, maxPixels)
	if err != nil {
		return validatedImage{}, err
	}
	if err := fullyDecode(data, decoded.config, decoded.format); err != nil {
		return validatedImage{}, err
	}
	if err := ctx.Err(); err != nil {
		return validatedImage{}, fmt.Errorf("finish import validation: %w", err)
	}
	return validatedImage{
		data: data, digest: sha256.Sum256(data), typeID: decoded.typeID,
		width: decoded.config.Width, height: decoded.config.Height, stem: stem,
	}, nil
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	limited := &io.LimitedReader{R: reader, N: maxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read import: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("import exceeds %d-byte limit", maxBytes)
	}
	if len(data) == 0 {
		return nil, errors.New("import is empty")
	}
	return data, nil
}

type decodedConfiguration struct {
	config image.Config
	format string
	typeID FileType
}

func decodeConfiguration(data []byte, hintedType FileType, maxPixels int64) (decodedConfiguration, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return decodedConfiguration{}, fmt.Errorf("decode image configuration: %w", err)
	}
	typeID, err := decodeType(format)
	if err != nil {
		return decodedConfiguration{}, err
	}
	if hintedType != "" && hintedType != typeID {
		return decodedConfiguration{}, fmt.Errorf("filename type %s does not match %s content", hintedType, typeID)
	}
	if err := validateDimensions(config.Width, config.Height, maxPixels); err != nil {
		return decodedConfiguration{}, err
	}
	return decodedConfiguration{config: config, format: format, typeID: typeID}, nil
}

func fullyDecode(data []byte, config image.Config, format string) error {
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("fully decode image: %w", err)
	}
	bounds := decoded.Bounds()
	if decodedFormat != format || bounds.Dx() != config.Width || bounds.Dy() != config.Height {
		return errors.New("decoded image facts are inconsistent")
	}
	return nil
}

func validateDimensions(width, height int, maxPixels int64) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("image dimensions must be positive, got %dx%d", width, height)
	}
	if int64(width) > maxPixels/int64(height) {
		return fmt.Errorf("image dimensions %dx%d exceed %d-pixel limit", width, height, maxPixels)
	}
	return nil
}

func validateHint(hint string) (string, FileType, error) {
	if hint == "" {
		return "artwork", "", nil
	}
	if filepath.Base(hint) != hint || strings.ContainsAny(hint, `/\`) {
		return "", "", errors.New("import hint must be a base name")
	}
	lower := strings.ToLower(hint)
	if isReserved(lower) {
		return "", "", fmt.Errorf("import hint %q is reserved", hint)
	}
	ext := strings.ToLower(filepath.Ext(hint))
	var typeID FileType
	switch ext {
	case "":
	case ".jpg", ".jpeg":
		typeID = FileTypeJPEG
	case ".png":
		typeID = FileTypePNG
	default:
		return "", "", fmt.Errorf("unsupported filename extension %q", ext)
	}
	stem := sanitizeStem(strings.TrimSuffix(hint, filepath.Ext(hint)))
	if stem == "" {
		stem = "artwork"
	}
	return stem, typeID, nil
}

func sanitizeStem(value string) string {
	var output strings.Builder
	output.Grow(len(value))
	separator := false
	for _, char := range strings.ToLower(value) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			output.WriteRune(char)
			separator = false
		} else if output.Len() > 0 && !separator {
			output.WriteByte('-')
			separator = true
		}
		if output.Len() >= 80 {
			break
		}
	}
	return strings.Trim(output.String(), "-")
}

func decodeType(format string) (FileType, error) {
	switch format {
	case "jpeg":
		return FileTypeJPEG, nil
	case "png":
		return FileTypePNG, nil
	default:
		return "", fmt.Errorf("unsupported image format %q", format)
	}
}

func isReserved(lowerName string) bool {
	return lowerName == "mattes.json" || lowerName == controlDirectory ||
		strings.HasPrefix(lowerName, "._") || strings.HasPrefix(lowerName, ".")
}
