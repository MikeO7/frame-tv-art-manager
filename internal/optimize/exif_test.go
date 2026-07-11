package optimize

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func exifJPEG(order binary.ByteOrder, tagType uint16, orientation uint32) []byte {
	tiff := make([]byte, 8+2+12)
	if order == binary.LittleEndian {
		copy(tiff, "II")
	} else {
		copy(tiff, "MM")
	}
	order.PutUint16(tiff[2:], 0x002A)
	order.PutUint32(tiff[4:], 8)
	order.PutUint16(tiff[8:], 1)
	order.PutUint16(tiff[10:], 0x0112)
	order.PutUint16(tiff[12:], tagType)
	order.PutUint32(tiff[14:], 1)
	if tagType == 3 {
		order.PutUint16(tiff[18:], uint16(orientation))
	} else {
		order.PutUint32(tiff[18:], orientation)
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	jpeg = append(jpeg, payload...)
	return append(jpeg, 0xff, 0xd9)
}

func TestReadOrientationEXIFVariants(t *testing.T) {
	for _, tt := range []struct {
		name        string
		order       binary.ByteOrder
		tagType     uint16
		orientation uint32
	}{
		{name: "little endian short", order: binary.LittleEndian, tagType: 3, orientation: 6},
		{name: "big endian long", order: binary.BigEndian, tagType: 4, orientation: 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadOrientation(bytes.NewReader(exifJPEG(tt.order, tt.tagType, tt.orientation)))
			if err != nil || got != int(tt.orientation) {
				t.Fatalf("ReadOrientation() = (%d, %v), want %d", got, err, tt.orientation)
			}
		})
	}
}

func TestReadOrientationMalformedInputs(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "short input", data: []byte{0xff}, want: "EOF"},
		{name: "missing SOI", data: []byte("no"), want: "SOI missing"},
		{name: "invalid marker prefix", data: []byte{0xff, 0xd8, 0x01, 0x02}, want: "invalid marker prefix"},
		{name: "short marker", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}, want: "EOF"},
		{name: "invalid marker length", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x01}, want: "invalid marker length"},
		{name: "truncated payload", data: []byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x10, 'E'}, want: "EOF"},
		{name: "truncated skipped marker", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x05, 0x01}, want: "EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadOrientation(bytes.NewReader(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReadOrientation() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseExifValidationAndMissingTag(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "short header", data: []byte("II"), want: "header too short"},
		{name: "bad byte order", data: []byte("ZZ******"), want: "invalid byte order"},
		{name: "bad magic", data: []byte{'I', 'I', 0, 0, 8, 0, 0, 0}, want: "magic"},
		{name: "offset below header", data: []byte{'I', 'I', 42, 0, 1, 0, 0, 0}, want: "ifd offset"},
		{name: "offset past data", data: []byte{'I', 'I', 42, 0, 20, 0, 0, 0}, want: "ifd offset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseExif(tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseExif() error = %v, want containing %q", err, tt.want)
			}
		})
	}

	missing := make([]byte, 10+12)
	copy(missing, "II")
	binary.LittleEndian.PutUint16(missing[2:], 42)
	binary.LittleEndian.PutUint32(missing[4:], 8)
	binary.LittleEndian.PutUint16(missing[8:], 1)
	if got, err := parseExif(missing); err != nil || got != 1 {
		t.Fatalf("parseExif missing orientation = (%d, %v)", got, err)
	}

	badTypeJPEG := exifJPEG(binary.LittleEndian, 9, 1)
	if _, err := ReadOrientation(bytes.NewReader(badTypeJPEG)); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("unexpected orientation type error = %v", err)
	}
}
