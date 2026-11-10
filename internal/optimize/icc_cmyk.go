package optimize

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
)

const (
	cmykChannels = 4
	pcsChannels  = 3
	// iccCurveSamples is the resolution post-lookup curves are tabulated at.
	iccCurveSamples = 4096

	pcsEncodingLegacyLab = iota
	pcsEncodingLab
	pcsEncodingXYZ
)

// d50White is the ICC profile connection space white.
//
//nolint:gochecknoglobals // immutable standard colorimetric constant
var d50White = [3]float64{0.9642, 1, 0.8249}

// cmykNode is one 8-bit ink value's place on a lookup-table axis: the byte
// offsets of the grid points on either side and how far it lies between them.
type cmykNode struct {
	lower, upper int
	fraction     float64
}

// cmykTransform maps CMYK ink values through a profile's device-to-connection
// lookup table to sRGB. Ink does not mix like light, so a CMYK profile carries
// a measured four-dimensional table and no formula reproduces it.
type cmykTransform struct {
	nodes     [cmykChannels][256]cmykNode
	table     []float64
	middle    [pcsChannels][]float64
	matrix    [pcsChannels][pcsChannels + 1]float64
	hasMatrix bool
	output    [pcsChannels][]float64
	encoding  int
	black     [3]float64
	toSRGB    [3][3]float64
	tables    *srgbTables
}

// parseICCTags validates a profile's header and returns its tags by signature.
func parseICCTags(data []byte) (map[string][]byte, error) {
	if len(data) < 132 {
		return nil, errors.New("ICC profile header is truncated")
	}
	if err := validateICCHeader(data); err != nil {
		return nil, err
	}
	declared := int(binary.BigEndian.Uint32(data[:4]))
	if declared < 132 || declared > len(data) {
		return nil, errors.New("ICC profile length is invalid")
	}
	data = data[:declared]
	count := int(binary.BigEndian.Uint32(data[128:132]))
	if count > 256 || 132+count*12 > len(data) {
		return nil, errors.New("ICC tag table is invalid")
	}
	tags := make(map[string][]byte, count)
	for index := range count {
		entry := data[132+index*12 : 144+index*12]
		offset, size := int(binary.BigEndian.Uint32(entry[4:8])), int(binary.BigEndian.Uint32(entry[8:12]))
		if offset < 0 || size < 0 || offset+size > len(data) {
			return nil, errors.New("ICC tag range is invalid")
		}
		tags[string(entry[:4])] = data[offset : offset+size]
	}
	return tags, nil
}

// newCMYKTransform builds the transform from a CMYK profile's perceptual
// device-to-connection table, or its colorimetric one when that is all the
// profile has.
func newCMYKTransform(data []byte) (*cmykTransform, error) {
	tags, err := parseICCTags(data)
	if err != nil {
		return nil, err
	}
	transform := &cmykTransform{tables: srgbLookup()}
	var lookup []byte
	for _, signature := range []string{"A2B0", "A2B1", "A2B2"} {
		if lookup = tags[signature]; lookup != nil {
			break
		}
	}
	if len(lookup) < 12 {
		return nil, errors.New("ICC CMYK profile has no device-to-connection lookup table")
	}
	lab := string(data[20:24]) == "Lab "
	if !lab && string(data[20:24]) != "XYZ " {
		return nil, fmt.Errorf("ICC connection space %q is unsupported", data[20:24])
	}
	var curves cmykLookupCurves
	switch kind := string(lookup[:4]); kind {
	case "mft1", "mft2":
		curves, err = transform.parseLegacyLookup(lookup, kind == "mft2")
		transform.encoding = pcsEncodingLab
		if kind == "mft2" {
			transform.encoding = pcsEncodingLegacyLab
		}
	case "mAB ":
		curves, err = transform.parseLookupAToB(lookup)
		transform.encoding = pcsEncodingLab
	default:
		return nil, fmt.Errorf("ICC lookup table type %q is unsupported", kind)
	}
	if err != nil {
		return nil, err
	}
	if !lab {
		transform.encoding = pcsEncodingXYZ
	}
	transform.setCurves(curves)
	transform.toSRGB = multiply3x3(linearSRGBFromXYZD65, transpose3x3([3][3]float64{
		d50ToD65([3]float64{1, 0, 0}),
		d50ToD65([3]float64{0, 1, 0}),
		d50ToD65([3]float64{0, 0, 1}),
	}))
	transform.black = transform.darkest()
	return transform, nil
}

// cmykLookupCurves are a lookup table's one-dimensional stages and grid size
// as parsed, before they are folded into per-value tables.
type cmykLookupCurves struct {
	grid   [cmykChannels]int
	input  [cmykChannels]iccCurve
	middle [pcsChannels]iccCurve
	output [pcsChannels]iccCurve
}

