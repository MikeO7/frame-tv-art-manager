package optimize

import (
	"crypto/sha256"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

func availableContentName(
	directory string,
	currentName string,
	label string,
	digest [sha256.Size]byte,
	extension string,
) (string, error) {
	for digestBytes := 8; digestBytes <= sha256.Size; digestBytes += 2 {
		candidate := artwork.BuildContentName(label, digest, extension, digestBytes)
		if candidate == currentName {
			return candidate, nil
		}
		_, err := os.Lstat(filepath.Join(directory, candidate))
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("every content-digest filename is occupied")
}

func fileDigest(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

// ValidateImage verifies that the complete encoded pixel stream decodes.
func ValidateImage(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, _, err = image.Decode(f)
	return err
}
