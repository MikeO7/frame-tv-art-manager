package jpegenc

// maxCodeLength is the longest Huffman code a baseline JPEG may use.
const maxCodeLength = 16

// symbolCount is the number of possible Huffman symbols: every DC category
// and AC run/size byte is in [0, 255].
const symbolCount = 256

type huffIndex int

const (
	huffIndexLuminanceDC huffIndex = iota
	huffIndexLuminanceAC
	huffIndexChrominanceDC
	huffIndexChrominanceAC
	nHuffIndex
)

// huffmanSpec is the DHT representation of one table.
type huffmanSpec struct {
	// count[i] is the number of codes of length i+1 bits.
	count [maxCodeLength]byte
	// value lists the symbols in order of increasing code length.
	value []byte
}

// huffmanLUT maps a symbol to its codeword. The 8 most significant bits hold
// the code length and the 24 least significant bits hold the code.
type huffmanLUT [symbolCount]uint32

// init assigns canonical codes (JPEG Annex C) to the symbols of s.
func (h *huffmanLUT) init(s *huffmanSpec) {
	*h = huffmanLUT{}
	code, k := uint32(0), 0
	for i, n := range s.count {
		length := uint32(i+1) << 24
		for range n {
			h[s.value[k]] = length | code
			code++
			k++
		}
		code <<= 1
	}
}

// optimalHuffmanSpec builds a code for the given symbol frequencies using
// the procedure of JPEG Annex K.2, as implemented by libjpeg's
// jpeg_gen_optimal_table: build a Huffman tree with a reserved symbol so no
// real code is all ones, limit lengths to 16 bits by moving pairs of leaves
// up the tree, and then drop the reserved code point.
func optimalHuffmanSpec(freq *[symbolCount]uint64) huffmanSpec {
	var f [symbolCount + 1]uint64
	copy(f[:], freq[:])
	// A table with no symbols still needs one real code to be well formed.
	used := false
	for _, n := range freq {
		used = used || n > 0
	}
	if !used {
		f[0] = 1
	}
	f[symbolCount] = 1
	codeSize := huffmanCodeSizes(&f)
	return specFromCodeSizes(&codeSize)
}

// huffmanCodeSizes returns the unlimited Huffman code length of every symbol
// with a nonzero frequency, merging the two least frequent subtrees at each
// step (Annex K, figure K.1). It overwrites f.
func huffmanCodeSizes(f *[symbolCount + 1]uint64) [symbolCount + 1]int {
	var codeSize [symbolCount + 1]int
	var others [symbolCount + 1]int
	for i := range others {
		others[i] = -1
	}
	for {
		c1, c2 := leastFrequentPair(f)
		if c2 < 0 {
			return codeSize
		}
		f[c1] += f[c2]
		f[c2] = 0
		// Every leaf in both subtrees moves one level deeper, and the c2
		// chain is appended to the c1 chain.
		codeSize[c1]++
		for others[c1] >= 0 {
			c1 = others[c1]
			codeSize[c1]++
		}
		others[c1] = c2
		codeSize[c2]++
		for others[c2] >= 0 {
			c2 = others[c2]
			codeSize[c2]++
		}
	}
}

// leastFrequentPair returns the symbols with the smallest and next smallest
// nonzero frequencies, preferring the larger symbol on ties as libjpeg does.
// c2 is -1 when fewer than two subtrees remain.
func leastFrequentPair(f *[symbolCount + 1]uint64) (c1, c2 int) {
	c1, c2 = -1, -1
	for i, n := range f {
		if n == 0 {
			continue
		}
		switch {
		case c1 < 0 || n <= f[c1]:
			c2, c1 = c1, i
		case c2 < 0 || n <= f[c2]:
			c2 = i
		}
	}
	return c1, c2
}

// specFromCodeSizes limits the code lengths to 16 bits (Annex K, figure K.3),
// removes the reserved symbol, and lists the symbols by increasing length.
func specFromCodeSizes(codeSize *[symbolCount + 1]int) huffmanSpec {
	var bits [symbolCount + 2]int
	longest := 0
	for _, size := range codeSize {
		if size > 0 {
			bits[size]++
			longest = max(longest, size)
		}
	}
	for i := longest; i > maxCodeLength; i-- {
		for bits[i] > 0 {
			// Two leaves at depth i are replaced by their parent's sibling
			// position: the pair moves up to depth j+1 under a leaf from
			// depth j, which moves down to depth j+1 as well.
			j := i - 2
			for bits[j] == 0 {
				j--
			}
			bits[i] -= 2
			bits[i-1]++
			bits[j+1] += 2
			bits[j]--
		}
	}
	// The reserved symbol has the lowest frequency, so it holds one of the
	// longest codes: the all-ones code that baseline JPEG forbids.
	last := maxCodeLength
	for bits[last] == 0 {
		last--
	}
	bits[last]--

	var spec huffmanSpec
	for i := range maxCodeLength {
		spec.count[i] = byte(bits[i+1]) //nolint:gosec // each length holds at most 256 symbols after limiting
	}
	spec.value = make([]byte, 0, symbolCount)
	for size := 1; size <= longest; size++ {
		for symbol := range symbolCount {
			if codeSize[symbol] == size {
				spec.value = append(spec.value, byte(symbol))
			}
		}
	}
	return spec
}
