package optimize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MikeO7/frame-tv-art-manager/internal/artwork"
)

func TestColorMetadataParsersRejectMalformedStreams(t *testing.T) {
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
		if _, err := findJPEGColorMetadata(bytes.NewReader(payload)); err == nil {
			t.Fatalf("findJPEGColorMetadata(case %d) error = nil", index)
		}
	}
	for _, payload := range [][]byte{
		{0xff, 0xd8, 0xff, 0xd9},
		{0xff, 0xd8, 0xff, 0xe1, 0, 4, 'x', 'y', 0xff, 0xd9},
	} {
		if metadata, err := findJPEGColorMetadata(bytes.NewReader(payload)); err != nil || metadata != "" {
			t.Fatalf("findJPEGColorMetadata(valid) = %q, %v", metadata, err)
		}
	}
	icc := append([]byte{0xff, 0xd8, 0xff, 0xe2, 0, 16}, []byte("ICC_PROFILE\x00\x01\x01")...)
	if metadata, err := findJPEGColorMetadata(bytes.NewReader(icc)); err != nil || metadata != "JPEG ICC" {
		t.Fatalf("findJPEGColorMetadata(ICC) = %q, %v", metadata, err)
	}

	pngCases := [][]byte{
		nil,
		make([]byte, 8),
		pngSignature[:],
		appendPNGChunkBytes(pngSignature[:], "text", []byte{1}, false),
	}
	for index, payload := range pngCases {
		if _, err := findPNGColorMetadata(bytes.NewReader(payload)); err == nil {
			t.Fatalf("findPNGColorMetadata(case %d) error = nil", index)
		}
	}
	for _, chunkType := range []string{"iCCP", "gAMA", "cHRM"} {
		payload := appendPNGChunkBytes(pngSignature[:], chunkType, nil, true)
		if metadata, err := findPNGColorMetadata(bytes.NewReader(payload)); err != nil || metadata != "PNG "+chunkType {
			t.Fatalf("findPNGColorMetadata(%s) = %q, %v", chunkType, metadata, err)
		}
	}
	for _, chunkType := range []string{"IDAT", pngChunkEnd} {
		payload := appendPNGChunkBytes(pngSignature[:], chunkType, nil, true)
		if metadata, err := findPNGColorMetadata(bytes.NewReader(payload)); err != nil || metadata != "" {
			t.Fatalf("findPNGColorMetadata(%s) = %q, %v", chunkType, metadata, err)
		}
	}
}

func TestPNGOrientationRejectsMalformedChunks(t *testing.T) {
	t.Parallel()
	tooLarge := append([]byte(nil), pngSignature[:]...)
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header[:4], (64<<20)+1)
	copy(header[4:], "text")
	tooLarge = append(tooLarge, header...)
	cases := [][]byte{
		nil,
		make([]byte, 8),
		pngSignature[:],
		tooLarge,
		appendPNGChunkBytes(pngSignature[:], "eXIf", []byte{1, 2}, false),
		appendPNGChunkBytes(pngSignature[:], "eXIf", nil, false),
		appendPNGChunkBytes(pngSignature[:], "text", []byte{1}, false),
	}
	for index, payload := range cases {
		if _, err := ReadPNGOrientation(bytes.NewReader(payload)); err == nil {
			t.Fatalf("ReadPNGOrientation(case %d) error = nil", index)
		}
	}
	iend := appendPNGChunkBytes(pngSignature[:], pngChunkEnd, nil, true)
	if orientation, err := ReadPNGOrientation(bytes.NewReader(iend)); err != nil || orientation != 1 {
		t.Fatalf("ReadPNGOrientation(IEND) = %d, %v", orientation, err)
	}
}