// checkLookupChannels requires four inks in and three connection values out.
func checkLookupChannels(lookup []byte) error {
	if lookup[8] != cmykChannels || lookup[9] != pcsChannels {
		return fmt.Errorf("ICC lookup table maps %d channels to %d, not CMYK to a connection space", lookup[8], lookup[9])
	}
	return nil
}

// gridEntries returns the number of samples in a table over grid, bounded by
// the bytes that could hold them.
func gridEntries(grid [cmykChannels]int, available int) (int, error) {
	entries := pcsChannels
	for _, points := range grid {
		if points < 2 {
			return 0, errors.New("ICC lookup table grid has fewer than two points")
		}
		entries *= points
		if entries > available {
			return 0, errors.New("ICC lookup table is truncated")
		}
	}
	return entries, nil
}

// readSamples decodes count 8-bit or 16-bit samples to 0..1.
func readSamples(data []byte, count int, wide bool) []float64 {
	samples := make([]float64, count)
	for index := range samples {
		if wide {
			samples[index] = float64(binary.BigEndian.Uint16(data[index*2:])) / 65535
		} else {
			samples[index] = float64(data[index]) / 255
		}
	}
	return samples
}

// parseLegacyLookup reads a lut8Type or lut16Type table: input tables, the
// grid, then output tables. Its matrix applies only to XYZ input, which CMYK
// is not.
func (transform *cmykTransform) parseLegacyLookup(lookup []byte, wide bool) (cmykLookupCurves, error) {
	var curves cmykLookupCurves
	if len(lookup) < 52 {
		return curves, errors.New("ICC lookup table header is truncated")
	}
	if err := checkLookupChannels(lookup); err != nil {
		return curves, err
	}
	width, inputEntries, outputEntries, offset := 1, 256, 256, 48
	if wide {
		width, offset = 2, 52
		inputEntries = int(binary.BigEndian.Uint16(lookup[48:50]))
		outputEntries = int(binary.BigEndian.Uint16(lookup[50:52]))
	}
	if inputEntries < 2 || outputEntries < 2 {
		return curves, errors.New("ICC lookup table curves have fewer than two entries")
	}
	for channel := range curves.grid {
		curves.grid[channel] = int(lookup[10])
	}
	body := lookup[offset:]
	entries, err := gridEntries(curves.grid, len(body)/width)
	if err != nil {
		return curves, err
	}
	if (cmykChannels*inputEntries+entries+pcsChannels*outputEntries)*width > len(body) {
		return curves, errors.New("ICC lookup table is truncated")
	}
	for channel := range curves.input {
		curves.input[channel] = iccCurve{kind: iccCurveTable, values: readSamples(body, inputEntries, wide)}
		body = body[inputEntries*width:]
	}
	transform.table = readSamples(body, entries, wide)
	body = body[entries*width:]
	for channel := range curves.output {
		curves.middle[channel] = iccCurve{kind: iccCurveIdentity}
		curves.output[channel] = iccCurve{kind: iccCurveTable, values: readSamples(body, outputEntries, wide)}
		body = body[outputEntries*width:]
	}
	return curves, nil
}

// parseLookupAToB reads a lutAToBType table: per-ink curves, the grid, then
// optional curves and a matrix, then the final curves.
func (transform *cmykTransform) parseLookupAToB(lookup []byte) (cmykLookupCurves, error) {
	var curves cmykLookupCurves
	if len(lookup) < 32 {
		return curves, errors.New("ICC lookup table header is truncated")
	}
	if err := checkLookupChannels(lookup); err != nil {
		return curves, err
	}
	section := func(field int) ([]byte, error) {
		offset := int(binary.BigEndian.Uint32(lookup[field : field+4]))
		if offset == 0 {
			return nil, nil
		}
		if offset < 32 || offset >= len(lookup) {
			return nil, errors.New("ICC lookup table section offset is invalid")
		}
		return lookup[offset:], nil
	}
	var sections [5][]byte
	for index := range sections {
		data, err := section(12 + index*4)
		if err != nil {
			return curves, err
		}
		sections[index] = data
	}
	final, matrix, middle, grid, input := sections[0], sections[1], sections[2], sections[3], sections[4]
	if final == nil || grid == nil || input == nil {
		return curves, errors.New("ICC CMYK lookup table lacks its curves or grid")
	}
	if err := parseCurveSequence(input, curves.input[:]); err != nil {
		return curves, fmt.Errorf("parse ICC input curves: %w", err)
	}
	if err := parseCurveSequence(final, curves.output[:]); err != nil {
		return curves, fmt.Errorf("parse ICC output curves: %w", err)
	}
	for channel := range curves.middle {
		curves.middle[channel] = iccCurve{kind: iccCurveIdentity}
	}
	if middle != nil {
		if err := parseCurveSequence(middle, curves.middle[:]); err != nil {
			return curves, fmt.Errorf("parse ICC matrix curves: %w", err)
		}
	}
	if matrix != nil {
		if len(matrix) < 48 {
			return curves, errors.New("ICC lookup table matrix is truncated")
		}
		for row := range pcsChannels {
			for column := range pcsChannels {
				transform.matrix[row][column] = iccFixed(matrix[(row*pcsChannels+column)*4:])
			}
			transform.matrix[row][pcsChannels] = iccFixed(matrix[(9+row)*4:])
		}
		transform.hasMatrix = true
	}
	if len(grid) < 20 {
		return curves, errors.New("ICC lookup table grid is truncated")
	}
	for channel := range curves.grid {
		curves.grid[channel] = int(grid[channel])
	}
	width := int(grid[16])
	if width != 1 && width != 2 {
		return curves, errors.New("ICC lookup table precision is invalid")
	}
	entries, err := gridEntries(curves.grid, (len(grid)-20)/width)
	if err != nil {
		return curves, err
	}
	transform.table = readSamples(grid[20:], entries, width == 2)
	return curves, nil
}

