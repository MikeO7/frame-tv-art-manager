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

func TestReadOrientationRejectsMalformedJPEG(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "short input", data: []byte{0xff}, want: "EOF"},
		{name: "missing SOI", data: []byte("no"), want: "SOI missing"},
		{name: "SOI without markers", data: []byte{0xff, 0xd8}, want: "EOF"},
		{name: "truncated fill bytes", data: []byte{0xff, 0xd8, 0xff, 0xff}, want: "EOF"},
		{name: "invalid marker prefix", data: []byte{0xff, 0xd8, 0x01, 0x02}, want: "invalid marker prefix"},
		{name: "short marker", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}, want: "EOF"},
		{name: "invalid marker length", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x01}, want: "invalid marker length"},
		{name: "truncated payload", data: []byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x10, 'E'}, want: "EOF"},
		{name: "truncated skipped marker", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x05, 0x01}, want: "EOF"},
		{name: "orientation tag type", data: exifJPEG(binary.LittleEndian, 9, 1), want: "unexpected orientation tag type"},
		{name: "orientation tag LONG type", data: exifJPEG(binary.LittleEndian, 4, 1), want: "unexpected orientation tag type"},
		{name: "orientation tag count", data: func() []byte {
			data := exifJPEG(binary.LittleEndian, 3, 1)
			binary.LittleEndian.PutUint32(data[26:], 2)
			return data
		}(), want: "unexpected orientation tag count"},
		{name: "orientation value", data: exifJPEG(binary.LittleEndian, 3, 9), want: "invalid orientation value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadOrientation(bytes.NewReader(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) || got != 1 {
				t.Fatalf("ReadOrientation() = (%d, %v), want (1, error containing %q)", got, err, tt.want)
			}
		})
	}
}

func TestReadOrientationDefaultsToUprightWithoutEXIF(t *testing.T) {
	t.Parallel()
	tests := map[string][]byte{
		"SOI then EOI": {0xff, 0xd8, 0xff, 0xd9},
		"fill bytes and non-EXIF APP1": {
			0xff, 0xd8,
			0xff, 0xff, 0xe0, 0x00, 0x04, 0x00, 0x00,
			0xff, 0xe1, 0x00, 0x08, 'N', 'O', 'T', 'E', 'X', 'I',
			0xff, 0xd9,
		},
		"encoded JPEG": func() []byte {
			var encoded bytes.Buffer
			if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
				t.Fatal(err)
			}
			return encoded.Bytes()
		}(),
	}
	for name, data := range tests {
		if got, err := ReadOrientation(bytes.NewReader(data)); err != nil || got != 1 {
			t.Errorf("%s: ReadOrientation() = (%d, %v), want (1, nil)", name, got, err)
		}
	}
}

func TestReadPNGOrientationRejectsMalformedChunks(t *testing.T) {
	t.Parallel()
	tooLarge := append([]byte(nil), pngSignature[:]...)
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header[:4], (64<<20)+1)
	copy(header[4:], "text")
	tooLarge = append(tooLarge, header...)
	cases := map[string][]byte{
		"empty":               nil,
		"wrong signature":     make([]byte, 8),
		"signature only":      pngSignature[:],
		"oversized chunk":     tooLarge,
		"truncated eXIf":      appendPNGChunkBytes(pngSignature[:], "eXIf", []byte{1, 2}, false),
		"empty eXIf":          appendPNGChunkBytes(pngSignature[:], "eXIf", nil, false),
		"truncated chunk CRC": appendPNGChunkBytes(pngSignature[:], "text", []byte{1}, false),
	}
	for name, payload := range cases {
		if orientation, err := ReadPNGOrientation(bytes.NewReader(payload)); err == nil || orientation != 1 {
			t.Errorf("ReadPNGOrientation(%s) = (%d, %v), want (1, error)", name, orientation, err)
		}
	}
	iend := appendPNGChunkBytes(pngSignature[:], pngChunkEnd, nil, true)
	if orientation, err := ReadPNGOrientation(bytes.NewReader(iend)); err != nil || orientation != 1 {
		t.Fatalf("ReadPNGOrientation(IEND) = %d, %v", orientation, err)
	}
}

func TestParseExifRejectsInvalidTIFFHeaders(t *testing.T) {
	t.Parallel()
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
		{name: "no room for IFD count", data: []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 0}, want: "too short for IFD count"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseExif(tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.want) || got != 1 {
				t.Fatalf("parseExif() = (%d, %v), want (1, error containing %q)", got, err, tt.want)
			}
		})
	}
}

func TestParseExifDefaultsToUprightWithoutOrientationTag(t *testing.T) {
	t.Parallel()
	header := []byte{'I', 'I', 0x2a, 0x00, 8, 0, 0, 0}
	withIFD := func(entries ...byte) []byte { return append(append([]byte(nil), header...), entries...) }
	otherTag := make([]byte, 2+12)
	otherTag[0] = 1
	binary.LittleEndian.PutUint16(otherTag[2:], 0x0100)
	tests := map[string][]byte{
		"no entries":               withIFD(0, 0, 0, 0, 0, 0),
		"other tag only":           withIFD(otherTag...),
		"entry claimed, truncated": withIFD(1, 0, 0, 0, 0, 0),
		"zeroed entry":             withIFD(append([]byte{1, 0}, make([]byte, 13)...)...),
	}
	for name, data := range tests {
		if got, err := parseExif(data); err != nil || got != 1 {
			t.Errorf("%s: parseExif() = (%d, %v), want (1, nil)", name, got, err)
		}
	}
}

func appendPNGChunkBytes(prefix []byte, chunkType string, payload []byte, includeCRC bool) []byte {
	result := append([]byte(nil), prefix...)
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header[:4], uint32(len(payload)))
	copy(header[4:], chunkType)
	result = append(result, header...)
	result = append(result, payload...)
	if includeCRC {
		result = append(result, 0, 0, 0, 0)
	}
	return result
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
