//nolint:revive // ICC metadata, matrix/TRC conversion, and HDR math form one cohesive color-management module
package optimize

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"log/slog"
	"math"
	"os"
)

const (
	maxEmbeddedProfileBytes = 4 << 20
	iccCurveIdentity        = "identity"
	iccCurveGamma           = "gamma"
	iccCurveTable           = "table"
	iccCurveParametric      = "parametric"
)

type hdrMetadata struct {
	primaries uint8
	transfer  uint8
}

type embeddedColorData struct {
	description string
	icc         []byte
	hdr         *hdrMetadata
}

func readColorDataWithPolicy(
	ctx context.Context,
	file *os.File,
	extension string,
	cfg Config,
	logger *slog.Logger,
	filename string,
) (embeddedColorData, error) {
	metadata, err := readEmbeddedColorData(ctx, file, extension)
	if err != nil {
		return embeddedColorData{}, err
	}
	if metadata.description != "" && cfg.ColorProfilePolicy == profileRejectEmbedded {
		return embeddedColorData{}, fmt.Errorf("unsupported embedded color metadata %s", metadata.description)
	}
	if metadata.description != "" && metadata.hdr == nil && cfg.ColorProfilePolicy == profileAssumeSRGB {
		logger.Warn(
			"embedded color metadata is not transformed; treating decoded samples as sRGB",
			"file", filename, "metadata", metadata.description,
		)
	}
	if metadata.description != "" && cfg.ColorProfilePolicy == profileConvertSRGB && len(metadata.icc) == 0 && metadata.hdr == nil {
		logger.Warn(
			"embedded color metadata is unsupported for conversion; assuming sRGB",
			"file", filename, "metadata", metadata.description,
		)
	}
	return metadata, nil
}

func readEmbeddedColorData(ctx context.Context, file *os.File, extension string) (embeddedColorData, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return embeddedColorData{}, fmt.Errorf("seek embedded color data: %w", err)
	}
	reader := &contextReader{ctx: ctx, reader: file}
	if extension == extPNG {
		return readPNGColorData(reader)
	}
	return readJPEGColorData(reader)
}