// parseCurveSequence reads consecutive curve elements, each padded to a
// four-byte boundary.
func parseCurveSequence(data []byte, curves []iccCurve) error {
	for index := range curves {
		curve, err := parseICCCurve(data)
		if err != nil {
			return err
		}
		size := 12
		switch curve.kind {
		case iccCurveParametric:
			size += 4 * (len(curve.params) - 1)
		case iccCurveTable:
			size += 2 * len(curve.values)
		case iccCurveGamma:
			size += 2
		}
		size = (size + 3) &^ 3
		if index < len(curves)-1 && size > len(data) {
			return errors.New("curve sequence is truncated")
		}
		curves[index] = curve
		data = data[min(size, len(data)):]
	}
	return nil
}

// setCurves folds the input curves and grid spacing into one table per 8-bit
// ink value and tabulates the curves that follow the grid.
func (transform *cmykTransform) setCurves(curves cmykLookupCurves) {
	stride := pcsChannels
	for channel := cmykChannels - 1; channel >= 0; channel-- {
		last := curves.grid[channel] - 1
		for value := range transform.nodes[channel] {
			position := curves.input[channel].evaluate(float64(value)/255) * float64(last)
			lower := min(int(position), last-1)
			transform.nodes[channel][value] = cmykNode{
				lower: lower * stride, upper: (lower + 1) * stride, fraction: position - float64(lower),
			}
		}
		stride *= curves.grid[channel]
	}
	tabulate := func(curve iccCurve) []float64 {
		if curve.kind == iccCurveIdentity {
			return nil
		}
		samples := make([]float64, iccCurveSamples)
		for index := range samples {
			samples[index] = clampUnit(curve.evaluate(float64(index) / (iccCurveSamples - 1)))
		}
		return samples
	}
	for channel := range pcsChannels {
		transform.middle[channel] = tabulate(curves.middle[channel])
		transform.output[channel] = tabulate(curves.output[channel])
	}
}

// sampleCurve interpolates a tabulated curve; a nil curve is the identity.
func sampleCurve(samples []float64, value float64) float64 {
	value = clampUnit(value)
	if samples == nil {
		return value
	}
	position := value * (iccCurveSamples - 1)
	lower := min(int(position), iccCurveSamples-2)
	return samples[lower] + (samples[lower+1]-samples[lower])*(position-float64(lower))
}

// connection returns the D50 XYZ the profile assigns to an ink combination,
// where 255 is full ink. The grid is interpolated linearly along all four
// axes.
func (transform *cmykTransform) connection(cyan, magenta, yellow, black uint8) [3]float64 {
	nodes := [cmykChannels]*cmykNode{
		&transform.nodes[0][cyan], &transform.nodes[1][magenta], &transform.nodes[2][yellow], &transform.nodes[3][black],
	}
	var value [pcsChannels]float64
	for corner := range 1 << cmykChannels {
		weight, offset := 1.0, 0
		for channel, node := range nodes {
			if corner>>channel&1 == 1 {
				weight *= node.fraction
				offset += node.upper
			} else {
				weight *= 1 - node.fraction
				offset += node.lower
			}
		}
		if weight == 0 {
			continue
		}
		sample := transform.table[offset : offset+pcsChannels : offset+pcsChannels]
		value[0] += weight * sample[0]
		value[1] += weight * sample[1]
		value[2] += weight * sample[2]
	}
	for channel := range value {
		value[channel] = sampleCurve(transform.middle[channel], value[channel])
	}
	if transform.hasMatrix {
		matrix := &transform.matrix
		value = [pcsChannels]float64{
			matrix[0][0]*value[0] + matrix[0][1]*value[1] + matrix[0][2]*value[2] + matrix[0][3],
			matrix[1][0]*value[0] + matrix[1][1]*value[1] + matrix[1][2]*value[2] + matrix[1][3],
			matrix[2][0]*value[0] + matrix[2][1]*value[1] + matrix[2][2]*value[2] + matrix[2][3],
		}
	}
	for channel := range value {
		value[channel] = sampleCurve(transform.output[channel], value[channel])
	}
	return transform.decodeConnection(value)
}

