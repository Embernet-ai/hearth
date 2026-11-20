package reportpdf

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"
)

func solid(w, h int, c color.Color) image.Image {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, c)
		}
	}
	return m
}

func jpegBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withOrientation splices an EXIF APP1 segment carrying one orientation tag
// into a JPEG, the way a phone does.
func withOrientation(jpg []byte, o byte) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, o, 0, 0, 0, 0, 0, 0}
	seg := append([]byte("Exif\x00\x00"), tiff...)
	n := len(seg) + 2
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte(n >> 8), byte(n)}
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

func TestWriteMakesAPDFWithEverythingInIt(t *testing.T) {
	d := Doc{
		Heading: "Daily reports", Subheading: "Engineering, Oct 1 to Oct 3",
		Generated: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
		Reports: []Report{{
			Day: "2026-10-02", Author: "Gabriel Adamán", Department: "Engineering / White Team",
			Body:     "**Done:** footer logo is live.\n\n# Next\n- pricing page\n- unit 4 filter [doc](https://x.y)",
			Photos:   []Photo{{Name: "a.jpg", Data: jpegBytes(t, solid(400, 300, color.RGBA{200, 30, 30, 255}))}, {Name: "b.png", Data: pngBytes(t, solid(200, 400, color.RGBA{0, 0, 200, 128}))}, {Name: "broken.heic", Data: []byte("nope")}},
			Files:    []string{"plan.docx"},
			Comments: []Comment{{Author: "Yara", Body: "looks **good**", At: time.Now()}},
		}},
	}
	var out bytes.Buffer
	if err := Write(&out, d); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	if !bytes.HasPrefix(b, []byte("%PDF-")) || !bytes.Contains(b[len(b)-64:], []byte("%%EOF")) {
		t.Fatalf("not a PDF: starts %q", b[:8])
	}
	// Two images embedded, the undecodable one only named.
	if n := bytes.Count(b, []byte("/Subtype /Image")); n != 2 {
		t.Fatalf("want 2 embedded images, got %d", n)
	}
}

func TestEmptySelectionStillMakesADocument(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, Doc{Heading: "Daily reports"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
}

func TestExifRotationTurnsAPortraitUpright(t *testing.T) {
	// 400 wide, 200 tall, marked "rotate 90 clockwise to view" (6): upright
	// it is 200 wide and 400 tall.
	raw := withOrientation(jpegBytes(t, solid(400, 200, color.RGBA{10, 200, 10, 255})), 6)
	if got := exifOrientation(raw); got != 6 {
		t.Fatalf("orientation read as %d", got)
	}
	_, w, h, err := prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	if w != 200 || h != 400 {
		t.Fatalf("got %dx%d, want 200x400", w, h)
	}
}

func TestBigPhotosAreShrunk(t *testing.T) {
	_, w, h, err := prepare(jpegBytes(t, solid(3000, 2000, color.Gray{128})))
	if err != nil {
		t.Fatal(err)
	}
	if w != maxPhotoPx || h > maxPhotoPx {
		t.Fatalf("got %dx%d", w, h)
	}
}

func TestMarkdownMarksDoNotPrint(t *testing.T) {
	got := plainLine("**a** and *b* and `c` and [d](http://e)")
	if strings.ContainsAny(got, "*`[]") || !strings.Contains(got, "d (http://e)") {
		t.Fatalf("got %q", got)
	}
}

// A comment with an emoji or a flag once failed the whole PDF with "character
// outside the supported range", which took a customer's entire history down
// with it. Found on the live data, not in a test.
func TestEmojiDoNotBreakThePDF(t *testing.T) {
	d := Doc{
		Heading: "History \U0001F1F2\U0001F1FD",
		Reports: []Report{{
			Day: "2026-10-26", Author: "Gabriel \U0001F600",
			Body:     "Done \u2705 \U0001F527 wired the panel \U0001F468\u200D\U0001F527",
			Comments: []Comment{{Author: "Yara", Body: "great \U0001F44D\U0001F3FD", At: time.Now()}},
			Files:    []string{"photo \U0001F4F7.jpg"},
		}},
	}
	var out bytes.Buffer
	if err := Write(&out, d); err != nil {
		t.Fatalf("emoji broke the pdf: %v", err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	if got := printable("a\U0001F600b\u200Dc\uFE0Fd"); got != "abcd" {
		t.Fatalf("printable = %q", got)
	}
}
