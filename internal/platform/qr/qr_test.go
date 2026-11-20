package qr

// Verifying an encoder without a decoder.
//
// A test that compares against a stored image proves the encoder still does
// what it did yesterday, which is not the same as doing the right thing. These
// check the two properties that make a symbol readable, and both are computed
// from the placed modules rather than from the encoder's own intermediate
// values, so an error in placement or masking cannot pass.
//
//  1. The codeword stream is a valid Reed-Solomon codeword, proved by
//     evaluating its syndromes. A valid codeword is divisible by the generator,
//     so the received polynomial is zero at every root. Getting this from
//     re-running the encoder would prove nothing; evaluating it is independent.
//  2. Reading the modules back through the inverse of the mask and the
//     placement reproduces the bytes that went in. That is what a scanner does.

import (
	"fmt"
	"strings"
	"testing"
)

// readCodewords is the inverse of placeData and the mask: it walks the same
// zigzag, undoes the mask, and reassembles the interleaved codeword stream.
func readCodewords(c *Code) []byte {
	var bits []bool
	n := c.Size
	up := true
	for right := n - 1; right >= 0; right -= 2 {
		if right == 6 {
			right--
		}
		for i := 0; i < n; i++ {
			y := i
			if up {
				y = n - 1 - i
			}
			for dx := 0; dx < 2; dx++ {
				x := right - dx
				if c.isReserved(x, y) {
					continue
				}
				v := c.Dark(x, y)
				if maskAt(c.Mask, x, y) {
					v = !v
				}
				bits = append(bits, v)
			}
		}
		up = !up
	}

	out := make([]byte, len(bits)/8)
	for i, b := range bits {
		if i/8 >= len(out) {
			break
		}
		if b {
			out[i/8] |= 1 << uint(7-i%8)
		}
	}
	return out
}

// deinterleave undoes the block interleaving, returning the data codewords of
// every block back to back.
func deinterleave(stream []byte, blk block) []byte {
	n := blk.blocks()
	sizes := make([]int, 0, n)
	for i := 0; i < blk.g1Count; i++ {
		sizes = append(sizes, blk.g1Data)
	}
	for i := 0; i < blk.g2Count; i++ {
		sizes = append(sizes, blk.g2Data)
	}

	blocks := make([][]byte, n)
	pos := 0
	longest := blk.g1Data
	if blk.g2Data > longest {
		longest = blk.g2Data
	}
	for i := 0; i < longest; i++ {
		for b := 0; b < n; b++ {
			if i < sizes[b] {
				blocks[b] = append(blocks[b], stream[pos])
				pos++
			}
		}
	}
	var out []byte
	for _, b := range blocks {
		out = append(out, b...)
	}
	return out
}

// blocksFromStream splits the interleaved stream back into full blocks,
// data followed by their error correction, which is what the syndrome check
// needs.
func blocksFromStream(stream []byte, blk block) [][]byte {
	n := blk.blocks()
	sizes := make([]int, 0, n)
	for i := 0; i < blk.g1Count; i++ {
		sizes = append(sizes, blk.g1Data)
	}
	for i := 0; i < blk.g2Count; i++ {
		sizes = append(sizes, blk.g2Data)
	}

	data := make([][]byte, n)
	pos := 0
	longest := blk.g1Data
	if blk.g2Data > longest {
		longest = blk.g2Data
	}
	for i := 0; i < longest; i++ {
		for b := 0; b < n; b++ {
			if i < sizes[b] {
				data[b] = append(data[b], stream[pos])
				pos++
			}
		}
	}
	for i := 0; i < blk.ecPerBlock; i++ {
		for b := 0; b < n; b++ {
			data[b] = append(data[b], stream[pos])
			pos++
		}
	}
	return data
}

// syndromesZero evaluates the codeword polynomial at each root of the
// generator. A valid Reed-Solomon codeword is divisible by the generator, so
// every one of these is zero. This is the independent check: nothing here calls
// the encoder.
func syndromesZero(codeword []byte, ec int) bool {
	for i := 0; i < ec; i++ {
		root := gfExp[i]
		var acc byte
		for _, coef := range codeword {
			acc = gfMul(acc, root) ^ coef
		}
		if acc != 0 {
			return false
		}
	}
	return true
}

