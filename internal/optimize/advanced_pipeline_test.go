package optimize

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOptimizeFileConvertsEmbeddedMatrixICCProfileToSRGB(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "profiled.jpg")
	source := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := range 20 {
		for x := range 20 {
			source.SetRGBA(x, y, color.RGBA{R: 245, G: 10, B: 10, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, source, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	profile := testMatrixICCProfile(true)
	payload := append([]byte("ICC_PROFILE\x00\x01\x01"), profile...)
	segment := make([]byte, 4+len(payload))
	segment[0], segment[1] = 0xff, 0xe2
	binary.BigEndian.PutUint16(segment[2:4], uint16(len(payload)+2))
	copy(segment[4:], payload)
	jpegWithProfile := append(append(append([]byte{}, encoded.Bytes()[:2]...), segment...), encoded.Bytes()[2:]...)
	if err := os.WriteFile(path, jpegWithProfile, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight = 20, 20
	cfg.SharpenAmount, cfg.HDRToneMap = 0, false
	name, modified, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile() error = %v", err)
	}
	if !modified {
		t.Fatal("OptimizeFile() modified = false, want ICC conversion")
	}
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	output, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := output.At(10, 10).RGBA()
	if b <= r+10_000 {
		t.Fatalf("converted center RGB16 = (%d, _, %d), want red profile channel mapped to blue", r, b)
	}
}

func TestBT2446MethodAReferenceLuma(t *testing.T) {
	t.Parallel()
	// ITU-R BT.2446-1 Table 2 equations with 1000-nit HDR and 100-nit SDR peaks.
	if got := bt2446ToneMapLuma(0.5, 1000, 100); math.Abs(got-0.6716731181085088) > 1e-12 {
		t.Fatalf("bt2446ToneMapLuma() = %.15f, want reference %.15f", got, 0.6716731181085088)
	}
}

func TestICCToneResponseCurveVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		curve       iccCurve
		input, want float64
	}{
		{"identity", iccCurve{kind: iccCurveIdentity}, 0.4, 0.4},
		{"gamma", iccCurve{kind: iccCurveGamma, params: []float64{2}}, 0.5, 0.25},
		{"table", iccCurve{kind: iccCurveTable, values: []float64{0, 0.2, 1}}, 0.75, 0.6},
		{"parametric-0", iccCurve{kind: iccCurveParametric, params: []float64{0, 2}}, 0.5, 0.25},
		{"parametric-1-low", iccCurve{kind: iccCurveParametric, params: []float64{1, 2, 1, -0.5}}, 0.25, 0},
		{"parametric-1-high", iccCurve{kind: iccCurveParametric, params: []float64{1, 2, 1, -0.5}}, 0.75, 0.0625},
		{"parametric-2-high", iccCurve{kind: iccCurveParametric, params: []float64{2, 2, 1, 0, 0.1}}, 0.5, 0.35},
		{"parametric-2-low", iccCurve{kind: iccCurveParametric, params: []float64{2, 2, 1, -0.5, 0.1}}, 0.25, 0.1},
		{"parametric-3-low", iccCurve{kind: iccCurveParametric, params: []float64{3, 2, 1, 0, 0.5, 0.4}}, 0.2, 0.1},
		{"parametric-3-high", iccCurve{kind: iccCurveParametric, params: []float64{3, 2, 1, 0, 0.5, 0.4}}, 0.5, 0.25},
		{"parametric-4-high", iccCurve{kind: iccCurveParametric, params: []float64{4, 2, 1, 0, 0.5, 0.4, 0.1, 0.05}}, 0.5, 0.35},
		{"parametric-4-low", iccCurve{kind: iccCurveParametric, params: []float64{4, 2, 1, 0, 0.5, 0.4, 0.1, 0.05}}, 0.2, 0.15},
		{"unknown", iccCurve{kind: "unknown"}, 0.4, 0.4},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.curve.evaluate(testCase.input); math.Abs(got-testCase.want) > 1e-12 {
				t.Fatalf("evaluate() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestColorNormalizationFallbackAndTransparentPixel(t *testing.T) {
	t.Parallel()
	source := image.NewRGBA(image.Rect(0, 0, 1, 1))
	cfg := DefaultConfig()
	got := normalizeColor(source, embeddedColorData{description: "broken", icc: []byte("bad")}, cfg, slog.New(slog.DiscardHandler), "broken.png")
	if got.Bounds() != source.Bounds() {
		t.Fatalf("fallback bounds = %v", got.Bounds())
	}
	converted, err := convertICCToSRGB(source, testMatrixICCProfile(false))
	if err != nil {
		t.Fatal(err)
	}
	if converted.RGBAAt(0, 0).A != 0 {
		t.Fatalf("transparent alpha = %d", converted.RGBAAt(0, 0).A)
	}
	if got := bt2446MethodA([3]float64{}, 1000, 100); got != [3]float64{} {
		t.Fatalf("black tone map = %v", got)
	}
	_ = normalizeColor(source, embeddedColorData{description: "PNG gAMA"}, cfg, slog.New(slog.DiscardHandler), "gamma.png")
	if hlgEOTF(0.25) <= 0 {
		t.Fatal("HLG low branch returned non-positive light")
	}
	pngMetadata := appendPNGColorChunk(pngSignature[:], pngChunkGamma, []byte{0, 0, 0xb1, 0x8f})
	pngMetadata = appendPNGColorChunk(pngMetadata, pngChunkEnd, nil)
	metadata, err := extractPNGColorData(pngMetadata)
	if err != nil || metadata.description != "PNG gAMA" {
		t.Fatalf("PNG gAMA metadata = %+v, %v", metadata, err)
	}
}

func TestParseICCCurveStandardEncodingsAndFailures(t *testing.T) {
	t.Parallel()
	curveData := func(kind string, payload []byte) []byte {
		data := make([]byte, 12+len(payload))
		copy(data, kind)
		copy(data[12:], payload)
		return data
	}
	identity := curveData("curv", nil)
	curve, err := parseICCCurve(identity)
	if err != nil || curve.kind != iccCurveIdentity {
		t.Fatalf("identity = %+v, %v", curve, err)
	}
	tablePayload := make([]byte, 4)
	binary.BigEndian.PutUint16(tablePayload[:2], 0)
	binary.BigEndian.PutUint16(tablePayload[2:], 65535)
	table := curveData("curv", tablePayload)
	binary.BigEndian.PutUint32(table[8:12], 2)
	curve, err = parseICCCurve(table)
	if err != nil || math.Abs(curve.evaluate(0.25)-0.25) > 1e-12 {
		t.Fatalf("table = %+v, %v", curve, err)
	}
	parametric := make([]byte, 16)
	copy(parametric, "para")
	binary.BigEndian.PutUint16(parametric[8:10], 0)
	binary.BigEndian.PutUint32(parametric[12:16], uint32(2*65536))
	curve, err = parseICCCurve(parametric)
	if err != nil || math.Abs(curve.evaluate(0.5)-0.25) > 1e-12 {
		t.Fatalf("parametric = %+v, %v", curve, err)
	}
	for name, data := range map[string][]byte{
		"short":   make([]byte, 4),
		"unknown": curveData("nope", nil),
		"curve-truncated": func() []byte {
			value := curveData("curv", nil)
			binary.BigEndian.PutUint32(value[8:12], 2)
			return value
		}(),
		"parametric-function": func() []byte {
			value := curveData("para", nil)
			binary.BigEndian.PutUint16(value[8:10], 9)
			return value
		}(),
		"parametric-truncated": func() []byte {
			value := curveData("para", nil)
			binary.BigEndian.PutUint16(value[8:10], 4)
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseICCCurve(data); err == nil {
				t.Fatal("parseICCCurve() error = nil")
			}
		})
	}
}

func TestICCProfileParserRejectsInvalidStructures(t *testing.T) {
	t.Parallel()
	if _, err := parseICCXYZ([]byte("bad")); err == nil {
		t.Fatal("parseICCXYZ() error = nil")
	}
	for name, profile := range map[string][]byte{
		"short":   make([]byte, 20),
		"not-rgb": make([]byte, 132),
		"bad-length": func() []byte {
			data := make([]byte, 132)
			copy(data[16:20], "RGB ")
			copy(data[20:24], "XYZ ")
			binary.BigEndian.PutUint32(data[:4], 1000)
			return data
		}(),
		"bad-table": func() []byte {
			data := make([]byte, 132)
			copy(data[16:20], "RGB ")
			copy(data[20:24], "XYZ ")
			binary.BigEndian.PutUint32(data[:4], 132)
			binary.BigEndian.PutUint32(data[128:132], 300)
			return data
		}(),
		"bad-signature": func() []byte {
			data := testMatrixICCProfile(false)
			data[36] = 0
			return data
		}(),
		"unsupported-version": func() []byte {
			data := testMatrixICCProfile(false)
			data[8] = 3
			return data
		}(),
		"non-D50-illuminant": func() []byte {
			data := testMatrixICCProfile(false)
			clear(data[68:80])
			return data
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMatrixProfile(profile); err == nil {
				t.Fatal("parseMatrixProfile() error = nil")
			}
		})
	}
}

func TestPNGICCExtractionAndConversion(t *testing.T) {
	t.Parallel()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			img.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(testMatrixICCProfile(true)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("test-profile\x00\x00"), compressed.Bytes()...)
	const afterIHDR = 33
	profiled := append([]byte{}, raw.Bytes()[:afterIHDR]...)
	profiled = appendPNGColorChunk(profiled, pngChunkICC, payload)
	profiled = append(profiled, raw.Bytes()[afterIHDR:]...)
	metadata, err := extractPNGColorData(profiled)
	if err != nil {
		t.Fatalf("extractPNGColorData() error = %v", err)
	}
	if metadata.description != "PNG iCCP" || len(metadata.icc) == 0 {
		t.Fatalf("metadata = %+v", metadata)
	}
	converted, err := convertICCToSRGB(img, metadata.icc)
	if err != nil {
		t.Fatal(err)
	}
	pixel := converted.RGBAAt(0, 0)
	if pixel.B <= pixel.R {
		t.Fatalf("converted pixel = %+v, want blue-dominant", pixel)
	}
}

func TestEmbeddedColorMetadataRejectsMalformedChunks(t *testing.T) {
	t.Parallel()
	tests := [][]byte{
		[]byte("not png"),
		appendPNGColorChunk(pngSignature[:], pngChunkICC, []byte("bad")),
		appendPNGColorChunk(pngSignature[:], "cICP", []byte{9, 16}),
	}
	for index, data := range tests {
		if _, err := extractPNGColorData(data); err == nil {
			t.Errorf("case %d error = nil", index)
		}
	}
	if _, err := extractJPEGColorData([]byte("bad")); err == nil {
		t.Fatal("extractJPEGColorData() malformed error = nil")
	}
}

func TestEmbeddedColorMetadataStreamsOnlyThroughImageData(t *testing.T) {
	t.Parallel()
	data := appendPNGColorChunk(pngSignature[:], pngChunkGamma, []byte{0, 0, 0xb1, 0x8f})
	data = append(data, 0, 0, 0x10, 0, 'I', 'D', 'A', 'T')
	path := filepath.Join(t.TempDir(), "metadata-only.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	metadata, err := readEmbeddedColorData(context.Background(), file, extPNG)
	if err != nil {
		t.Fatalf("readEmbeddedColorData() error = %v", err)
	}
	if metadata.description != "PNG gAMA" {
		t.Fatalf("metadata = %+v, want gAMA without reading IDAT payload", metadata)
	}
}

func TestStreamingJPEGMetadataAcceptsFillAndStandaloneMarkers(t *testing.T) {
	t.Parallel()
	metadata, err := readJPEGColorData(bytes.NewReader([]byte{0xff, 0xd8, 0xff, 0xff, 0x01, 0xff, 0xda}))
	if err != nil {
		t.Fatalf("readJPEGColorData() error = %v", err)
	}
	if metadata.description != "" {
		t.Fatalf("metadata = %+v, want empty", metadata)
	}
}

func TestPNGColorDataDoesNotToneMapUnsupportedHDRPrimaries(t *testing.T) {
	t.Parallel()
	data := appendPNGColorChunk(pngSignature[:], "cICP", []byte{12, 16, 0, 1})
	data = appendPNGColorChunk(data, pngChunkEnd, nil)
	metadata, err := extractPNGColorData(data)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.description != "PNG cICP" || metadata.hdr != nil {
		t.Fatalf("metadata = %+v, want unsupported non-HDR fallback", metadata)
	}
}

func TestHLGToneMapAndAdvancedCropValidationBranches(t *testing.T) {
	t.Parallel()
	source := image.NewRGBA(image.Rect(0, 0, 1, 1))
	source.SetRGBA(0, 0, color.RGBA{R: 200, G: 180, B: 160, A: 255})
	output := toneMapHDRToSDR(source, hdrMetadata{primaries: 9, transfer: 18}, 1000, 100)
	if output.RGBAAt(0, 0) == source.RGBAAt(0, 0) {
		t.Fatal("HLG tone map left pixel unchanged")
	}
	bounds := image.Rect(0, 0, 200, 100)
	for _, proposal := range []cropProposal{
		{X: -1, Width: 100, Height: 100, Confidence: 0.8},
		{Width: 100, Height: 100, Confidence: 2},
		{Width: 90, Height: 100, Confidence: 0.8},
	} {
		if err := validateCropProposal(proposal, bounds, 1); err == nil {
			t.Errorf("validateCropProposal(%+v) error = nil", proposal)
		}
	}
	if err := validateCropProposal(cropProposal{Width: 100, Height: 100, Confidence: 0.8}, bounds, 1); err != nil {
		t.Fatalf("valid crop error = %v", err)
	}
	if got := resizePreview(image.NewRGBA(image.Rect(0, 0, 100, 200)), 50).Bounds(); got.Dx() != 25 || got.Dy() != 50 {
		t.Fatalf("portrait preview = %v", got)
	}
	if got := resizePreview(image.NewRGBA(image.Rect(0, 0, 200, 100)), 50).Bounds(); got.Dx() != 50 || got.Dy() != 25 {
		t.Fatalf("landscape preview = %v", got)
	}
}

func TestToneMapUsesSixteenBitHDRSamplesBeforeQuantizing(t *testing.T) {
	t.Parallel()
	const samples = 1 << 16
	source := image.NewNRGBA64(image.Rect(0, 0, samples, 1))
	for value := range samples {
		sample := uint16(value)
		source.SetNRGBA64(value, 0, color.NRGBA64{R: sample, G: sample, B: sample, A: 0xffff})
	}
	output := toneMapHDRToSDR(source, hdrMetadata{primaries: 9, transfer: 16}, 1000, 100)
	for value := 1; value < samples; value++ {
		if value>>8 != (value-1)>>8 {
			continue
		}
		if output.RGBAAt(value, 0) != output.RGBAAt(value-1, 0) {
			return
		}
	}
	t.Fatal("tone-mapped output never distinguished 16-bit samples within an 8-bit input code")
}

func TestAdvancedCropProviderRejectsBadResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"status", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusBadGateway) }},
		{"json", func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte("not-json")) }},
		{"trailing-json", func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"x":0,"y":0,"width":100,"height":100,"confidence":0.9} {}`))
		}},
		{"oversized", func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write(bytes.Repeat([]byte(" "), 4097))
		}},
		{"geometry", func(writer http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(writer).Encode(cropProposal{Width: 80, Height: 100, Confidence: 0.9})
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(testCase.handler)
			defer server.Close()
			cfg := DefaultConfig()
			cfg.SmartCropProviderURL = server.URL
			cfg.SmartCropProviderTimeout = time.Second
			if _, err := requestAdvancedCrop(context.Background(), image.NewRGBA(image.Rect(0, 0, 200, 100)), 100, 100, cfg); err == nil {
				t.Fatal("requestAdvancedCrop() error = nil")
			}
		})
	}
}

func TestOptimizeFileToneMapsMetadataDeclaredHDRPNG(t *testing.T) {
	t.Parallel()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			img.SetRGBA(x, y, color.RGBA{R: 192, G: 192, B: 192, A: 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatal(err)
	}
	const afterIHDR = 33
	withHDR := append([]byte{}, raw.Bytes()[:afterIHDR]...)
	withHDR = appendPNGColorChunk(withHDR, "cICP", []byte{9, 16, 0, 1})
	withHDR = append(withHDR, raw.Bytes()[afterIHDR:]...)
	directory := t.TempDir()
	path := filepath.Join(directory, "hdr.png")
	if err := os.WriteFile(path, withHDR, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight, cfg.SharpenAmount = 2, 2, 0
	name, modified, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile() error = %v", err)
	}
	if !modified {
		t.Fatal("OptimizeFile() modified = false, want HDR normalization")
	}
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	output, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := output.At(0, 0).RGBA()
	if uint8(r>>8) == 192 {
		t.Fatalf("tone-mapped pixel remained %d", r>>8)
	}
}

func TestOptimizeFileUsesConfidentHTTPCrop(t *testing.T) {
	t.Parallel()
	source := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := range 100 {
		for x := range 200 {
			if x < 100 {
				source.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
			} else {
				source.SetRGBA(x, y, color.RGBA{B: 255, A: 255})
			}
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "image/jpeg" {
			t.Errorf("request = %s %s", request.Method, request.Header.Get("Content-Type"))
		}
		if request.URL.Query().Get("target_width") != "100" {
			t.Errorf("target_width = %q", request.URL.Query().Get("target_width"))
		}
		_ = json.NewEncoder(writer).Encode(cropProposal{X: 0, Y: 0, Width: 100, Height: 100, Confidence: 0.95})
	}))
	defer server.Close()

	directory := t.TempDir()
	path := filepath.Join(directory, "wide.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, source); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxWidth, cfg.MaxHeight, cfg.SmartCropEnabled = 100, 100, true
	cfg.SmartCropProvider, cfg.SmartCropProviderURL = "http", server.URL
	cfg.SmartCropProviderTimeout, cfg.SharpenAmount = 2*time.Second, 0
	name, _, err := OptimizeFile(path, cfg, slog.Default())
	if err != nil {
		t.Fatalf("OptimizeFile() error = %v", err)
	}
	outputFile, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	defer outputFile.Close()
	output, _, err := image.Decode(outputFile)
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := output.At(50, 50).RGBA()
	if r <= b {
		t.Fatalf("provider-selected crop center = red %d blue %d, want left/red crop", r, b)
	}
}

func TestAdvancedCropFailureFallsBackToLocalPolicy(t *testing.T) {
	t.Parallel()
	source := image.NewRGBA(image.Rect(0, 0, 200, 100))
	cfg := DefaultConfig()
	cfg.SmartCropEnabled = true
	cfg.SmartCropProvider = "http"
	cfg.SmartCropProviderURL = "http://127.0.0.1:1"
	cfg.SmartCropProviderTimeout = 100 * time.Millisecond
	rect := cropRectWithPolicy(context.Background(), source, 100, 100, cfg, slog.New(slog.DiscardHandler))
	if rect != image.Rect(50, 0, 150, 100) {
		t.Fatalf("fallback crop = %v, want centered local crop", rect)
	}
}

func TestProtectedCropPenalizesDetailedBoundary(t *testing.T) {
	t.Parallel()
	const width, height, window = 256, 128, 128
	saliency := make([]float64, width*height)
	protection := make([]float64, width*height)
	for index := range saliency {
		saliency[index] = 0.5
	}
	for y := range height {
		protection[y*width+64] = 1
	}
	saliencyIntegral := calculateIntegralImage(saliency, width, height)
	protectionIntegral := calculateIntegralImage(protection, width, height)
	safe := protectedCropWindowScore(saliencyIntegral, protectionIntegral, width, height, window, height, true, 0, 0.35)
	unsafe := protectedCropWindowScore(saliencyIntegral, protectionIntegral, width, height, window, height, true, 64, 0.35)
	if unsafe >= safe {
		t.Fatalf("unsafe boundary score %.3f >= safe score %.3f", unsafe, safe)
	}
}

func testMatrixICCProfile(swapRedBlue bool) []byte {
	type tag struct {
		signature string
		data      []byte
	}
	xyz := func(values [3]float64) []byte {
		data := make([]byte, 20)
		copy(data, "XYZ ")
		for index, value := range values {
			binary.BigEndian.PutUint32(data[8+index*4:12+index*4], uint32(int32(math.Round(value*65536))))
		}
		return data
	}
	curve := make([]byte, 16)
	copy(curve, "curv")
	binary.BigEndian.PutUint32(curve[8:12], 1)
	binary.BigEndian.PutUint16(curve[12:14], 256)
	red := [3]float64{0.4360747, 0.2225045, 0.0139322}
	green := [3]float64{0.3850649, 0.7168786, 0.0971045}
	blue := [3]float64{0.1430804, 0.0606169, 0.7141733}
	if swapRedBlue {
		red, blue = blue, red
	}
	tags := []tag{{"rXYZ", xyz(red)}, {"gXYZ", xyz(green)}, {"bXYZ", xyz(blue)}, {"rTRC", curve}, {"gTRC", curve}, {"bTRC", curve}}
	offset := 132 + 12*len(tags)
	profile := make([]byte, offset)
	profile[8], profile[9] = 4, 0x30
	copy(profile[36:40], "acsp")
	binary.BigEndian.PutUint32(profile[68:72], uint32(math.Round(0.9642*65536)))
	binary.BigEndian.PutUint32(profile[72:76], 1<<16)
	binary.BigEndian.PutUint32(profile[76:80], uint32(math.Round(0.8249*65536)))
	copy(profile[16:20], "RGB ")
	copy(profile[20:24], "XYZ ")
	binary.BigEndian.PutUint32(profile[128:132], uint32(len(tags)))
	for index, value := range tags {
		entry := 132 + index*12
		copy(profile[entry:entry+4], value.signature)
		binary.BigEndian.PutUint32(profile[entry+4:entry+8], uint32(offset))
		binary.BigEndian.PutUint32(profile[entry+8:entry+12], uint32(len(value.data)))
		profile = append(profile, value.data...)
		offset += len(value.data)
	}
	binary.BigEndian.PutUint32(profile[:4], uint32(len(profile)))
	return profile
}

func appendPNGColorChunk(data []byte, kind string, payload []byte) []byte {
	chunk := make([]byte, 12+len(payload))
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(payload)))
	copy(chunk[4:8], kind)
	copy(chunk[8:], payload)
	binary.BigEndian.PutUint32(chunk[8+len(payload):], crc32.ChecksumIEEE(chunk[4:8+len(payload)]))
	return append(data, chunk...)
}
