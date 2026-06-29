package main

import (
	"fmt"
	"io"
)

func advanceToNextMarker(r io.Reader, buf []byte) (byte, error) {
	return 0, nil
}

func processMarker(r io.Reader, marker byte) (int, error) {
	return 0, nil
}

func ReadOrientation(r io.Reader) (int, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:2]); err != nil {
		return 1, err
	}
	if buf[0] != 0xFF || buf[1] != 0xD8 {
		return 1, fmt.Errorf("not a JPEG (SOI missing)")
	}

	for {
		marker, err := advanceToNextMarker(r, buf[:2])
		if err != nil {
			return 1, err
		}

		if marker == 0xD9 || marker == 0xDA { // EOI or SOS
			break
		}

		orientation, err := processMarker(r, marker)
		if err != nil {
			return 1, err
		}
		if orientation > 0 {
			return orientation, nil
		}
	}
	return 1, nil
}