//nolint:funlen,gocognit,gocyclo // bounded marker parsing and ICC segment assembly form one streaming transaction
func readJPEGColorData(reader io.Reader) (embeddedColorData, error) {
	var signature [2]byte
	if _, err := io.ReadFull(reader, signature[:]); err != nil {
		return embeddedColorData{}, fmt.Errorf("read JPEG signature: %w", err)
	}
	if signature != [2]byte{0xff, 0xd8} {
		return embeddedColorData{}, errors.New("not a JPEG while reading embedded color data")
	}
	parts := make(map[int][]byte)
	total := 0
	for {
		marker, err := readJPEGMarker(reader)
		if err != nil {
			return embeddedColorData{}, err
		}
		if marker == 0xd9 || marker == 0xda {
			return assembleJPEGColorData(parts, total)
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		var lengthBytes [2]byte
		if _, err := io.ReadFull(reader, lengthBytes[:]); err != nil {
			return embeddedColorData{}, fmt.Errorf("read JPEG marker length: %w", err)
		}
		length := int(binary.BigEndian.Uint16(lengthBytes[:])) - 2
		if length < 0 {
			return embeddedColorData{}, errors.New("invalid JPEG marker length while reading embedded color data")
		}
		if marker != 0xe2 {
			if _, err := io.CopyN(io.Discard, reader, int64(length)); err != nil {
				return embeddedColorData{}, fmt.Errorf("skip JPEG marker: %w", err)
			}
			continue
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return embeddedColorData{}, fmt.Errorf("read JPEG ICC marker: %w", err)
		}
		if len(payload) < 14 || string(payload[:12]) != jpegICCSignature {
			continue
		}
		sequence, count := int(payload[12]), int(payload[13])
		if sequence < 1 || count < 1 || sequence > count {
			return embeddedColorData{}, errors.New("invalid JPEG ICC segment numbering")
		}
		if total != 0 && total != count {
			return embeddedColorData{}, errors.New("inconsistent JPEG ICC segment count")
		}
		if _, exists := parts[sequence]; exists {
			return embeddedColorData{}, errors.New("duplicate JPEG ICC segment")
		}
		total = count
		parts[sequence] = append([]byte(nil), payload[14:]...)
		profileBytes := 0
		for _, part := range parts {
			profileBytes += len(part)
		}
		if profileBytes > maxEmbeddedProfileBytes {
			return embeddedColorData{}, fmt.Errorf("JPEG ICC profile exceeds %d-byte limit", maxEmbeddedProfileBytes)
		}
	}
}

func readJPEGMarker(reader io.Reader) (byte, error) {
	var value [1]byte
	if _, err := io.ReadFull(reader, value[:]); err != nil {
		return 0, fmt.Errorf("read JPEG marker prefix: %w", err)
	}
	if value[0] != 0xff {
		return 0, errors.New("invalid JPEG marker while reading embedded color data")
	}
	for {
		if _, err := io.ReadFull(reader, value[:]); err != nil {
			return 0, fmt.Errorf("read JPEG marker: %w", err)
		}
		if value[0] != 0xff {
			if value[0] == 0 {
				return 0, errors.New("invalid stuffed JPEG marker outside scan data")
			}
			return value[0], nil
		}
	}
}

func assembleJPEGColorData(parts map[int][]byte, total int) (embeddedColorData, error) {
	if total == 0 {
		return embeddedColorData{}, nil
	}
	profile := make([]byte, 0)
	for sequence := 1; sequence <= total; sequence++ {
		part, ok := parts[sequence]
		if !ok {
			return embeddedColorData{}, errors.New("incomplete JPEG ICC profile")
		}
		profile = append(profile, part...)
	}
	return embeddedColorData{description: jpegICCMetadata, icc: profile}, nil
}

//nolint:funlen,gocognit,gocyclo // chunk framing, bounded ICC decompression, and metadata precedence stay local
func readPNGColorData(reader io.Reader) (embeddedColorData, error) {
	var signature [8]byte
	if _, err := io.ReadFull(reader, signature[:]); err != nil {
		return embeddedColorData{}, fmt.Errorf("read PNG signature: %w", err)
	}
	if signature != pngSignature {
		return embeddedColorData{}, errors.New("not a PNG while reading embedded color data")
	}
	result := embeddedColorData{}
	for {
		var header [8]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return embeddedColorData{}, fmt.Errorf("read PNG chunk header: %w", err)
		}
		length := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:])
		if kind == pngChunkImageData || kind == pngChunkEnd {
			return result, nil
		}
		switch kind {
		case pngChunkICC:
			if length > maxEmbeddedProfileBytes+256 {
				return embeddedColorData{}, fmt.Errorf("PNG iCCP chunk exceeds %d-byte compressed limit", maxEmbeddedProfileBytes+256)
			}
			payload := make([]byte, int(length))
			if _, err := io.ReadFull(reader, payload); err != nil {
				return embeddedColorData{}, fmt.Errorf("read PNG iCCP chunk: %w", err)
			}
			metadata, err := decodePNGICC(payload)
			if err != nil {
				return embeddedColorData{}, err
			}
			result.description, result.icc = metadata.description, metadata.icc
		case pngChunkCICP:
			if length != 4 {
				return embeddedColorData{}, errors.New("invalid PNG cICP chunk")
			}
			var payload [4]byte
			if _, err := io.ReadFull(reader, payload[:]); err != nil {
				return embeddedColorData{}, fmt.Errorf("read PNG cICP chunk: %w", err)
			}
			result.description = pngCICPMetadata
			if payload[0] == 9 && payload[2] == 0 && payload[3] == 1 && (payload[1] == 16 || payload[1] == 18) {
				result.description = "PNG cICP HDR"
				result.hdr = &hdrMetadata{primaries: payload[0], transfer: payload[1]}
			}
		default:
			if kind == pngChunkGamma || kind == pngChunkChromaticity {
				if result.description == "" {
					result.description = "PNG " + kind
				}
			}
			if _, err := io.CopyN(io.Discard, reader, length); err != nil {
				return embeddedColorData{}, fmt.Errorf("skip PNG %s chunk: %w", kind, err)
			}
		}
		if _, err := io.CopyN(io.Discard, reader, 4); err != nil {
			return embeddedColorData{}, fmt.Errorf("read PNG %s CRC: %w", kind, err)
		}
	}
}

