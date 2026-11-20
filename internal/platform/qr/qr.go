// Package qr encodes a short string as a QR code and draws it as SVG.
//
// # Why this is here rather than imported
//
// The same reason the markdown renderer and the PDF text extractor are: this is
// one page of a printed asset label, it has to work offline on a machine with
// no registry access, and a dependency that pulls an image encoder to draw
// twenty-nine black squares is not a trade worth making. The specification is
// ISO/IEC 18004 and it has not moved in twenty years.
//
// # What it does and does not do
//
// Byte mode, error correction level M, versions 1 to 10. That is up to 213
// bytes, which is several times the longest URL this product will ever put on a
// label, and it keeps the tables short enough to read. Anything longer is
// refused rather than silently truncated: a label that scans to the wrong page
// is worse than one that does not scan.
//
// Level M corrects fifteen percent. That is the right choice for something
// glued to a machine: L smudges out, and H spends a third of the symbol on
// correction, making the modules smaller on a label of fixed size, which is the
// opposite of helpful when the surface is oily.
//
// # Verifying it
//
// The tests do not compare against a stored image. They check the two things
// that make a symbol correct: that the codeword stream is a valid Reed-Solomon
// codeword, proved by evaluating its syndromes rather than by re-running the
// encoder, and that reading the placed modules back through the inverse of the
// placement and the mask reproduces the original bytes.
package qr

import (
	"errors"
	"fmt"
	"strings"
)

// ErrTooLong means the payload does not fit in a version this package writes.
var ErrTooLong = errors.New("qr: too long for a version 10 symbol at level M")

// maxVersion is the largest symbol this package produces.
const maxVersion = 10

// block describes how one version splits its data into Reed-Solomon blocks.
//
// The two groups exist because most versions cannot divide their data evenly:
// version 8 at level M holds 154 data codewords in four blocks, and 154 is not
// a multiple of four, so two blocks carry 38 and two carry 39.
type block struct {
	ecPerBlock int
	g1Count    int
	g1Data     int
	g2Count    int
	g2Data     int
}

func (b block) dataCodewords() int { return b.g1Count*b.g1Data + b.g2Count*b.g2Data }
func (b block) blocks() int        { return b.g1Count + b.g2Count }

// levelM is the error correction table for level M, versions 1 to 10. Taken
// from ISO/IEC 18004 tables 13 to 22.
var levelM = map[int]block{
	1:  {10, 1, 16, 0, 0},
	2:  {16, 1, 28, 0, 0},
	3:  {26, 1, 44, 0, 0},
	4:  {18, 2, 32, 0, 0},
	5:  {24, 2, 43, 0, 0},
	6:  {16, 4, 27, 0, 0},
	7:  {18, 4, 31, 0, 0},
	8:  {22, 2, 38, 2, 39},
	9:  {22, 3, 36, 2, 37},
	10: {26, 4, 43, 1, 44},
}

// alignmentCentres are the row and column centres of the alignment patterns.
// Version 1 has none, which is why the smallest symbol is also the only one
// with no correction for print distortion.
var alignmentCentres = map[int][]int{
	1: nil,
	2: {6, 18}, 3: {6, 22}, 4: {6, 26}, 5: {6, 30}, 6: {6, 34},
	7: {6, 22, 38}, 8: {6, 24, 42}, 9: {6, 26, 46}, 10: {6, 28, 50},
}

// remainderBits are the padding bits after the last codeword, which exist
// because the module count is not always a multiple of eight.
func remainderBits(version int) int {
	if version >= 2 && version <= 6 {
		return 7
	}
	return 0
}

// Code is a finished symbol.
type Code struct {
	Version int
	// Size is the side length in modules, including no quiet zone.
	Size int
	// Mask is the pattern chosen by penalty scoring, kept because the tests
	// need it to undo the mask.
	Mask int

	modules  []bool
	reserved []bool
}

// Dark reports whether one module is set. Out of range reads light, so a
// renderer drawing a quiet zone needs no bounds checks of its own.
func (c *Code) Dark(x, y int) bool {
	if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
		return false
	}
	return c.modules[y*c.Size+x]
}

func (c *Code) set(x, y int, v bool) { c.modules[y*c.Size+x] = v }
func (c *Code) reserve(x, y int)     { c.reserved[y*c.Size+x] = true }
func (c *Code) isReserved(x, y int) bool {
	return c.reserved[y*c.Size+x]
}