func TestAuditSafetyHelperBranches(t *testing.T) {
	t.Parallel()
	if err := validateOutputDimensions(0, 1, 1); err == nil {
		t.Fatal("validateOutputDimensions(invalid) error = nil")
	}
	if err := validateOutputDimensions(1, 1, 0); err != nil {
		t.Fatalf("validateOutputDimensions(default limit) = %v", err)
	}
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight, cfg.MaxWorkingBytes = 1, 1, 0
	if err := validateWorkingPixels(1, cfg); err != nil {
		t.Fatalf("validateWorkingPixels(default memory) = %v", err)
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "image.jpg")
	writeTestImage(t, path, 8, 6)
	input := StageInput{Name: "image.jpg", Path: path, Width: 8, Height: 6}
	if orientation, err := preflightOrientation(context.Background(), input); err != nil || orientation != 1 {
		t.Fatalf("preflightOrientation() = %d, %v", orientation, err)
	}
	malformedPath := filepath.Join(directory, "malformed.jpg")
	if err := os.WriteFile(malformedPath, []byte{0xff, 0xd8}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := preflightOrientation(context.Background(), StageInput{
		Name: "malformed.jpg", Path: malformedPath,
	}); err == nil {
		t.Fatal("preflightOrientation(malformed) error = nil")
	}
	malformed, err := os.Open(malformedPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enforceColorProfilePolicy(context.Background(), malformed, extJPG, "assume-srgb"); err == nil {
		_ = malformed.Close()
		t.Fatal("enforceColorProfilePolicy(malformed) error = nil")
	}
	if err := malformed.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := preflightOrientation(ctx, input); err == nil {
		t.Fatal("preflightOrientation(canceled) error = nil")
	}

	digest := sha256.Sum256([]byte("name"))
	request := contentNameRequest{directory: directory, label: "label", digest: digest, extension: extJPG}
	if _, err := availableContentName(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("availableContentName(canceled) error = %v", err)
	}
	if _, err := fileDigest(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("fileDigest(canceled) error = %v", err)
	}
	if err := ValidateImage(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("ValidateImage(canceled) error = %v", err)
	}
	current := artwork.BuildContentName("label", digest, extJPG, 8)
	request.currentName = current
	if got, err := availableContentName(context.Background(), request); err != nil || got != current {
		t.Fatalf("availableContentName(current) = %q, %v", got, err)
	}
	for digestBytes := 8; digestBytes <= sha256.Size; digestBytes += 2 {
		name := artwork.BuildContentName("occupied", digest, extJPG, digestBytes)
		if err := os.WriteFile(filepath.Join(directory, name), []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request.currentName, request.label = "", "occupied"
	if _, err := availableContentName(context.Background(), request); err == nil {
		t.Fatal("availableContentName(all occupied) error = nil")
	}
	notDirectory := filepath.Join(directory, "not-directory")
	if err := os.WriteFile(notDirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.directory, request.label = notDirectory, "label"
	if _, err := availableContentName(context.Background(), request); err == nil {
		t.Fatal("availableContentName(non-directory) error = nil")
	}
	if _, err := fileDigest(context.Background(), filepath.Join(directory, "missing")); err == nil {
		t.Fatal("fileDigest(missing) error = nil")
	}
	if _, err := fileDigest(context.Background(), directory); err == nil {
		t.Fatal("fileDigest(directory) error = nil")
	}

	invalidTarget := DefaultConfig()
	invalidTarget.MaxWidth = 0
	if _, _, err := OptimizeFile(path, invalidTarget, nil); err == nil {
		t.Fatal("OptimizeFile(invalid target) error = nil")
	}
	unsafeMemory := DefaultConfig()
	unsafeMemory.MaxWidth, unsafeMemory.MaxHeight = 8, 6
	unsafeMemory.MaxWorkingBytes = 1
	if _, _, err := OptimizeFile(path, unsafeMemory, nil); err == nil {
		t.Fatal("OptimizeFile(unsafe memory) error = nil")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := validateOptimizedPixels(context.Background(), file, 1, 1); err == nil {
		t.Fatal("validateOptimizedPixels(wrong dimensions) error = nil")
	}
}

func TestStageCopyRejectsUnstableAndInvalidInputs(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	missing := StageInput{Name: "missing.jpg", Path: filepath.Join(directory, "missing.jpg")}
	if _, err := openStageInput(missing); err == nil {
		t.Fatal("openStageInput(missing) error = nil")
	}
	if _, err := openStageInput(StageInput{Name: "directory.jpg", Path: directory}); err == nil {
		t.Fatal("openStageInput(directory) error = nil")
	}

	sourcePath := filepath.Join(directory, "source.jpg")
	if err := os.WriteFile(sourcePath, []byte("source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := StageInput{Name: "source.jpg", Path: sourcePath, Digest: sha256.Sum256([]byte("source bytes"))}
	openSource := func(t *testing.T) stageSource {
		t.Helper()
		source, err := openStageInput(input)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = source.file.Close() })
		return source
	}

	t.Run("destination exists", func(t *testing.T) {
		stageDirectory := t.TempDir()
		if err := os.WriteFile(filepath.Join(stageDirectory, input.Name), []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeStageInput(context.Background(), stageDirectory, input, openSource(t)); err == nil {
			t.Fatal("writeStageInput(occupied) error = nil")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := writeStageInput(ctx, t.TempDir(), input, openSource(t)); err == nil {
			t.Fatal("writeStageInput(canceled) error = nil")
		}
	})
	t.Run("digest changed", func(t *testing.T) {
		wrong := input
		wrong.Digest = sha256.Sum256([]byte("different"))
		if err := writeStageInput(context.Background(), t.TempDir(), wrong, openSource(t)); err == nil {
			t.Fatal("writeStageInput(wrong digest) error = nil")
		}
	})
	t.Run("path removed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.jpg")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		candidate := StageInput{Name: "source.jpg", Path: path}
		source, err := openStageInput(candidate)
		if err != nil {
			t.Fatal(err)
		}
		defer source.file.Close()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := verifyStageInputStable(candidate, source.file, source.before, source.opened); err == nil {
			t.Fatal("verifyStageInputStable(removed) error = nil")
		}
	})
	t.Run("path replaced", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.jpg")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		candidate := StageInput{Name: "source.jpg", Path: path}
		source, err := openStageInput(candidate)
		if err != nil {
			t.Fatal(err)
		}
		defer source.file.Close()
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyStageInputStable(candidate, source.file, source.before, source.opened); err == nil {
			t.Fatal("verifyStageInputStable(replaced) error = nil")
		}
	})
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
