package web

// The stylesheet, checked rather than looked at.
//
// Contrast and target size are the two things about this interface that decide
// whether it works in a plant room, and they are also the two that cannot be
// judged by opening it on the machine it was written on. A ratio is arithmetic
// and a tap target is a number, so both are testable, and a test is the only
// thing that catches a colour somebody nudged at four in the afternoon.

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Contrast
// ---------------------------------------------------------------------------

// pair is a foreground on a background that the interface actually puts there.
//
// Listed by hand, deliberately. A test that discovered pairs from the CSS
// would be testing its own parser; this one is a statement about where the
// colours are used, and a rule that puts a new one somewhere is a line here.
type pair struct {
	fg, bg string
	// min is the ratio this pair has to reach. 4.5 is the readable floor for
	// body text. 3 is the floor for something that only has to be seen rather
	// than read: a border, the edge of a control.
	min   float64
	where string
}

var contrastPairs = []pair{
	{"--fg", "--bg", 4.5, "everything anybody reads"},
	{"--fg", "--panel", 4.5, "text on a card, the top bar, the field grid"},
	{"--muted", "--bg", 4.5, "every timestamp, every count, every note under a field"},
	{"--muted", "--panel", 4.5, "the same, on a card"},
	{"--accent", "--bg", 4.5, "every link"},
	{"--accent", "--panel", 4.5, "a link inside a card"},
	{"--err", "--bg", 4.5, "the word that says something is wrong"},
	{"--err", "--panel", 4.5, "the same, on a card"},
	{"--warn", "--bg", 4.5, "overdue"},
	{"--ok", "--bg", 4.5, "done"},
	// The navigation rail is its own surface, so every colour the rail puts on
	// it is a pair here rather than an assumption that it is close enough to a
	// card. It is the one surface on screen at all times.
	{"--fg", "--rail", 4.5, "the destination somebody is currently on"},
	{"--muted", "--rail", 4.5, "every other destination, and the group headings"},
	// The boundary of a control, which is the thing that says a field is a
	// field. Three to one, which is the floor for something that has to be
	// seen rather than read.
	//
	// --line is deliberately not here. It separates rather than bounds: the
	// hairline between two table rows and the edge of a card carry no
	// information a reader needs, and a separator at three to one turns every
	// list into a grid. The two were one token until this test measured it at
	// 1.34 to 1 and the fields turned out to have no visible edge at all.
	{"--edge", "--bg", 3, "the edge of every field and every button"},
	{"--edge", "--panel", 3, "the same, on a card"},
	{"--edge", "--rail", 3, "the dot on a navigation group with no colour of its own"},
}

// Both palettes, because the daylight one is the one that gets less use and so
// gets less looking at.
var palettes = []string{"", `[data-theme="day"]`}

func TestEveryColourPairIsReadable(t *testing.T) {
	css := mustCSS(t)
	for _, theme := range palettes {
		tokens := tokensFor(t, css, theme)
		name := "dark"
		if theme != "" {
			name = "daylight"
		}
		for _, p := range contrastPairs {
			fg, ok := tokens[p.fg]
			if !ok {
				t.Fatalf("%s palette has no %s", name, p.fg)
			}
			bg, ok := tokens[p.bg]
			if !ok {
				t.Fatalf("%s palette has no %s", name, p.bg)
			}
			if got := contrast(t, fg, bg); got < p.min {
				t.Errorf("%s: %s (%s) on %s (%s) is %.2f to 1, needs %.1f.\n"+
					"That is %s, and it is read outdoors in gloves.",
					name, p.fg, fg, p.bg, bg, got, p.min, p.where)
			}
		}
	}
}