// decode reads a finished symbol back to the string that went into it.
func decode(t *testing.T, c *Code) string {
	t.Helper()
	blk := levelM[c.Version]
	stream := readCodewords(c)
	if len(stream) < blk.dataCodewords()+blk.ecPerBlock*blk.blocks() {
		t.Fatalf("read back %d codewords, want at least %d",
			len(stream), blk.dataCodewords()+blk.ecPerBlock*blk.blocks())
	}
	data := deinterleave(stream, blk)

	// Mode, then length, then the bytes.
	if mode := data[0] >> 4; mode != 0b0100 {
		t.Fatalf("mode nibble is %04b, want 0100 (byte mode)", mode)
	}
	countBits := charCountBits(c.Version)
	// The stream is not byte aligned after the four bit mode indicator, so
	// this reads bit by bit rather than pretending it is.
	bitAt := func(i int) int {
		if data[i/8]&(1<<uint(7-i%8)) != 0 {
			return 1
		}
		return 0
	}
	pos := 4
	length := 0
	for i := 0; i < countBits; i++ {
		length = length<<1 | bitAt(pos)
		pos++
	}
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		var b int
		for k := 0; k < 8; k++ {
			b = b<<1 | bitAt(pos)
			pos++
		}
		out[i] = byte(b)
	}
	return string(out)
}

func TestEncodeRoundTripsThroughTheModules(t *testing.T) {
	cases := []string{
		"A",
		"https://hearth.example.com/a/CV-003",
		"https://hearth.example.com/a/NORTH-LINE3-GEARBOX-BEARING-07",
		strings.Repeat("x", 100),
		strings.Repeat("y", 213), // exactly the version 10 capacity
	}
	for _, want := range cases {
		c, err := Encode(want)
		if err != nil {
			t.Fatalf("encode %q: %v", short(want), err)
		}
		if got := decode(t, c); got != want {
			t.Errorf("round trip of %q gave %q.\n"+
				"The modules on the symbol do not read back as the bytes that "+
				"went in, so a scanner would not read it either.",
				short(want), short(got))
		}
	}
}

func TestEveryBlockIsAValidReedSolomonCodeword(t *testing.T) {
	for _, text := range []string{
		"A",
		"https://hearth.example.com/a/CV-003",
		strings.Repeat("z", 60),
		strings.Repeat("z", 150),
		strings.Repeat("z", 213),
	} {
		c, err := Encode(text)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		blk := levelM[c.Version]
		for i, cw := range blocksFromStream(readCodewords(c), blk) {
			if !syndromesZero(cw, blk.ecPerBlock) {
				t.Errorf("version %d block %d is not a valid codeword.\n"+
					"Its syndromes are not zero, so the error correction is "+
					"wrong and a reader will reject or miscorrect it.", c.Version, i)
			}
		}
	}
}

// The error correction table is the part most likely to be mistyped, and the
// round trip above cannot catch a mistake in it: the reader in this file uses
// the same table, so a wrong entry is consistent with itself and everything
// passes.
//
// This checks it against something that does not come from any table. The
// number of modules a symbol has left after its function patterns are drawn is
// a property of the geometry, and it has to be exactly the codewords the table
// claims, plus the remainder bits. Change one entry and the two disagree.
func TestTheErrorCorrectionTableMatchesTheModulesAvailable(t *testing.T) {
	// Total codewords by version, which is the one number in the specification
	// that does not depend on the error correction level. Cross-checking two
	// independent tables catches a typo in either.
	totalCodewords := map[int]int{
		1: 26, 2: 44, 3: 70, 4: 100, 5: 134,
		6: 172, 7: 196, 8: 242, 9: 292, 10: 346,
	}

	for v := 1; v <= maxVersion; v++ {
		blk := levelM[v]
		claimed := blk.dataCodewords() + blk.ecPerBlock*blk.blocks()
		if claimed != totalCodewords[v] {
			t.Errorf("version %d: the level M table adds up to %d codewords, "+
				"the specification says %d", v, claimed, totalCodewords[v])
		}

		// And the geometry, which is computed rather than tabled. Build the
		// symbol, count what the function patterns did not take, and compare.
		c := &Code{Version: v, Size: v*4 + 17}
		c.modules = make([]bool, c.Size*c.Size)
		c.reserved = make([]bool, c.Size*c.Size)
		c.drawFunctionPatterns()
		free := 0
		for _, r := range c.reserved {
			if !r {
				free++
			}
		}
		if want := claimed*8 + remainderBits(v); free != want {
			t.Errorf("version %d has %d modules for data, but the table wants "+
				"%d codewords and %d remainder bits, which is %d modules.\n"+
				"One of the two is wrong, and the table is the likelier.",
				v, free, claimed, remainderBits(v), want)
		}
	}
}