func decodePNGICC(payload []byte) (embeddedColorData, error) {
	separator := bytes.IndexByte(payload, 0)
	if separator < 1 || separator+2 > len(payload) || payload[separator+1] != 0 {
		return embeddedColorData{}, errors.New("invalid PNG iCCP chunk")
	}
	reader, err := zlib.NewReader(bytes.NewReader(payload[separator+2:]))
	if err != nil {
		return embeddedColorData{}, fmt.Errorf("open PNG ICC profile: %w", err)
	}
	profile, readErr := io.ReadAll(io.LimitReader(reader, maxEmbeddedProfileBytes+1))
	closeErr := reader.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return embeddedColorData{}, fmt.Errorf("read PNG ICC profile: %w", err)
	}
	if len(profile) > maxEmbeddedProfileBytes {
		return embeddedColorData{}, fmt.Errorf("PNG ICC profile exceeds %d-byte limit", maxEmbeddedProfileBytes)
	}
	return embeddedColorData{description: pngICCMetadata, icc: profile}, nil
}

func extractJPEGColorData(data []byte) (embeddedColorData, error) {
	return readJPEGColorData(bytes.NewReader(data))
}

func extractPNGColorData(data []byte) (embeddedColorData, error) {
	return readPNGColorData(bytes.NewReader(data))
}

type iccCurve struct {
	kind   string
	values []float64
	params []float64
}

type matrixProfile struct {
	columns [3][3]float64
	curves  [3]iccCurve
}

//nolint:gocognit,gocyclo // tag-table bounds and required matrix/TRC tags are validated in one pass
func parseMatrixProfile(data []byte) (matrixProfile, error) {
	if len(data) < 132 {
		return matrixProfile{}, errors.New("ICC profile header is truncated")
	}
	if err := validateICCHeader(data); err != nil {
		return matrixProfile{}, err
	}
	if string(data[16:20]) != "RGB " || string(data[20:24]) != "XYZ " {
		return matrixProfile{}, errors.New("ICC profile is not an RGB matrix profile with XYZ connection space")
	}
	declared := int(binary.BigEndian.Uint32(data[:4]))
	if declared < 132 || declared > len(data) {
		return matrixProfile{}, errors.New("ICC profile length is invalid")
	}
	data = data[:declared]
	count := int(binary.BigEndian.Uint32(data[128:132]))
	if count > 256 || 132+count*12 > len(data) {
		return matrixProfile{}, errors.New("ICC tag table is invalid")
	}
	tags := make(map[string][]byte, count)
	for index := 0; index < count; index++ {
		entry := data[132+index*12 : 144+index*12]
		offset, size := int(binary.BigEndian.Uint32(entry[4:8])), int(binary.BigEndian.Uint32(entry[8:12]))
		if offset < 0 || size < 0 || offset+size > len(data) {
			return matrixProfile{}, errors.New("ICC tag range is invalid")
		}
		tags[string(entry[:4])] = data[offset : offset+size]
	}
	var profile matrixProfile
	for channel, signature := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		value, err := parseICCXYZ(tags[signature])
		if err != nil {
			return matrixProfile{}, fmt.Errorf("parse ICC %s: %w", signature, err)
		}
		for row := range 3 {
			profile.columns[channel][row] = value[row]
		}
	}
	for channel, signature := range []string{"rTRC", "gTRC", "bTRC"} {
		curve, err := parseICCCurve(tags[signature])
		if err != nil {
			return matrixProfile{}, fmt.Errorf("parse ICC %s: %w", signature, err)
		}
		profile.curves[channel] = curve
	}
	return profile, nil
}

