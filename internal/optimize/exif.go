package optimize

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var pngSignature = [8]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a} //nolint:gochecknoglobals // format constant

const (
	pngChunkEnd   = "IEND"
	pngChunkExif  = "eXIf"
	exifSignature = "Exif\x00\x00"
	// maxPNGExifBytes bounds how much of a PNG eXIf chunk is read. The
	// orientation tag sits in the first directory, within the first few
	// hundred bytes, and a JPEG cannot carry more than 64 KiB of EXIF at all.
	maxPNGExifBytes = 64 << 10
)

// readOrientation reads the EXIF orientation of a JPEG, or 1 (upright) when
// it carries none. It walks the markers exactly as readJPEGColorData does, so
// the two never disagree about where a file's segments are. An EXIF block
// that cannot be parsed says nothing about orientation and reads as upright:
// the picture is still a valid image and is optimized as it is stored.
func readOrientation(r io.Reader) (int, error) {
	var signature [2]byte
	if _, err := io.ReadFull(r, signature[:]); err != nil {
		return 1, err
	}
	if signature != [2]byte{0xff, 0xd8} {
		return 1, errors.New("not a JPEG (SOI missing)")
	}
	for {
		marker, length, err := nextJPEGSegment(r)
		if err != nil {
			return 1, err
		}
		if marker == jpegMarkerEOI || marker == jpegMarkerSOS {
			return 1, nil
		}
		if marker != jpegMarkerAPP1 {
			if _, err := io.CopyN(io.Discard, r, int64(length)); err != nil {
				return 1, fmt.Errorf("skip JPEG marker: %w", err)
			}
			continue
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			return 1, fmt.Errorf("read JPEG APP1 marker: %w", err)
		}
		if tiff, isExif := bytes.CutPrefix(payload, []byte(exifSignature)); isExif {
			return orientationOrUpright(tiff), nil
		}
	}
}

// orientationOrUpright returns the orientation an EXIF block declares, or 1
// when the block is malformed or declares none.
func orientationOrUpright(tiff []byte) int {
	orientation, err := parseExif(tiff)
	if err != nil {
		return 1
	}
	return orientation
}

// readPNGOrientation reads the standardized PNG eXIf chunk when present.
// Chunks other than eXIf are stepped over with seeker when it is not nil,
// which must be the stream r reads from, so image data of any size costs one
// seek instead of being read. Only the first maxPNGExifBytes of an eXIf chunk
// are read.
func readPNGOrientation(r io.Reader, seeker io.Seeker) (int, error) {
	var signature [8]byte
	if _, err := io.ReadFull(r, signature[:]); err != nil {
		return 1, err
	}
	if signature != pngSignature {
		return 1, errors.New("not a PNG (signature missing)")
	}
	skip := func(count int64) error {
		if seeker == nil {
			_, err := io.CopyN(io.Discard, r, count)
			return err
		}
		_, err := seeker.Seek(count, io.SeekCurrent)
		return err
	}
	for {
		var header [8]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return 1, err
		}
		length := int64(binary.BigEndian.Uint32(header[:4]))
		chunkType := string(header[4:])
		if chunkType == pngChunkEnd {
			return 1, nil
		}
		if chunkType != pngChunkExif {
			// The payload is followed by a four-byte checksum.
			if err := skip(length + 4); err != nil {
				return 1, fmt.Errorf("skip PNG %s chunk: %w", chunkType, err)
			}
			continue
		}
		payload := make([]byte, min(length, maxPNGExifBytes))
		if _, err := io.ReadFull(r, payload); err != nil {
			return 1, fmt.Errorf("read PNG eXIf chunk: %w", err)
		}
		payload, _ = bytes.CutPrefix(payload, []byte(exifSignature))
		return orientationOrUpright(payload), nil
	}
}

func parseExif(tiff []byte) (int, error) {
	if len(tiff) < 8 {
		return 1, fmt.Errorf("tiff header too short")
	}

	var bo binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	default:
		return 1, fmt.Errorf("invalid byte order")
	}

	if bo.Uint16(tiff[2:]) != 0x002A {
		return 1, fmt.Errorf("invalid tiff magic number")
	}

	ifdOffset := int(bo.Uint32(tiff[4:]))
	if ifdOffset < 8 || ifdOffset >= len(tiff) {
		return 1, fmt.Errorf("invalid ifd offset")
	}

	if len(tiff) < ifdOffset+2 {
		return 1, fmt.Errorf("tiff too short for IFD count")
	}
	numEntries := int(bo.Uint16(tiff[ifdOffset:]))
	entryOffset := ifdOffset + 2

	return findOrientationTag(tiff, bo, numEntries, entryOffset)
}

func findOrientationTag(tiff []byte, bo binary.ByteOrder, numEntries, entryOffset int) (int, error) {
	for i := 0; i < numEntries; i++ {
		if len(tiff) < entryOffset+12 {
			break
		}
		if bo.Uint16(tiff[entryOffset:]) == 0x0112 {
			valType := bo.Uint16(tiff[entryOffset+2:])
			count := bo.Uint32(tiff[entryOffset+4:])
			if valType != 3 {
				return 1, fmt.Errorf("unexpected orientation tag type: %d", valType)
			}
			if count != 1 {
				return 1, fmt.Errorf("unexpected orientation tag count: %d", count)
			}
			orientation := int(bo.Uint16(tiff[entryOffset+8:]))
			// Some cameras and editors write 0 for "not set".
			if orientation == 0 {
				return 1, nil
			}
			if orientation > 8 {
				return 1, fmt.Errorf("invalid orientation value: %d", orientation)
			}
			return orientation, nil
		}
		entryOffset += 12
	}
	return 1, nil
}
