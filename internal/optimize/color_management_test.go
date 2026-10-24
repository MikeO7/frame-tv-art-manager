package optimize

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestColorMetadataReadersClassifyStreams(t *testing.T) {
	t.Parallel()
	jpegCases := [][]byte{
		nil,
		{0, 0},
		{0xff, 0xd8},
		{0xff, 0xd8, 0, 0},
		{0xff, 0xd8, 0xff, 0xe1},
		{0xff, 0xd8, 0xff, 0xe1, 0, 1},
		{0xff, 0xd8, 0xff, 0xe2, 0, 16, 'I'},
		{0xff, 0xd8, 0xff, 0xe1, 0, 4, 'x'},
	}
	for index, payload := range jpegCases {
		if _, err := readJPEGColorData(bytes.NewReader(payload)); err == nil {
			t.Fatalf("readJPEGColorData(case %d) error = nil", index)
		}
	}
	for _, payload := range [][]byte{
		{0xff, 0xd8, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xe1, 0, 4, 'x', 'y', 0xff, 0xd9},
	} {
		if metadata, err := readJPEGColorData(bytes.NewReader(payload)); err != nil || metadata.description != "" {
			t.Fatalf("readJPEGColorData(valid) = %+v, %v", metadata, err)
		}
	}
	icc := append([]byte{0xff, 0xd8, 0xff, 0xe2, 0, 16}, []byte("ICC_PROFILE\x00\x01\x01")...)
	icc = append(icc, 0xff, 0xd9)
	if metadata, err := readJPEGColorData(bytes.NewReader(icc)); err != nil || metadata.description != jpegICCMetadata {
		t.Fatalf("readJPEGColorData(ICC) = %+v, %v", metadata, err)
	}

	pngCases := [][]byte{
		nil,
		make([]byte, 8),
		pngSignature[:],
		appendPNGChunkBytes(pngSignature[:], "text", []byte{1}, false),
	}
	for index, payload := range pngCases {
		if _, err := readPNGColorData(bytes.NewReader(payload)); err == nil {
			t.Fatalf("readPNGColorData(case %d) error = nil", index)
		}
	}
	for chunkType, chunkData := range map[string][]byte{pngChunkGamma: make([]byte, 4), pngChunkChromaticity: make([]byte, 32)} {
		payload := appendPNGChunkBytes(pngSignature[:], chunkType, chunkData, true)
		payload = appendPNGChunkBytes(payload, pngChunkEnd, nil, true)
		if metadata, err := readPNGColorData(bytes.NewReader(payload)); err != nil || metadata.description != "PNG "+chunkType {
			t.Fatalf("readPNGColorData(%s) = %+v, %v", chunkType, metadata, err)
		}
	}
	for _, chunkType := range []string{"IDAT", pngChunkEnd} {
		payload := appendPNGChunkBytes(pngSignature[:], chunkType, nil, true)
		if metadata, err := readPNGColorData(bytes.NewReader(payload)); err != nil || metadata.description != "" {
			t.Fatalf("readPNGColorData(%s) = %+v, %v", chunkType, metadata, err)
		}
	}
}

func TestReadEmbeddedColorDataRejectsTruncatedJPEG(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "malformed.jpg")
	if err := os.WriteFile(path, []byte{0xff, 0xd8}, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := readEmbeddedColorData(context.Background(), file, extJPG); err == nil {
		t.Fatal("readEmbeddedColorData(truncated JPEG) error = nil")
	}
}