func validateICCHeader(data []byte) error {
	if string(data[36:40]) != "acsp" {
		return errors.New("ICC profile signature is invalid")
	}
	if major := data[8]; major != 2 && major != 4 {
		return fmt.Errorf("ICC profile version %d is unsupported", major)
	}
	illuminant := [3]float64{iccFixed(data[68:72]), iccFixed(data[72:76]), iccFixed(data[76:80])}
	d50 := [3]float64{0.9642, 1, 0.8249}
	for index := range illuminant {
		if math.Abs(illuminant[index]-d50[index]) > 0.001 {
			return errors.New("ICC profile PCS illuminant is not D50")
		}
	}
	return nil
}

func parseICCXYZ(data []byte) ([3]float64, error) {
	if len(data) < 20 || string(data[:4]) != "XYZ " {
		return [3]float64{}, errors.New("tag is not XYZType")
	}
	return [3]float64{iccFixed(data[8:12]), iccFixed(data[12:16]), iccFixed(data[16:20])}, nil
}

//nolint:gocognit // ICC curve types deliberately mirror the standard's cases
func parseICCCurve(data []byte) (iccCurve, error) {
	if len(data) < 12 {
		return iccCurve{}, errors.New("curve tag is truncated")
	}
	switch string(data[:4]) {
	case "curv":
		count := int(binary.BigEndian.Uint32(data[8:12]))
		if count == 0 {
			return iccCurve{kind: iccCurveIdentity}, nil
		}
		if 12+count*2 > len(data) {
			return iccCurve{}, errors.New("curveType values are truncated")
		}
		values := make([]float64, count)
		for index := range count {
			values[index] = float64(binary.BigEndian.Uint16(data[12+index*2:14+index*2])) / 65535
		}
		if count == 1 {
			return iccCurve{kind: iccCurveGamma, params: []float64{float64(binary.BigEndian.Uint16(data[12:14])) / 256}}, nil
		}
		return iccCurve{kind: iccCurveTable, values: values}, nil
	case "para":
		function := int(binary.BigEndian.Uint16(data[8:10]))
		parameterCounts := []int{1, 3, 4, 5, 7}
		if function < 0 || function >= len(parameterCounts) {
			return iccCurve{}, errors.New("unsupported parametricCurveType function")
		}
		count := parameterCounts[function]
		if 12+count*4 > len(data) {
			return iccCurve{}, errors.New("parametricCurveType values are truncated")
		}
		params := make([]float64, count+1)
		params[0] = float64(function)
		for index := range count {
			params[index+1] = iccFixed(data[12+index*4 : 16+index*4])
		}
		return iccCurve{kind: iccCurveParametric, params: params}, nil
	default:
		return iccCurve{}, errors.New("unsupported ICC tone response curve type")
	}
}

func iccFixed(data []byte) float64 {
	raw := binary.BigEndian.Uint32(data)
	signed := int64(raw)
	if raw&0x80000000 != 0 {
		signed -= 1 << 32
	}
	return float64(signed) / 65536
}

func (curve iccCurve) evaluate(value float64) float64 {
	value = clampUnit(value)
	switch curve.kind {
	case iccCurveIdentity:
		return value
	case iccCurveGamma:
		return math.Pow(value, curve.params[0])
	case iccCurveTable:
		position := value * float64(len(curve.values)-1)
		left := int(math.Floor(position))
		right := min(left+1, len(curve.values)-1)
		return curve.values[left] + (curve.values[right]-curve.values[left])*(position-float64(left))
	case iccCurveParametric:
		return evaluateParametricCurve(value, int(curve.params[0]), curve.params[1:])
	default:
		return value
	}
}