// decodeConnection turns encoded connection-space values into D50 XYZ.
func (transform *cmykTransform) decodeConnection(value [3]float64) [3]float64 {
	var lightness, greenRed, blueYellow float64
	switch transform.encoding {
	case pcsEncodingXYZ:
		const scale = 1 + 32767.0/32768
		return [3]float64{value[0] * scale, value[1] * scale, value[2] * scale}
	case pcsEncodingLegacyLab:
		// lut16Type keeps the version 2 encoding, where 0xFF00 is L* 100 and
		// 0x8000 is a neutral a* or b*.
		lightness = value[0] * (65535.0 / 65280) * 100
		greenRed = value[1]*(65535.0/256) - 128
		blueYellow = value[2]*(65535.0/256) - 128
	default:
		lightness, greenRed, blueYellow = value[0]*100, value[1]*255-128, value[2]*255-128
	}
	return labToXYZD50(lightness, greenRed, blueYellow)
}

// labToXYZD50 converts CIE Lab to XYZ relative to the D50 connection white.
func labToXYZD50(lightness, greenRed, blueYellow float64) [3]float64 {
	const epsilon = 216.0 / 24389.0
	const kappa = 24389.0 / 27.0
	inverse := func(value float64) float64 {
		if cube := value * value * value; cube > epsilon {
			return cube
		}
		return (116*value - 16) / kappa
	}
	fy := (lightness + 16) / 116
	return [3]float64{
		d50White[0] * inverse(fy+greenRed/500),
		d50White[1] * inverse(fy),
		d50White[2] * inverse(fy-blueYellow/200),
	}
}

// darkest returns the neutral black point of the press the profile describes:
// the luminance of its darkest ink, black alone or all four together. Paper
// black is far lighter than a display's, so it is mapped to display black;
// otherwise every shadow in a converted picture would render as dark gray.
func (transform *cmykTransform) darkest() [3]float64 {
	luminance := min(
		transform.connection(255, 255, 255, 255)[1],
		transform.connection(0, 0, 0, 255)[1],
	)
	// A black lighter than L* 50 is not a black point but a broken or
	// unusual table, and scaling by it would crush the picture.
	const brightestBlack = 0.184187
	if !(luminance >= 0) || luminance > brightestBlack {
		luminance = 0
	}
	return [3]float64{d50White[0] * luminance, luminance, d50White[2] * luminance}
}

// convert writes the opaque sRGB pixel for an ink combination.
func (transform *cmykTransform) convert(output []uint8, cyan, magenta, yellow, black uint8) {
	xyz := transform.connection(cyan, magenta, yellow, black)
	for channel := range xyz {
		white := d50White[channel]
		xyz[channel] = (xyz[channel] - transform.black[channel]) * white / (white - transform.black[channel])
	}
	for row := range 3 {
		matrix := &transform.toSRGB[row]
		output[row] = transform.tables.encode(matrix[0]*xyz[0] + matrix[1]*xyz[1] + matrix[2]*xyz[2])
	}
	output[3] = 255
}

// convertCMYKToSRGB converts a CMYK image through its embedded profile.
func convertCMYKToSRGB(src image.Image, profileData []byte) (*image.RGBA, error) {
	inks, ok := src.(*image.CMYK)
	if !ok {
		return nil, errors.New("ICC profile is CMYK but the image is not")
	}
	transform, err := newCMYKTransform(profileData)
	if err != nil {
		return nil, err
	}
	bounds := inks.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	runPixelRows(0, bounds.Dy(), func(_, startY, endY int) {
		for y := startY; y < endY; y++ {
			source := inks.Pix[inks.PixOffset(bounds.Min.X, bounds.Min.Y+y):]
			row := dst.Pix[y*dst.Stride:]
			for x := range bounds.Dx() {
				ink := source[x*4 : x*4+4 : x*4+4]
				transform.convert(row[x*4:x*4+4:x*4+4], ink[0], ink[1], ink[2], ink[3])
			}
		}
	})
	return dst, nil
}

// iccDataColorSpace returns a profile's four-character device color space, or
// "" when the header is too short to hold one.
func iccDataColorSpace(data []byte) string {
	if len(data) < 20 {
		return ""
	}
	return string(data[16:20])
}