// Encode builds a symbol for one string.
func Encode(text string) (*Code, error) {
	data := []byte(text)
	version, blk, err := pickVersion(len(data))
	if err != nil {
		return nil, err
	}

	stream := buildStream(data, version, blk)
	final := interleave(stream, blk)

	c := &Code{Version: version, Size: version*4 + 17}
	c.modules = make([]bool, c.Size*c.Size)
	c.reserved = make([]bool, c.Size*c.Size)

	c.drawFunctionPatterns()
	c.placeData(final, remainderBits(version))
	c.Mask = c.applyBestMask()
	c.drawFormat(c.Mask)
	if version >= 7 {
		c.drawVersion()
	}
	return c, nil
}

// pickVersion is the smallest symbol the payload fits in. Smallest, because a
// label is a fixed size and a smaller version means larger modules, which is
// what a scanner actually needs.
func pickVersion(n int) (int, block, error) {
	for v := 1; v <= maxVersion; v++ {
		blk := levelM[v]
		header := 4 + charCountBits(v)
		if blk.dataCodewords()*8 >= header+n*8 {
			return v, blk, nil
		}
	}
	return 0, block{}, fmt.Errorf("%w: %d bytes", ErrTooLong, n)
}

// charCountBits is the width of the length field, which widens at version 10
// in byte mode.
func charCountBits(version int) int {
	if version >= 10 {
		return 16
	}
	return 8
}

// -----------------------------------------------------------------------------
// Bit stream
// -----------------------------------------------------------------------------

type bitWriter struct {
	bytes []byte
	nbits int
}

func (w *bitWriter) write(value, width int) {
	for i := width - 1; i >= 0; i-- {
		if w.nbits%8 == 0 {
			w.bytes = append(w.bytes, 0)
		}
		if value&(1<<uint(i)) != 0 {
			w.bytes[w.nbits/8] |= 1 << uint(7-w.nbits%8)
		}
		w.nbits++
	}
}

// buildStream writes the mode, the length, the payload, the terminator, and the
// pad bytes, then splits it into blocks and appends each block's error
// correction.
func buildStream(data []byte, version int, blk block) [][]byte {
	w := &bitWriter{}
	w.write(0b0100, 4) // byte mode
	w.write(len(data), charCountBits(version))
	for _, b := range data {
		w.write(int(b), 8)
	}

	return splitBlocks(finishStream(w, blk.dataCodewords()*8), blk)
}

// finishStream closes the bit stream and pads it to the capacity.
//
// The terminator is up to four zero bits, and shorter when the symbol is nearly
// full: writing four regardless would push the stream past the last codeword
// and the tail would be dropped.
func finishStream(w *bitWriter, capacity int) []byte {
	if term := capacity - w.nbits; term > 0 {
		if term > 4 {
			term = 4
		}
		w.write(0, term)
	}
	// Then to a byte boundary.
	if r := w.nbits % 8; r != 0 {
		w.write(0, 8-r)
	}
	out := w.bytes
	// 0xEC and 0x11 alternating, which is what every reader expects to find
	// and what makes two symbols of the same length comparable.
	for i := 0; len(out)*8 < capacity; i++ {
		if i%2 == 0 {
			out = append(out, 0xEC)
		} else {
			out = append(out, 0x11)
		}
	}
	return out
}

// splitBlocks cuts the data into its groups and appends the error correction
// for each, returning data blocks followed by EC blocks.
func splitBlocks(data []byte, blk block) [][]byte {
	out := make([][]byte, 0, blk.blocks()*2)
	ec := make([][]byte, 0, blk.blocks())

	pos := 0
	take := func(n int) []byte {
		chunk := data[pos : pos+n]
		pos += n
		return chunk
	}
	for i := 0; i < blk.g1Count; i++ {
		d := take(blk.g1Data)
		out = append(out, d)
		ec = append(ec, reedSolomon(d, blk.ecPerBlock))
	}
	for i := 0; i < blk.g2Count; i++ {
		d := take(blk.g2Data)
		out = append(out, d)
		ec = append(ec, reedSolomon(d, blk.ecPerBlock))
	}
	return append(out, ec...)
}