func evaluateParametricCurve(x float64, function int, p []float64) float64 {
	g := p[0]
	switch function {
	case 0:
		return math.Pow(x, g)
	case 1:
		a, b := p[1], p[2]
		if x >= -b/a {
			return math.Pow(a*x+b, g)
		}
		return 0
	case 2:
		a, b, c := p[1], p[2], p[3]
		if x >= -b/a {
			return math.Pow(a*x+b, g) + c
		}
		return c
	case 3:
		a, b, c, d := p[1], p[2], p[3], p[4]
		if x >= d {
			return math.Pow(a*x+b, g)
		}
		return c * x
	case 4:
		a, b, c, d, e, f := p[1], p[2], p[3], p[4], p[5], p[6]
		if x >= d {
			return math.Pow(a*x+b, g) + e
		}
		return c*x + f
	default:
		return x
	}
}

func convertICCToSRGB(src image.Image, profileData []byte) (*image.RGBA, error) {
	profile, err := parseMatrixProfile(profileData)
	if err != nil {
		return nil, err
	}
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			r16, g16, b16, a16 := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			if a16 == 0 {
				dst.SetRGBA(x, y, color.RGBA{})
				continue
			}
			device := [3]float64{
				profile.curves[0].evaluate(float64(r16) / float64(a16)),
				profile.curves[1].evaluate(float64(g16) / float64(a16)),
				profile.curves[2].evaluate(float64(b16) / float64(a16)),
			}
			xyz50 := [3]float64{}
			for channel := range 3 {
				for row := range 3 {
					xyz50[row] += profile.columns[channel][row] * device[channel]
				}
			}
			xyz65 := d50ToD65(xyz50)
			linear := [3]float64{
				3.2404542*xyz65[0] - 1.5371385*xyz65[1] - 0.4985314*xyz65[2],
				-0.969266*xyz65[0] + 1.8760108*xyz65[1] + 0.041556*xyz65[2],
				0.0556434*xyz65[0] - 0.2040259*xyz65[1] + 1.0572252*xyz65[2],
			}
			dst.SetRGBA(x, y, rgbaFromFloat(linear, clampByte(float64(a16)/257)))
		}
	}
	return dst, nil
}

func d50ToD65(v [3]float64) [3]float64 {
	return [3]float64{
		0.9555766*v[0] - 0.0230393*v[1] + 0.0631636*v[2],
		-0.0282895*v[0] + 1.0099416*v[1] + 0.0210077*v[2],
		0.0122982*v[0] - 0.020483*v[1] + 1.3299098*v[2],
	}
}

func normalizeColor(src image.Image, metadata embeddedColorData, cfg Config, logger *slog.Logger, filename string) *image.RGBA {
	if metadata.hdr != nil && cfg.HDRToneMap {
		return toneMapHDRToSDR(src, *metadata.hdr, cfg.HDRSourcePeakNits, cfg.HDRTargetPeakNits)
	}
	var rgba *image.RGBA
	if len(metadata.icc) > 0 && cfg.ColorProfilePolicy == profileConvertSRGB {
		converted, err := convertICCToSRGB(src, metadata.icc)
		if err == nil {
			rgba = converted
		} else {
			logger.Warn("embedded ICC profile conversion failed; assuming sRGB", "file", filename, "error", err)
		}
	}
	if rgba == nil {
		rgba = toRGBA(src)
	}
	return rgba
}

