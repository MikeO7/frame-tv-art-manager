package optimize

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ReadOrientation reads the EXIF orientation tag from an image file if available.
//
//nolint:gocognit,gocyclo // custom EXIF marker scan with raw JPEG bounds checks
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

		orientation, stop, err := processMarker(r, marker)
		if err != nil {
			return 1, err
		}
		if stop {
			if orientation > 0 {
				return orientation, nil
			}
			continue
		}
	}
	return 1, nil
}

func processMarker(r io.Reader, marker byte) (int, bool, error) {
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:2]); err != nil {
		return 0, true, err
	}
	length := int(buf[0])<<8 | int(buf[1])
	if length < 2 {
		return 0, true, fmt.Errorf("invalid marker length")
	}

	if marker == 0xE1 {
		payload := make([]byte, length-2)
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, true, err
		}
		if len(payload) >= 6 && string(payload[:6]) == "Exif\x00\x00" {
			orientation, err := parseExif(payload[6:])
			return orientation, true, err
		}
		return 0, true, nil
	}

	discardBytes := int64(length - 2)
	if _, err := io.CopyN(io.Discard, r, discardBytes); err != nil {
		return 0, true, err
	}
	return 0, false, nil
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
			switch valType {
			case 3:
				return int(bo.Uint16(tiff[entryOffset+8:])), nil
			case 4:
				return int(bo.Uint32(tiff[entryOffset+8:])), nil
			default:
				return 1, fmt.Errorf("unexpected orientation tag type: %d", valType)
			}
		}
		entryOffset += 12
	}
	return 1, nil
}