// interleave takes one codeword from each block in turn, which is what spreads
// a coffee ring across several blocks instead of destroying one.
func interleave(blocks [][]byte, blk block) []byte {
	n := blk.blocks()
	dataBlocks, ecBlocks := blocks[:n], blocks[n:]

	var out []byte
	longest := blk.g1Data
	if blk.g2Data > longest {
		longest = blk.g2Data
	}
	for i := 0; i < longest; i++ {
		for _, b := range dataBlocks {
			if i < len(b) {
				out = append(out, b[i])
			}
		}
	}
	for i := 0; i < blk.ecPerBlock; i++ {
		for _, b := range ecBlocks {
			out = append(out, b[i])
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// Reed-Solomon over GF(256)
// -----------------------------------------------------------------------------

// The field is GF(2^8) with the primitive polynomial 0x11D, which is the one
// the specification names. exp and log are built once at init because
// multiplication by table lookup is the whole reason the field is usable.
var (
	gfExp [512]byte
	gfLog [256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	// Doubled, so a product of two logs can be looked up without a modulo.
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// generator builds the polynomial whose roots are α^0 through α^(n-1).
func generator(n int) []byte {
	poly := []byte{1}
	for i := 0; i < n; i++ {
		next := make([]byte, len(poly)+1)
		for j, c := range poly {
			next[j] ^= c
			next[j+1] ^= gfMul(c, gfExp[i])
		}
		poly = next
	}
	return poly
}

// reedSolomon returns the remainder of the message divided by the generator,
// which is the error correction block.
func reedSolomon(data []byte, n int) []byte {
	gen := generator(n)
	rem := make([]byte, len(data)+n)
	copy(rem, data)

	for i := 0; i < len(data); i++ {
		coef := rem[i]
		if coef == 0 {
			continue
		}
		for j, g := range gen {
			rem[i+j] ^= gfMul(g, coef)
		}
	}
	return rem[len(data):]
}

// -----------------------------------------------------------------------------
// Function patterns
// -----------------------------------------------------------------------------

func (c *Code) drawFunctionPatterns() {
	n := c.Size
	// The three finders and their separators. Their positions are what a
	// scanner locks onto, so they and the light border around them are
	// reserved before anything else is placed.
	for _, p := range [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
		c.drawFinder(p[0], p[1])
	}
	// Timing patterns, the alternating row and column at index six.
	for i := 8; i < n-8; i++ {
		on := i%2 == 0
		c.set(6, i, on)
		c.reserve(6, i)
		c.set(i, 6, on)
		c.reserve(i, 6)
	}
	c.drawAlignment()

	// The dark module, which is always set and always in the same place.
	c.set(8, n-8, true)
	c.reserve(8, n-8)

	// Reserve the format areas so data placement steps over them. They are
	// written after masking, because the mask number is part of what they say.
	for i := 0; i < 9; i++ {
		if i != 6 {
			c.reserve(i, 8)
			c.reserve(8, i)
		}
	}
	for i := 0; i < 8; i++ {
		c.reserve(n-1-i, 8)
		c.reserve(8, n-1-i)
	}
	if c.Version >= 7 {
		for i := 0; i < 6; i++ {
			for j := 0; j < 3; j++ {
				c.reserve(i, n-11+j)
				c.reserve(n-11+j, i)
			}
		}
	}
}

func (c *Code) drawFinder(x, y int) {
	for dy := -1; dy <= 7; dy++ {
		for dx := -1; dx <= 7; dx++ {
			px, py := x+dx, y+dy
			if px < 0 || py < 0 || px >= c.Size || py >= c.Size {
				continue
			}
			// A seven by seven ring with a three by three centre, and the
			// separator around it, which is the light row that stops the
			// finder merging into the data.
			inRing := dx >= 0 && dx <= 6 && dy >= 0 && dy <= 6 &&
				(dx == 0 || dx == 6 || dy == 0 || dy == 6)
			inCentre := dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4
			c.set(px, py, inRing || inCentre)
			c.reserve(px, py)
		}
	}
}

func (c *Code) drawAlignment() {
	centres := alignmentCentres[c.Version]
	n := c.Size
	for _, cy := range centres {
		for _, cx := range centres {
			// Skip the three that would sit on a finder.
			if (cx == 6 && cy == 6) || (cx == 6 && cy == n-7) || (cx == n-7 && cy == 6) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					on := dx == -2 || dx == 2 || dy == -2 || dy == 2 || (dx == 0 && dy == 0)
					c.set(cx+dx, cy+dy, on)
					c.reserve(cx+dx, cy+dy)
				}
			}
		}
	}
}

// -----------------------------------------------------------------------------
// Data placement
// -----------------------------------------------------------------------------

// placeData walks the zigzag: two-module-wide columns from the right, upwards
// then downwards, skipping the vertical timing column and everything reserved.
func (c *Code) placeData(data []byte, remainder int) {
	bit := 0
	total := len(data)*8 + remainder
	next := func() bool {
		if bit >= len(data)*8 {
			bit++
			return false // the remainder bits are always light
		}
		v := data[bit/8]&(1<<uint(7-bit%8)) != 0
		bit++
		return v
	}

	n := c.Size
	up := true
	for right := n - 1; right >= 0; right -= 2 {
		if right == 6 {
			// Column six is the timing pattern. Step past it, or every column
			// to its left lands one module out.
			right--
		}
		for i := 0; i < n; i++ {
			y := i
			if up {
				y = n - 1 - i
			}
			for dx := 0; dx < 2; dx++ {
				x := right - dx
				if c.isReserved(x, y) || bit >= total {
					continue
				}
				c.set(x, y, next())
			}
		}
		up = !up
	}
}

// -----------------------------------------------------------------------------
// Masking
// -----------------------------------------------------------------------------

// maskAt reports whether one module is inverted by one pattern. The eight are
// fixed by the specification; the encoder's job is only to pick between them.
func maskAt(pattern, x, y int) bool {
	switch pattern {
	case 0:
		return (y+x)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (y+x)%3 == 0
	case 4:
		return (y/2+x/3)%2 == 0
	case 5:
		return (y*x)%2+(y*x)%3 == 0
	case 6:
		return ((y*x)%2+(y*x)%3)%2 == 0
	default:
		return ((y+x)%2+(y*x)%3)%2 == 0
	}
}

// applyBestMask tries all eight and keeps the one with the lowest penalty.
//
// The scoring is not decoration. A symbol with a large blank area or an
// accidental finder-like run scans slowly or not at all, and the penalty rules
// are what stop the encoder producing one.
func (c *Code) applyBestMask() int {
	best, bestScore := 0, 1<<62
	saved := make([]bool, len(c.modules))
	copy(saved, c.modules)

	for pattern := 0; pattern < 8; pattern++ {
		copy(c.modules, saved)
		c.xorMask(pattern)
		c.drawFormat(pattern)
		if score := c.penalty(); score < bestScore {
			best, bestScore = pattern, score
		}
	}
	copy(c.modules, saved)
	c.xorMask(best)
	return best
}

func (c *Code) xorMask(pattern int) {
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if !c.isReserved(x, y) && maskAt(pattern, x, y) {
				c.set(x, y, !c.Dark(x, y))
			}
		}
	}
}

func (c *Code) penalty() int {
	return c.penaltyRuns() + c.penaltyBlocks() + c.penaltyFinderLike() + c.penaltyBalance()
}

// penaltyRuns charges for five or more of the same colour in a line.
func (c *Code) penaltyRuns() int {
	score := 0
	count := func(get func(i int) bool) {
		run, prev := 1, get(0)
		for i := 1; i < c.Size; i++ {
			if v := get(i); v == prev {
				run++
			} else {
				if run >= 5 {
					score += 3 + (run - 5)
				}
				run, prev = 1, v
			}
		}
		if run >= 5 {
			score += 3 + (run - 5)
		}
	}
	for y := 0; y < c.Size; y++ {
		y := y
		count(func(x int) bool { return c.Dark(x, y) })
	}
	for x := 0; x < c.Size; x++ {
		x := x
		count(func(y int) bool { return c.Dark(x, y) })
	}
	return score
}

// penaltyBlocks charges for every two by two of one colour.
func (c *Code) penaltyBlocks() int {
	score := 0
	for y := 0; y < c.Size-1; y++ {
		for x := 0; x < c.Size-1; x++ {
			v := c.Dark(x, y)
			if c.Dark(x+1, y) == v && c.Dark(x, y+1) == v && c.Dark(x+1, y+1) == v {
				score += 3
			}
		}
	}
	return score
}

// finderLike is the 1:1:3:1:1 run that a scanner mistakes for a finder.
var finderLike = [11]bool{true, false, true, true, true, false, true, false, false, false, false}

func (c *Code) penaltyFinderLike() int {
	score := 0
	match := func(get func(i int) bool, i int, reversed bool) bool {
		for k := 0; k < 11; k++ {
			want := finderLike[k]
			if reversed {
				want = finderLike[10-k]
			}
			if get(i+k) != want {
				return false
			}
		}
		return true
	}
	scan := func(get func(i int) bool) {
		for i := 0; i+11 <= c.Size; i++ {
			if match(get, i, false) || match(get, i, true) {
				score += 40
			}
		}
	}
	for y := 0; y < c.Size; y++ {
		y := y
		scan(func(x int) bool { return c.Dark(x, y) })
	}
	for x := 0; x < c.Size; x++ {
		x := x
		scan(func(y int) bool { return c.Dark(x, y) })
	}
	return score
}

// penaltyBalance charges for drifting away from half dark.
func (c *Code) penaltyBalance() int {
	dark := 0
	for _, v := range c.modules {
		if v {
			dark++
		}
	}
	percent := dark * 100 / len(c.modules)
	off := percent - 50
	if off < 0 {
		off = -off
	}
	return (off / 5) * 10
}

// -----------------------------------------------------------------------------
// Format and version information
// -----------------------------------------------------------------------------

// drawFormat writes the level and mask, twice, in both format areas. Twice
// because losing one corner to damage must not lose the symbol.
func (c *Code) drawFormat(mask int) {
	// Level M is 0b00. Five data bits, BCH(15,5), then the fixed XOR that
	// stops an all-zero format reading as valid.
	bits := bch(0b00<<3|mask, 10, 0b10100110111) | (0b00<<3|mask)<<10
	bits ^= 0b101010000010010

	n := c.Size
	for i := 0; i < 15; i++ {
		on := bits&(1<<uint(i)) != 0
		// Around the top left finder.
		switch {
		case i < 6:
			c.set(8, i, on)
		case i == 6:
			c.set(8, 7, on)
		case i == 7:
			c.set(8, 8, on)
		case i == 8:
			c.set(7, 8, on)
		default:
			c.set(14-i, 8, on)
		}
		// And split across the other two.
		// And the second copy, split between the other two finders. The dark
		// module at (8, size-8) sits between the two halves and is not part of
		// either, which is why the vertical run starts at size-7.
		if i < 8 {
			c.set(n-1-i, 8, on)
		} else {
			c.set(8, n-15+i, on)
		}
	}
}

// drawVersion writes the version number, which only versions seven and above
// carry, in two blocks near the lower left and upper right finders.
func (c *Code) drawVersion() {
	bits := bch(c.Version, 12, 0b1111100100101) | c.Version<<12
	n := c.Size
	for i := 0; i < 18; i++ {
		on := bits&(1<<uint(i)) != 0
		x, y := i/3, n-11+i%3
		c.set(x, y, on)
		c.set(y, x, on)
	}
}

// bch computes the remainder used by both the format and version fields.
func bch(data, degree, generator int) int {
	value := data << uint(degree)
	genBits := bitLength(generator)
	for bitLength(value) >= genBits {
		value ^= generator << uint(bitLength(value)-genBits)
	}
	return value
}

func bitLength(v int) int {
	n := 0
	for v != 0 {
		n++
		v >>= 1
	}
	return n
}

// -----------------------------------------------------------------------------
// Rendering
// -----------------------------------------------------------------------------

// quietZone is the light border, four modules, which the specification requires
// and which scanners genuinely need. A QR printed flush to the edge of a label
// against a dark machine does not read.
const quietZone = 4

// SVG draws the symbol as a standalone SVG document.
//
// SVG rather than a raster, because the output goes to a label printer at
// whatever resolution it has, and a scaled-up PNG of a twenty-nine module
// symbol prints as a blurry grid. One path element for every dark module would
// be thousands of nodes; this emits one rectangle each, which every printer
// driver handles and which stays readable in the source.
func (c *Code) SVG(pixelsPerModule int) string {
	if pixelsPerModule < 1 {
		pixelsPerModule = 4
	}
	side := (c.Size + quietZone*2) * pixelsPerModule

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" `+
		`viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" `+
		`aria-label="Asset code">`, side, side, side, side)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`, side, side)
	b.WriteString(`<g fill="#000000">`)
	for y := 0; y < c.Size; y++ {
		// Runs rather than one rectangle per module. A version 10 symbol is
		// 3249 modules and roughly half are dark, and 1600 nodes in a page is
		// a page that scrolls badly.
		x := 0
		for x < c.Size {
			if !c.Dark(x, y) {
				x++
				continue
			}
			run := 1
			for x+run < c.Size && c.Dark(x+run, y) {
				run++
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d"/>`,
				(x+quietZone)*pixelsPerModule, (y+quietZone)*pixelsPerModule,
				run*pixelsPerModule, pixelsPerModule)
			x += run
		}
	}
	b.WriteString(`</g></svg>`)
	return b.String()
}

// SVG is the one-call form: encode and draw, for a caller that has no use for
// the symbol itself.
func SVG(text string, pixelsPerModule int) (string, error) {
	c, err := Encode(text)
	if err != nil {
		return "", err
	}
	return c.SVG(pixelsPerModule), nil
}
