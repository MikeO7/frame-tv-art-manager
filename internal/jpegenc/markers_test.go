package jpegenc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"testing"
)

type frameComponent struct {
	id, sampling, quant byte
}

type dhtTable struct {
	classID byte
	counts  [16]byte
	values  []byte
}

type jpegSegments struct {
	markers    []byte
	width      int
	height     int
	precision  byte
	components []frameComponent
	huffman    []dhtTable
	scanTables []byte
}

// parseSegments walks the marker structure of a baseline JPEG produced by
// Encode and fails the test on anything malformed.
func parseSegments(t *testing.T, data []byte) jpegSegments {
	t.Helper()
	var s jpegSegments
	if len(data) < 4 || data[0] != 0xff || data[1] != soiMarker {
		t.Fatal("stream does not start with SOI")
	}
	if data[len(data)-2] != 0xff || data[len(data)-1] != eoiMarker {
		t.Fatal("stream does not end with EOI")
	}
	pos := 2
	for {
		if pos+4 > len(data) || data[pos] != 0xff {
			t.Fatalf("expected marker at offset %d", pos)
		}
		marker := data[pos+1]
		length := int(binary.BigEndian.Uint16(data[pos+2:]))
		if length < 2 || pos+2+length > len(data) {
			t.Fatalf("marker %#x has bad length %d", marker, length)
		}
		payload := data[pos+4 : pos+2+length]
		s.markers = append(s.markers, marker)
		switch marker {
		case sof0Marker:
			parseSOF(t, payload, &s)
		case dhtMarker:
			parseDHT(t, payload, &s)
		case sosMarker:
			parseSOS(t, payload, &s)
			checkEntropyData(t, data[pos+2+length:len(data)-2])
			return s
		case dqtMarker:
			if len(payload)%(1+blockSize) != 0 {
				t.Fatalf("DQT payload of %d bytes is not whole 8-bit tables", len(payload))
			}
		default:
			t.Fatalf("unexpected marker %#x", marker)
		}
		pos += 2 + length
	}
}

func parseSOF(t *testing.T, p []byte, s *jpegSegments) {
	t.Helper()
	if len(p) < 6 || len(p) != 6+3*int(p[5]) {
		t.Fatalf("SOF0 payload length %d does not match its component count", len(p))
	}
	s.precision = p[0]
	s.height = int(binary.BigEndian.Uint16(p[1:]))
	s.width = int(binary.BigEndian.Uint16(p[3:]))
	for i := range int(p[5]) {
		s.components = append(s.components, frameComponent{id: p[6+3*i], sampling: p[7+3*i], quant: p[8+3*i]})
	}
}

func parseDHT(t *testing.T, p []byte, s *jpegSegments) {
	t.Helper()
	for len(p) > 0 {
		if len(p) < 17 {
			t.Fatalf("DHT table header truncated: %d bytes", len(p))
		}
		var table dhtTable
		table.classID = p[0]
		copy(table.counts[:], p[1:17])
		total := 0
		// Kraft sum in units of 2^-16: a valid baseline table is strictly
		// below 1 because the all-ones code is reserved.
		kraft := 0
		for i, n := range table.counts {
			total += int(n)
			kraft += int(n) << (15 - i)
		}
		if total == 0 || total > 256 || 17+total > len(p) {
			t.Fatalf("DHT table %#x declares %d symbols", table.classID, total)
		}
		if kraft >= 1<<16 {
			t.Fatalf("DHT table %#x is over-full or uses the all-ones code (Kraft %d/65536)", table.classID, kraft)
		}
		table.values = append([]byte(nil), p[17:17+total]...)
		seen := map[byte]bool{}
		for _, v := range table.values {
			if seen[v] {
				t.Fatalf("DHT table %#x repeats symbol %#x", table.classID, v)
			}
			seen[v] = true
			if table.classID>>4 == 0 && v > 11 {
				t.Fatalf("DC table %#x has category %d", table.classID, v)
			}
		}
		s.huffman = append(s.huffman, table)
		p = p[17+total:]
	}
}

