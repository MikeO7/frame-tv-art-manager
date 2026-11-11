package optimize

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ICC signatures for the connection spaces and the 16-bit lookup table type.
const (
	iccSpaceXYZ    = "XYZ "
	iccSpaceLab    = "Lab "
	iccLookup16Bit = "mft2"
)

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
		// Compared as 64-bit values: on 32-bit builds the sum would wrap.
		offset, size := binary.BigEndian.Uint32(entry[4:8]), binary.BigEndian.Uint32(entry[8:12])
		if uint64(offset)+uint64(size) > uint64(len(data)) {
			return nil, errors.New("ICC tag range is invalid")
		}
		tags[string(entry[:4])] = data[offset : offset+size]
	}
	return tags, nil
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
	// The header lists the sections in the reverse of the order they apply.
	var sections [5][]byte
	for index := range sections {
		offset := int(binary.BigEndian.Uint32(lookup[12+index*4:]))
		if offset == 0 {
			continue
		}
		if offset < 32 || offset >= len(lookup) {
			return curves, errors.New("ICC lookup table section offset is invalid")
		}
		sections[index] = lookup[offset:]
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
	if err := transform.parseLookupMatrix(matrix); err != nil {
		return curves, err
	}
	var err error
	curves.grid, transform.table, err = parseLookupGrid(grid)
	return curves, err
}

// parseLookupMatrix reads a lutAToBType's optional three-by-four matrix.
func (transform *cmykTransform) parseLookupMatrix(matrix []byte) error {
	if matrix == nil {
		return nil
	}
	if len(matrix) < 48 {
		return errors.New("ICC lookup table matrix is truncated")
	}
	for row := range pcsChannels {
		for column := range pcsChannels {
			transform.matrix[row][column] = iccFixed(matrix[(row*pcsChannels+column)*4:])
		}
		transform.matrix[row][pcsChannels] = iccFixed(matrix[(9+row)*4:])
	}
	transform.hasMatrix = true
	return nil
}

// parseLookupGrid reads a lutAToBType's grid: its size along each ink, the
// sample width, then the samples.
func parseLookupGrid(grid []byte) ([cmykChannels]int, []float64, error) {
	var points [cmykChannels]int
	if len(grid) < 20 {
		return points, nil, errors.New("ICC lookup table grid is truncated")
	}
	for channel := range points {
		points[channel] = int(grid[channel])
	}
	width := int(grid[16])
	if width != 1 && width != 2 {
		return points, nil, errors.New("ICC lookup table precision is invalid")
	}
	entries, err := gridEntries(points, (len(grid)-20)/width)
	if err != nil {
		return points, nil, err
	}
	return points, readSamples(grid[20:], entries, width == 2), nil
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