func toneMapHDRToSDR(src image.Image, metadata hdrMetadata, sourcePeak, targetPeak float64) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := color.NRGBA64Model.Convert(src.At(x, y)).(color.NRGBA64)
			encoded := [3]float64{
				float64(pixel.R) / 65535,
				float64(pixel.G) / 65535,
				float64(pixel.B) / 65535,
			}
			linear := [3]float64{}
			for channel := range 3 {
				if metadata.transfer == 16 {
					linear[channel] = pqEOTF(encoded[channel]) * 10_000 / sourcePeak
				} else {
					linear[channel] = hlgEOTF(encoded[channel])
				}
			}
			mapped := bt2446MethodA(linear, sourcePeak, targetPeak)
			if metadata.primaries == 9 {
				mapped = bt2020ToBT709(mapped)
			}
			dst.SetRGBA(x-bounds.Min.X, y-bounds.Min.Y, rgbaFromFloat(mapped, uint8(pixel.A>>8)))
		}
	}
	return dst
}

func bt2446MethodA(linear [3]float64, sourcePeak, targetPeak float64) [3]float64 {
	gammaRGB := [3]float64{math.Pow(max(linear[0], 0), 1/2.4), math.Pow(max(linear[1], 0), 1/2.4), math.Pow(max(linear[2], 0), 1/2.4)}
	luma := 0.2627*gammaRGB[0] + 0.6780*gammaRGB[1] + 0.0593*gammaRGB[2]
	mappedLuma := bt2446ToneMapLuma(luma, sourcePeak, targetPeak)
	if luma <= 1e-9 {
		return [3]float64{}
	}
	scale := mappedLuma / luma
	return [3]float64{
		math.Pow(clampUnit(gammaRGB[0]*scale), 2.4),
		math.Pow(clampUnit(gammaRGB[1]*scale), 2.4),
		math.Pow(clampUnit(gammaRGB[2]*scale), 2.4),
	}
}

func bt2446ToneMapLuma(luma, sourcePeak, targetPeak float64) float64 {
	rhoHDR := 1 + 32*math.Pow(sourcePeak/10_000, 1/2.4)
	perceptual := math.Log(1+(rhoHDR-1)*clampUnit(luma)) / math.Log(rhoHDR)
	var compressed float64
	switch {
	case perceptual <= 0.7399:
		compressed = 1.0770 * perceptual
	case perceptual < 0.9909:
		compressed = -1.1510*perceptual*perceptual + 2.7811*perceptual - 0.6302
	default:
		compressed = 0.5*perceptual + 0.5
	}
	rhoSDR := 1 + 32*math.Pow(targetPeak/10_000, 1/2.4)
	return (math.Pow(rhoSDR, compressed) - 1) / (rhoSDR - 1)
}

func pqEOTF(value float64) float64 {
	const m1, m2 = 2610.0 / 16384, 2523.0 * 128 / 4096
	const c1, c2, c3 = 3424.0 / 4096, 2413.0 * 32 / 4096, 2392.0 * 32 / 4096
	power := math.Pow(clampUnit(value), 1/m2)
	return math.Pow(max(power-c1, 0)/(c2-c3*power), 1/m1)
}

func hlgEOTF(value float64) float64 {
	const a, b, c = 0.17883277, 0.28466892, 0.55991073
	var scene float64
	if value <= 0.5 {
		scene = value * value / 3
	} else {
		scene = (math.Exp((value-c)/a) + b) / 12
	}
	return math.Pow(scene, 1.2)
}

func bt2020ToBT709(v [3]float64) [3]float64 {
	return [3]float64{
		1.660491*v[0] - 0.587641*v[1] - 0.07285*v[2],
		-0.12455*v[0] + 1.1329*v[1] - 0.008349*v[2],
		-0.018151*v[0] - 0.100579*v[1] + 1.11873*v[2],
	}
}

func rgbaFromFloat(linear [3]float64, alpha uint8) color.RGBA {
	return color.RGBA{
		R: uint8(math.Round(clampUnit(srgbEncode(linear[0])) * 255)),
		G: uint8(math.Round(clampUnit(srgbEncode(linear[1])) * 255)),
		B: uint8(math.Round(clampUnit(srgbEncode(linear[2])) * 255)),
		A: alpha,
	}
}

func clampUnit(value float64) float64 { return math.Min(math.Max(value, 0), 1) }