// The version has to be the smallest that fits. A label is a fixed size, so a
// larger version means smaller modules, which is exactly what a scanner looking
// at an oily surface does not need.
func TestVersionIsTheSmallestThatFits(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want int
	}{
		{1, 1}, {14, 1}, {15, 2}, {26, 2}, {27, 3},
		{62, 4}, {63, 5}, {213, 10},
	} {
		c, err := Encode(strings.Repeat("a", tc.n))
		if err != nil {
			t.Fatalf("encode %d bytes: %v", tc.n, err)
		}
		if c.Version != tc.want {
			t.Errorf("%d bytes chose version %d, want %d", tc.n, c.Version, tc.want)
		}
		if c.Size != c.Version*4+17 {
			t.Errorf("version %d is %d modules across, want %d",
				c.Version, c.Size, c.Version*4+17)
		}
	}
}

// Too long is refused rather than truncated. A label that scans to the wrong
// page is worse than one that does not scan.
func TestTooLongIsRefused(t *testing.T) {
	if _, err := Encode(strings.Repeat("a", 214)); err == nil {
		t.Error("214 bytes encoded into a symbol that holds 213")
	}
}

// The finders are what a scanner locks onto before it reads anything. If they
// are wrong, nothing else matters.
func TestFinderPatternsAreWhereAScannerLooks(t *testing.T) {
	c, err := Encode("https://hearth.example.com/a/CV-003")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	n := c.Size
	for _, origin := range [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
		ox, oy := origin[0], origin[1]
		for dy := 0; dy < 7; dy++ {
			for dx := 0; dx < 7; dx++ {
				ring := dx == 0 || dx == 6 || dy == 0 || dy == 6
				centre := dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4
				if got := c.Dark(ox+dx, oy+dy); got != (ring || centre) {
					t.Fatalf("finder at (%d,%d) is wrong at offset (%d,%d)", ox, oy, dx, dy)
				}
			}
		}
	}
	// The timing pattern alternates, starting dark at index six.
	for i := 8; i < n-8; i++ {
		if c.Dark(i, 6) != (i%2 == 0) || c.Dark(6, i) != (i%2 == 0) {
			t.Fatalf("timing pattern is wrong at %d", i)
		}
	}
	// And the dark module, which is always set.
	if !c.Dark(8, n-8) {
		t.Error("the dark module is not set")
	}
}

// The format field says which mask was used, and a reader that cannot recover
// it cannot read the symbol at all. This decodes it back the way a reader does,
// including the fixed XOR.
func TestFormatInformationDecodesBackToTheMaskUsed(t *testing.T) {
	c, err := Encode("https://hearth.example.com/a/CV-003")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	read := func(at func(i int) bool) int {
		v := 0
		for i := 14; i >= 0; i-- {
			v <<= 1
			if at(i) {
				v |= 1
			}
		}
		return v
	}
	n := c.Size
	first := read(func(i int) bool {
		switch {
		case i < 6:
			return c.Dark(8, i)
		case i == 6:
			return c.Dark(8, 7)
		case i == 7:
			return c.Dark(8, 8)
		case i == 8:
			return c.Dark(7, 8)
		default:
			return c.Dark(14-i, 8)
		}
	})
	second := read(func(i int) bool {
		if i < 8 {
			return c.Dark(n-1-i, 8)
		}
		return c.Dark(8, n-15+i)
	})
	if first != second {
		t.Errorf("the two format copies disagree: %015b and %015b.\n"+
			"They exist so that damage to one corner does not lose the symbol.",
			first, second)
	}

	bits := first ^ 0b101010000010010
	level, mask := bits>>13, (bits>>10)&0b111
	if level != 0b00 {
		t.Errorf("format says error correction level %02b, want 00 for M", level)
	}
	if mask != c.Mask {
		t.Errorf("format says mask %d, the symbol was masked with %d", mask, c.Mask)
	}
	// And the BCH remainder has to check out, or a reader will treat the
	// format as damaged and try to correct it into something else.
	if got := bch(bits>>10, 10, 0b10100110111); got != bits&0b1111111111 {
		t.Errorf("format BCH remainder is %010b, want %010b", bits&0b1111111111, got)
	}
}