// The buttons carry their own colours rather than a token pair, so they are
// checked as the literal declarations they are. The primary button is the one
// people press, and it shipped at 4.25 to 1 until this test said so.
func TestButtonLabelsAreReadableOnTheirOwnBackgrounds(t *testing.T) {
	css := mustCSS(t)
	dark := tokensFor(t, css, "")
	day := tokensFor(t, css, `[data-theme="day"]`)

	cases := []struct {
		fg, bg, what string
	}{
		{"#ffffff", dark["--accent-dim"], "the primary button, dark"},
		{"#111111", dark["--accent"], "the primary button while it is pressed"},
		{dark["--fg"], dark["--line"], "the label on an ordinary button, dark"},
		{"#ffffff", dark["--accent-dim"], "the unread badge and the active filter chip"},
		{day["--fg"], day["--panel"], "an ordinary button, daylight"},
		{"#ffffff", day["--accent-dim"], "the primary button, daylight"},
	}
	for _, c := range cases {
		if got := contrast(t, c.fg, c.bg); got < 4.5 {
			t.Errorf("%s: %s on %s is %.2f to 1, needs 4.5", c.what, c.fg, c.bg, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Touch
// ---------------------------------------------------------------------------

// The gloved minimum.
//
// Forty-four is what the platform guidelines say and it is measured on a bare
// fingertip. A glove is wider and less precise, and the difference between
// forty-four and forty-eight is one mis-tap per shift rather than none.
const minTarget = 48

func TestEveryControlIsBigEnoughForAGlove(t *testing.T) {
	css := mustCSS(t)
	block := coarseBlock(t, css)

	// The controls a hand actually lands on. A rule for each has to exist, or
	// the block is decoration.
	for _, selector := range []string{"button", "input", "select", "textarea", ".chip"} {
		if !mentions(block, selector) {
			t.Errorf("the coarse pointer block does not size %q.\n"+
				"Every control somebody taps at a machine needs a minimum of "+
				"%dpx, and one that is missed is the one they need in gloves.",
				selector, minTarget)
		}
	}

	// And every size declared inside it has to be at or above the minimum. A
	// rule that sets 32px is worse than no rule: it looks like the question was
	// answered.
	heights := regexp.MustCompile(`min-(?:height|width):\s*(\d+)px`)
	found := 0
	for _, m := range heights.FindAllStringSubmatch(block, -1) {
		found++
		px, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("could not read %q", m[0])
		}
		if px < minTarget {
			t.Errorf("the coarse pointer block declares %s, under the %dpx minimum",
				m[0], minTarget)
		}
	}
	if found == 0 {
		t.Fatal("the coarse pointer block sets no minimum size at all, so this " +
			"test proves nothing")
	}

	// A phone browser zooms the page when a field smaller than sixteen pixels
	// takes focus, and the layout jumps out from under the thumb that is still
	// on the screen. It is one declaration and it is invisible until it is not.
	if !strings.Contains(block, "font-size:16px") {
		t.Error("fields in the coarse pointer block are under 16px, so focusing " +
			"one zooms the page and moves everything under the thumb")
	}
}

// The button people press hardest is the one on the outbox that answers a
// conflict, and the scan box. Both are sized whatever the pointer says it is,
// because the pointer is not always right about a tablet in a cradle.
func TestTheHardestControlsAreSizedWithoutBeingAsked(t *testing.T) {
	css := mustCSS(t)
	re := regexp.MustCompile(`(?m)^button\.big[^{]*\{[^}]*min-height:\s*(\d+)px`)
	m := re.FindStringSubmatch(css)
	if m == nil {
		t.Fatal("there is no .big control size outside a media query")
	}
	px, _ := strconv.Atoi(m[1])
	if px < minTarget {
		t.Errorf(".big is %dpx, under the %dpx minimum", px, minTarget)
	}
}

// ---------------------------------------------------------------------------
// Reading the stylesheet
// ---------------------------------------------------------------------------

func mustCSS(t *testing.T) string {
	t.Helper()
	body, err := staticFS.ReadFile("static/hearth.css")
	if err != nil {
		t.Fatalf("read the stylesheet: %v", err)
	}
	return string(body)
}

// tokensFor pulls one palette out of the stylesheet.
//
// The dark one is on a bare :root and the daylight one is on :root with the
// attribute, so the daylight palette starts as a copy of the dark one and is
// overwritten by whatever it redefines. That is how the cascade actually
// applies them, and a test that read only the second block would pass on a
// palette missing half its colours.
func tokensFor(t *testing.T, css, theme string) map[string]string {
	t.Helper()
	out := map[string]string{}
	readInto(t, out, css, `:root{`)
	if theme != "" {
		readInto(t, out, css, `:root`+theme+`{`)
	}
	if len(out) == 0 {
		t.Fatalf("no palette found for %q", theme)
	}
	return out
}

func readInto(t *testing.T, out map[string]string, css, opener string) {
	t.Helper()
	i := strings.Index(css, opener)
	if i < 0 {
		t.Fatalf("the stylesheet has no %s block", opener)
	}
	body := css[i+len(opener):]
	if j := strings.Index(body, "}"); j >= 0 {
		body = body[:j]
	}
	for _, decl := range strings.Split(body, ";") {
		name, value, ok := strings.Cut(decl, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || !strings.HasPrefix(name, "--") {
			continue
		}
		out[name] = value
	}
}

// coarseBlock is everything inside the pointer:coarse media query.
func coarseBlock(t *testing.T, css string) string {
	t.Helper()
	i := strings.Index(css, "@media (pointer:coarse)")
	if i < 0 {
		t.Fatal("there is no coarse pointer block, so nothing in this interface " +
			"is sized for a finger")
	}
	// Brace counting rather than a regular expression, because the block
	// contains nested rules and a lazy match stops at the first inner brace.
	depth, start := 0, -1
	for j := i; j < len(css); j++ {
		switch css[j] {
		case '{':
			if depth == 0 {
				start = j + 1
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[start:j]
			}
		}
	}
	t.Fatal("the coarse pointer block is not closed")
	return ""
}

// mentions reports whether a selector appears in the block as a whole selector
// rather than as part of a longer one. Looking for "input" naively also matches
// the "input" inside ".fields label", and a test that cannot tell the
// difference passes on a block that sizes nothing.
func mentions(block, selector string) bool {
	for _, rule := range strings.Split(block, "}") {
		head, _, ok := strings.Cut(rule, "{")
		if !ok {
			continue
		}
		for _, part := range strings.Split(head, ",") {
			if strings.TrimSpace(part) == selector {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The arithmetic
// ---------------------------------------------------------------------------

// contrast is the WCAG ratio between two colours, which is what the whole
// question reduces to: (lighter + 0.05) / (darker + 0.05) over the relative
// luminance of each.
func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	r, g, b := rgb(t, hex)
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

// channel undoes the sRGB transfer function, which is the step that makes this
// arithmetic rather than a guess: a channel at half its range is nowhere near
// half its light.
func channel(v float64) float64 {
	c := v / 255
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func rgb(t *testing.T, hex string) (float64, float64, float64) {
	t.Helper()
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) == 3 {
		h = fmt.Sprintf("%c%c%c%c%c%c", h[0], h[0], h[1], h[1], h[2], h[2])
	}
	if len(h) != 6 {
		t.Fatalf("%q is not a colour this test can read", hex)
	}
	var out [3]float64
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		if err != nil {
			t.Fatalf("%q is not a colour this test can read", hex)
		}
		out[i] = float64(v)
	}
	return out[0], out[1], out[2]
}
