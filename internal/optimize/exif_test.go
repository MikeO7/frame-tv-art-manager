package optimize

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
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
	orientation, err := readPNGOrientation(bytes.NewReader(withExif), nil)
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
			got, err := readOrientation(bytes.NewReader(exifJPEG(tt.order, 3, tt.orientation)))
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
		{name: "invalid marker prefix", data: []byte{0xff, 0xd8, 0x01, 0x02}, want: "invalid JPEG marker prefix"},
		{name: "stuffed zero outside scan data", data: []byte{0xff, 0xd8, 0xff, 0x00, 0x00, 0x02}, want: "stuffed JPEG marker"},
		{name: "short marker", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}, want: "EOF"},
		{name: "invalid marker length", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x01}, want: "invalid JPEG marker length"},
		{name: "truncated payload", data: []byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x10, 'E'}, want: "EOF"},
		{name: "truncated skipped marker", data: []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x05, 0x01}, want: "EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := readOrientation(bytes.NewReader(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) || got != 1 {
				t.Fatalf("ReadOrientation() = (%d, %v), want (1, error containing %q)", got, err, tt.want)
			}
		})
	}
}

// An EXIF block that cannot be parsed must not make the picture unreadable:
// the transform would skip it and every sync would stage the collection again.
func TestReadOrientationTreatsUnparseableEXIFAsUpright(t *testing.T) {
	t.Parallel()
	withTIFF := func(tiff ...byte) []byte {
		length := len(exifSignature) + len(tiff) + 2
		data := append([]byte{0xff, 0xd8, 0xff, 0xe1, byte(length >> 8), byte(length)}, exifSignature...)
		return append(append(data, tiff...), 0xff, 0xd9)
	}
	tests := map[string][]byte{
		"orientation tag type":      exifJPEG(binary.LittleEndian, 9, 6),
		"orientation tag LONG type": exifJPEG(binary.LittleEndian, 4, 6),
		"orientation tag count": func() []byte {
			data := exifJPEG(binary.LittleEndian, 3, 6)
			binary.LittleEndian.PutUint32(data[26:], 2)
			return data
		}(),
		"orientation value": exifJPEG(binary.LittleEndian, 3, 9),
		"empty block":       withTIFF(),
		"bad byte order":    withTIFF([]byte("ZZ******")...),
		"offset past data":  withTIFF('I', 'I', 42, 0, 20, 0, 0, 0),
	}
	for name, data := range tests {
		if got, err := readOrientation(bytes.NewReader(data)); err != nil || got != 1 {
			t.Errorf("%s: readOrientation() = (%d, %v), want (1, nil)", name, got, err)
		}
	}
	png := appendPNGChunkBytes(pngSignature[:], pngChunkExif, []byte("ZZ******"), true)
	if got, err := readPNGOrientation(bytes.NewReader(png), nil); err != nil || got != 1 {
		t.Errorf("readPNGOrientation(unparseable eXIf) = (%d, %v), want (1, nil)", got, err)
	}
}

// readOrientation and readJPEGColorData share one marker walk: a standalone
// TEM or restart marker carries no length, and reading one misplaces every
// segment after it.
func TestReadOrientationPassesStandaloneMarkers(t *testing.T) {
	t.Parallel()
	withExif := exifJPEG(binary.BigEndian, 3, 6)
	data := append([]byte{0xff, 0xd8, 0xff, 0x01, 0xff, 0xd0, 0xff, 0xff, 0xd7}, withExif[2:]...)
	if got, err := readOrientation(bytes.NewReader(data)); err != nil || got != 6 {
		t.Fatalf("readOrientation(standalone markers before EXIF) = (%d, %v), want (6, nil)", got, err)
	}
	if _, err := readJPEGColorData(bytes.NewReader(data)); err != nil {
		t.Fatalf("readJPEGColorData() disagrees about the same markers: %v", err)
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
		if got, err := readOrientation(bytes.NewReader(data)); err != nil || got != 1 {
			t.Errorf("%s: ReadOrientation() = (%d, %v), want (1, nil)", name, got, err)
		}
	}
}

