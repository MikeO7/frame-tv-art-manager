package optimize

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
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

func TestReadPNGOrientationEXIFChunk(t *testing.T) {
	t.Parallel()
	jpegWithExif := exifJPEG(binary.LittleEndian, 3, 6)
	tiff := jpegWithExif[12 : len(jpegWithExif)-2]
	var chunk bytes.Buffer
	if err := binary.Write(&chunk, binary.BigEndian, uint32(len(tiff))); err != nil {
		t.Fatal(err)
	}
	chunk.WriteString("eXIf")
	chunk.Write(tiff)
	checksum := crc32.ChecksumIEEE(append([]byte("eXIf"), tiff...))
	if err := binary.Write(&chunk, binary.BigEndian, checksum); err != nil {
		t.Fatal(err)
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	raw := encoded.Bytes()
	const afterIHDR = 33
	withExif := append(append(append([]byte(nil), raw[:afterIHDR]...), chunk.Bytes()...), raw[afterIHDR:]...)
	orientation, err := ReadPNGOrientation(bytes.NewReader(withExif))
	if err != nil || orientation != 6 {
		t.Fatalf("ReadPNGOrientation() = (%d, %v), want (6, nil)", orientation, err)
	}
}

func TestReadOrientationEXIFVariants(t *testing.T) {
	for _, tt := range []struct {
		name        string
		order       binary.ByteOrder
		orientation uint32
	}{
		{name: "little endian short", order: binary.LittleEndian, orientation: 6},
		{name: "big endian short", order: binary.BigEndian, orientation: 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadOrientation(bytes.NewReader(exifJPEG(tt.order, 3, tt.orientation)))
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

	noEntries := []byte{
		'I', 'I', 0x2a, 0x00, 8, 0, 0, 0,
		0, 0, // no IFD entries
		0, 0, 0, 0, // no next IFD
	}
	if got, err := parseExif(noEntries); err != nil || got != 1 {
		t.Fatalf("parseExif no entries = (%d, %v)", got, err)
	}

	badTypeJPEG := exifJPEG(binary.LittleEndian, 9, 1)
	if _, err := ReadOrientation(bytes.NewReader(badTypeJPEG)); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("unexpected orientation type error = %v", err)
	}
	longJPEG := exifJPEG(binary.LittleEndian, 4, 1)
	if _, err := ReadOrientation(bytes.NewReader(longJPEG)); err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("LONG orientation type error = %v", err)
	}
	badCountJPEG := exifJPEG(binary.LittleEndian, 3, 1)
	binary.LittleEndian.PutUint32(badCountJPEG[26:], 2)
	if _, err := ReadOrientation(bytes.NewReader(badCountJPEG)); err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("orientation count error = %v", err)
	}
	badValueJPEG := exifJPEG(binary.LittleEndian, 3, 9)
	if _, err := ReadOrientation(bytes.NewReader(badValueJPEG)); err == nil || !strings.Contains(err.Error(), "value") {
		t.Fatalf("orientation value error = %v", err)
	}
}

func TestProcessMarker_App1PayloadWithoutExif(t *testing.T) {
	buf := bytes.NewBuffer(nil)
	_, _ = buf.Write([]byte{0x00, 0x08})
	_, _ = buf.WriteString("NOTEXI")

	gotOrientation, stop, err := processMarker(buf, 0xe1)
	if err != nil {
		t.Fatalf("processMarker() = %v", err)
	}
	if gotOrientation != 0 {
		t.Fatalf("expected orientation 0 from App1 payload without EXIF, got %d", gotOrientation)
	}
	if !stop {
		t.Fatal("expected stop=true after App1 payload")
	}
}

func TestProcessMarker_DiscardPayloadPath(t *testing.T) {
	// App0 marker, 2-byte payload: [0x01 0x02]
	buf := bytes.NewBuffer(nil)
	_, _ = buf.Write([]byte{0x00, 0x04})
	_, _ = buf.Write([]byte{0x01, 0x02})

	gotOrientation, stop, err := processMarker(buf, 0xe0)
	if err != nil {
		t.Fatalf("processMarker() = %v", err)
	}
	if gotOrientation != 0 {
		t.Fatalf("expected zero orientation, got %d", gotOrientation)
	}
	if stop {
		t.Fatal("expected stop=false for non-App1 marker")
	}
}

func TestReadOrientation_ShortMarkerAfterSOI(t *testing.T) {
	orientation, err := ReadOrientation(bytes.NewReader([]byte{0xff, 0xd8}))
	if err == nil || orientation != 1 {
		t.Fatalf("ReadOrientation() = (%d, %v)", orientation, err)
	}
}

func TestReadOrientation_TruncatedFillBytes(t *testing.T) {
	orientation, err := ReadOrientation(bytes.NewReader([]byte{0xff, 0xd8, 0xff, 0xff}))
	if err == nil || orientation != 1 {
		t.Fatalf("ReadOrientation() = (%d, %v)", orientation, err)
	}
}

func TestReadOrientation_SkipsFFFillersAndNonExifApp1(t *testing.T) {
	// SOI, 0xFF fill byte, fake APP0, App1 without EXIF marker, then EOI.
	raw := []byte{
		0xff, 0xd8,
		0xff, 0xff, 0xe0, 0x00, 0x04, 0x00, 0x00,
		0xff, 0xe1, 0x00, 0x08, 'N', 'O', 'T', 'E', 'X', 'I',
		0xff, 0xd9,
	}
	got, err := ReadOrientation(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadOrientation() = %v", err)
	}
	if got != 1 {
		t.Fatalf("expected default orientation 1, got %d", got)
	}
}

func TestParseExif_DefaultOnOffsetOutOfRange(t *testing.T) {
	// TIFF header with a valid IFD offset but missing a valid IFD entry.
	tiff := []byte{
		'I', 'I', 0x2a, 0x00, 8, 0, 0, 0,
		1, 0, // one entry
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0,
	}
	got, err := parseExif(tiff)
	if err != nil {
		t.Fatalf("parseExif() = %v", err)
	}
	if got != 1 {
		t.Fatalf("expected default orientation, got %d", got)
	}
}

func writeJPEGWithExifOrientation(t *testing.T, path string, width, height int, orientation uint32) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 40, A: 255})
		}
	}

	var baseBuf bytes.Buffer
	if err := jpeg.Encode(&baseBuf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode image: %v", err)
	}

	exifSegment := exifJPEG(binary.LittleEndian, 3, orientation)
	exifPayload := exifSegment[2 : len(exifSegment)-2] // keep just marker+len+Exif payload, drop SOI/EOI

	encoded := baseBuf.Bytes()
	withEXIF := make([]byte, 0, len(encoded)+len(exifPayload))
	withEXIF = append(withEXIF, encoded[:2]...)
	withEXIF = append(withEXIF, exifPayload...)
	withEXIF = append(withEXIF, encoded[2:]...)

	if err := os.WriteFile(path, withEXIF, 0o600); err != nil {
		t.Fatalf("write JPEG with EXIF: %v", err)
	}
}