// Version information only appears from version seven, and a reader uses it to
// know how big the symbol is.
func TestVersionInformationAppearsFromVersionSeven(t *testing.T) {
	small, err := Encode("short")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if small.Version >= 7 {
		t.Fatalf("expected a small version for a short string, got %d", small.Version)
	}
	big, err := Encode(strings.Repeat("q", 150))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if big.Version < 7 {
		t.Fatalf("expected version 7 or above for 150 bytes, got %d", big.Version)
	}

	n := big.Size
	v := 0
	for i := 17; i >= 0; i-- {
		v <<= 1
		if big.Dark(i/3, n-11+i%3) {
			v |= 1
		}
	}
	if v>>12 != big.Version {
		t.Errorf("version block says %d, the symbol is version %d", v>>12, big.Version)
	}
	if got := bch(big.Version, 12, 0b1111100100101); got != v&0xFFF {
		t.Errorf("version BCH remainder is %012b, want %012b", v&0xFFF, got)
	}
	// Both copies, for the same reason as the format.
	for i := 0; i < 18; i++ {
		if big.Dark(i/3, n-11+i%3) != big.Dark(n-11+i%3, i/3) {
			t.Fatalf("the two version blocks disagree at bit %d", i)
		}
	}
}

// The mask is chosen by penalty, not taken at random, and the choice has to be
// the best of the eight or the symbol scans badly.
func TestTheChosenMaskIsTheLowestPenalty(t *testing.T) {
	c, err := Encode("https://hearth.example.com/a/CV-003")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Undo the chosen mask to get back to the unmasked symbol, then score all
	// eight the way the encoder did.
	base := &Code{Version: c.Version, Size: c.Size,
		modules: make([]bool, len(c.modules)), reserved: c.reserved}
	copy(base.modules, c.modules)
	base.xorMask(c.Mask)

	best, bestScore := -1, 1<<62
	saved := make([]bool, len(base.modules))
	copy(saved, base.modules)
	for m := 0; m < 8; m++ {
		copy(base.modules, saved)
		base.xorMask(m)
		base.drawFormat(m)
		if s := base.penalty(); s < bestScore {
			best, bestScore = m, s
		}
	}
	if best != c.Mask {
		t.Errorf("mask %d was used, but mask %d scores lower", c.Mask, best)
	}
}

func TestSVGIsSelfContainedAndHasAQuietZone(t *testing.T) {
	svg, err := SVG("https://hearth.example.com/a/CV-003", 4)
	if err != nil {
		t.Fatalf("svg: %v", err)
	}
	if !strings.HasPrefix(svg, "<svg xmlns=") || !strings.HasSuffix(svg, "</svg>") {
		t.Error("the SVG is not a standalone document")
	}
	if strings.Contains(svg, "http://") && !strings.Contains(svg, "www.w3.org") {
		t.Error("the SVG references something off the page")
	}
	c, err := Encode("https://hearth.example.com/a/CV-003")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The quiet zone is four modules on every side, and a scanner genuinely
	// needs it: a symbol printed flush to the edge of a label against a dark
	// machine does not read.
	side := (c.Size + quietZone*2) * 4
	if !strings.Contains(svg, fmt.Sprintf(`width="%d"`, side)) {
		t.Errorf("the symbol is %d modules but the drawing is not %d wide", c.Size, side)
	}
}

func short(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}