func parseSOS(t *testing.T, p []byte, s *jpegSegments) {
	t.Helper()
	n := int(p[0])
	if len(p) != 1+2*n+3 {
		t.Fatalf("SOS payload length %d does not match %d components", len(p), n)
	}
	for i := range n {
		s.scanTables = append(s.scanTables, p[2+2*i])
	}
	if p[1+2*n] != 0x00 || p[2+2*n] != 0x3f || p[3+2*n] != 0x00 {
		t.Fatalf("SOS spectral selection % x is not baseline sequential", p[1+2*n:])
	}
}

// checkEntropyData verifies that every 0xff byte in the scan is stuffed.
func checkEntropyData(t *testing.T, scan []byte) {
	t.Helper()
	if len(scan) == 0 {
		t.Fatal("scan has no entropy-coded data")
	}
	for i := 0; i < len(scan); i++ {
		if scan[i] != 0xff {
			continue
		}
		if i+1 >= len(scan) || scan[i+1] != 0x00 {
			t.Fatalf("unstuffed 0xff in entropy data at offset %d", i)
		}
		i++
	}
}

func TestMarkersAreWellFormedBaseline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		img          *image.RGBA
		opts         Options
		wantSampling []byte
		wantQuant    []byte
		wantTables   []byte
		wantScan     []byte
	}{
		{
			name: "444", img: artwork(33, 17), opts: Options{Quality: 95},
			wantSampling: []byte{0x11, 0x11, 0x11}, wantQuant: []byte{0, 1, 1},
			wantTables: []byte{0x00, 0x10, 0x01, 0x11}, wantScan: []byte{0x00, 0x11, 0x11},
		},
		{
			name: "420", img: artwork(33, 17), opts: Options{Quality: 95, Subsampling: Subsampling420},
			wantSampling: []byte{0x22, 0x11, 0x11}, wantQuant: []byte{0, 1, 1},
			wantTables: []byte{0x00, 0x10, 0x01, 0x11}, wantScan: []byte{0x00, 0x11, 0x11},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := parseSegments(t, mustEncode(t, tt.img, &tt.opts))
			if got, want := fmt.Sprintf("% x", s.markers), fmt.Sprintf("% x", []byte{dqtMarker, sof0Marker, dhtMarker, sosMarker}); got != want {
				t.Fatalf("markers %s, want %s", got, want)
			}
			if s.precision != 8 || s.width != tt.img.Bounds().Dx() || s.height != tt.img.Bounds().Dy() {
				t.Fatalf("SOF0 %d-bit %dx%d, want 8-bit %v", s.precision, s.width, s.height, tt.img.Bounds().Size())
			}
			if len(s.components) != len(tt.wantSampling) {
				t.Fatalf("SOF0 has %d components, want %d", len(s.components), len(tt.wantSampling))
			}
			for i, c := range s.components {
				if c.id != byte(i+1) || c.sampling != tt.wantSampling[i] || c.quant != tt.wantQuant[i] {
					t.Fatalf("component %d = %+v, want id %d sampling %#x quant %d", i, c, i+1, tt.wantSampling[i], tt.wantQuant[i])
				}
			}
			if len(s.huffman) != len(tt.wantTables) {
				t.Fatalf("DHT holds %d tables, want %d", len(s.huffman), len(tt.wantTables))
			}
			for i, table := range s.huffman {
				if table.classID != tt.wantTables[i] {
					t.Fatalf("DHT table %d is %#x, want %#x", i, table.classID, tt.wantTables[i])
				}
			}
			if !bytes.Equal(s.scanTables, tt.wantScan) {
				t.Fatalf("SOS table selectors %v, want %v", s.scanTables, tt.wantScan)
			}
		})
	}
}
