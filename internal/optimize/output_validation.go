package optimize

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

type contentNameRequest struct {
	directory, currentName, label, extension string
	digest                                   [sha256.Size]byte
}

func availableContentName(ctx context.Context, request contentNameRequest) (string, error) {
	for digestBytes := 8; digestBytes <= sha256.Size; digestBytes += 2 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		candidate := artwork.BuildContentName(request.label, request.digest, request.extension, digestBytes)
		if candidate == request.currentName {
			return candidate, nil
		}
		_, err := os.Lstat(filepath.Join(request.directory, candidate))
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("every content-digest filename is occupied")
}

func fileDigest(ctx context.Context, path string) ([sha256.Size]byte, error) {
	if err := ctx.Err(); err != nil {
		return [sha256.Size]byte{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, &contextReader{ctx: ctx, reader: file}); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

// ValidateImage verifies that the complete encoded pixel stream decodes.
func ValidateImage(ctx context.Context, path string) error {
	_, _, err := decodeImageOutput(ctx, path)
	return err
}

func validateImageOutput(ctx context.Context, path, expectedFormat string, expectedWidth, expectedHeight int) error {
	decoded, format, err := decodeImageOutput(ctx, path)
	if err != nil {
		return err
	}
	if format != expectedFormat {
		return fmt.Errorf("output format %q, want %q", format, expectedFormat)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != expectedWidth || bounds.Dy() != expectedHeight {
		return fmt.Errorf(
			"output dimensions %dx%d, want %dx%d",
			bounds.Dx(), bounds.Dy(), expectedWidth, expectedHeight,
		)
	}
	return nil
}

func decodeImageOutput(ctx context.Context, path string) (image.Image, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()

	decoded, format, err := image.Decode(&contextReader{ctx: ctx, reader: f})
	return decoded, format, err
}