func TestReadPNGOrientationRejectsMalformedChunks(t *testing.T) {
	t.Parallel()
	truncatedExif := appendPNGChunkBytes(pngSignature[:], pngChunkExif, []byte{1, 2}, false)
	binary.BigEndian.PutUint32(truncatedExif[8:], 10)
	cases := map[string][]byte{
		"empty":               nil,
		"wrong signature":     make([]byte, 8),
		"signature only":      pngSignature[:],
		"truncated eXIf":      truncatedExif,
		"truncated chunk CRC": appendPNGChunkBytes(pngSignature[:], "text", []byte{1}, false),
	}
	for name, payload := range cases {
		if orientation, err := readPNGOrientation(bytes.NewReader(payload), nil); err == nil || orientation != 1 {
			t.Errorf("ReadPNGOrientation(%s) = (%d, %v), want (1, error)", name, orientation, err)
		}
	}
	iend := appendPNGChunkBytes(pngSignature[:], pngChunkEnd, nil, true)
	if orientation, err := readPNGOrientation(bytes.NewReader(iend), nil); err != nil || orientation != 1 {
		t.Fatalf("ReadPNGOrientation(IEND) = %d, %v", orientation, err)
	}
}

// sparsePNG is a PNG whose image data is a hole of the given size, so a test
// can present a chunk far larger than it could hold in memory. It counts the
// bytes actually read.
type sparsePNG struct {
	head, tail []byte
	hole       int64
	position   int64
	read       int64
}

func (file *sparsePNG) Read(buffer []byte) (int, error) {
	size := int64(len(file.head)) + file.hole + int64(len(file.tail))
	if file.position >= size {
		return 0, io.EOF
	}
	count := 0
	for count < len(buffer) && file.position < size {
		switch tailStart := int64(len(file.head)) + file.hole; {
		case file.position < int64(len(file.head)):
			buffer[count] = file.head[file.position]
		case file.position < tailStart:
			buffer[count] = 0
		default:
			buffer[count] = file.tail[file.position-tailStart]
		}
		count++
		file.position++
	}
	file.read += int64(count)
	return count, nil
}

func (file *sparsePNG) Seek(offset int64, whence int) (int64, error) {
	if whence != io.SeekCurrent {
		return 0, errors.New("sparsePNG only seeks from the current position")
	}
	file.position += offset
	return file.position, nil
}

func TestReadPNGOrientationSeeksOverImageData(t *testing.T) {
	t.Parallel()
	const imageData = int64(200 << 20)
	head := append([]byte(nil), pngSignature[:]...)
	head = binary.BigEndian.AppendUint32(head, uint32(imageData))
	head = append(head, pngChunkImageData...)
	jpegWithExif := exifJPEG(binary.LittleEndian, 3, 6)
	// Four checksum bytes close the image data chunk before eXIf starts.
	tail := appendPNGChunkBytes(make([]byte, 4), pngChunkExif, jpegWithExif[12:len(jpegWithExif)-2], true)
	file := &sparsePNG{head: head, hole: imageData, tail: tail}
	orientation, err := readOrientationForExtension(context.Background(), file, extPNG)
	if err != nil || orientation != 6 {
		t.Fatalf("readOrientationForExtension() = (%d, %v), want (6, nil) past a %d MiB chunk", orientation, err, imageData>>20)
	}
	if file.read > 4096 {
		t.Fatalf("read %d bytes to find the orientation, want only the chunk headers and eXIf", file.read)
	}
}

func TestReadPNGOrientationReadsOnlyTheStartOfAHugeExifChunk(t *testing.T) {
	t.Parallel()
	jpegWithExif := exifJPEG(binary.LittleEndian, 3, 8)
	tiff := jpegWithExif[12 : len(jpegWithExif)-2]
	const declared = int64(32 << 20)
	head := append([]byte(nil), pngSignature[:]...)
	head = binary.BigEndian.AppendUint32(head, uint32(declared))
	head = append(append(head, pngChunkExif...), tiff...)
	file := &sparsePNG{head: head, hole: declared - int64(len(tiff)) + 4}
	orientation, err := readPNGOrientation(file, file)
	if err != nil || orientation != 8 {
		t.Fatalf("readPNGOrientation() = (%d, %v), want (8, nil)", orientation, err)
	}
	if file.read > maxPNGExifBytes+64 {
		t.Fatalf("read %d bytes of a %d-byte eXIf chunk, want at most %d", file.read, declared, maxPNGExifBytes)
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
