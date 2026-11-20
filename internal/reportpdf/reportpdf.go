// Package reportpdf draws daily reports as a PDF a person can download and
// email.
//
// It knows nothing about Hearth's database or its permissions. The caller
// hands it reports it has already decided the reader may see, with their photos
// as bytes, and gets a document back. Keeping the two apart is what lets the
// drawing be tested without a database and the access rules be read without
// any drawing in the way.
package reportpdf

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"
	_ "golang.org/x/image/webp"
)

// Photo is one picture attached to a report.
type Photo struct {
	Name string
	Data []byte
}

// Comment is one remark on a report.
type Comment struct {
	Author string
	Body   string
	At     time.Time
}

// Report is one person's report for one day.
type Report struct {
	// Title is not drawn for a daily report. Lead, when set, replaces the
	// "day, author" heading, and Subject is a bold line under it: a service
	// history uses them for "date, job" and the work order's title.
	Title      string
	Lead       string
	Subject    string
	Author     string
	Day        string
	Department string
	Body       string
	Photos     []Photo
	// Files are the names of attachments that are not pictures. They are
	// listed, not embedded.
	Files    []string
	Comments []Comment
}

// Doc is one PDF: a heading and the reports under it.
type Doc struct {
	Heading    string
	Subheading string
	Generated  time.Time
	// Unit names what an entry is in the count line. Empty means "report".
	Unit    string
	Reports []Report
}

// MaxPhotos bounds the pictures embedded in one PDF so a wide date range stays
// an attachment somebody can email. Reports past the cap list their photos by
// name.
const MaxPhotos = 80

// maxPhotoPx is the longest side a photo is kept at. A phone camera produces
// four thousand pixels across; a page shows about nine hundred.
const maxPhotoPx = 1400

const (
	marginMM  = 15.0
	photoMaxW = 85.0
	photoMaxH = 70.0
)

// printable drops what the embedded font cannot draw. Comments carry emoji and
// flags; the PDF library refuses any character above the Basic Multilingual
// Plane outright, which turned a whole customer's history into an error. Joiners
// and variation selectors go with them, since on their own they print as boxes.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r > 0xFFFF, r == 0x200D, r >= 0xFE00 && r <= 0xFE0F, r == 0xFFFD:
			return -1
		}
		return r
	}, s)
}

// cleaned returns a copy of d with every string made drawable.
func cleaned(d Doc) Doc {
	d.Heading, d.Subheading = printable(d.Heading), printable(d.Subheading)
	reports := make([]Report, len(d.Reports))
	for i, r := range d.Reports {
		r.Title, r.Lead, r.Subject = printable(r.Title), printable(r.Lead), printable(r.Subject)
		r.Author, r.Department, r.Body = printable(r.Author), printable(r.Department), printable(r.Body)
		files := make([]string, len(r.Files))
		for j, f := range r.Files {
			files[j] = printable(f)
		}
		r.Files = files
		comments := make([]Comment, len(r.Comments))
		for j, c := range r.Comments {
			c.Author, c.Body = printable(c.Author), printable(c.Body)
			comments[j] = c
		}
		r.Comments = comments
		reports[i] = r
	}
	d.Reports = reports
	return d
}

// Write draws d to w.
func Write(w io.Writer, d Doc) error {
	d = cleaned(d)
	pdf := fpdf.New("P", "mm", "Letter", "")
	pdf.SetMargins(marginMM, marginMM, marginMM)
	pdf.SetAutoPageBreak(true, marginMM)
	pdf.SetCompression(true)
	pdf.SetTitle(d.Heading, true)
	pdf.SetCreator("Hearth", true)
	// Go's own fonts: free, Unicode, and embedded, so an accented name or a
	// Spanish report prints correctly on any machine that opens the file.
	pdf.AddUTF8FontFromBytes("go", "", goregular.TTF)
	pdf.AddUTF8FontFromBytes("go", "B", gobold.TTF)
	pdf.AddUTF8FontFromBytes("go", "I", goitalic.TTF)

	generated := d.Generated
	if generated.IsZero() {
		generated = time.Now()
	}
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("go", "I", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(0, 6, fmt.Sprintf("%s  |  page %d of {nb}", d.Heading, pdf.PageNo()),
			"", 0, "C", false, 0, "")
	})
	pdf.AliasNbPages("{nb}")
	pdf.AddPage()

	pdf.SetTextColor(20, 20, 20)
	pdf.SetFont("go", "B", 18)
	pdf.MultiCell(0, 8, d.Heading, "", "L", false)
	if d.Subheading != "" {
		pdf.SetFont("go", "", 11)
		pdf.SetTextColor(90, 90, 90)
		pdf.MultiCell(0, 6, d.Subheading, "", "L", false)
	}
	pdf.SetFont("go", "I", 9)
	unit := d.Unit
	if unit == "" {
		unit = "report"
	}
	pdf.MultiCell(0, 5, fmt.Sprintf("%d %s%s, prepared %s", len(d.Reports), unit,
		plural(len(d.Reports)), generated.Format("January 2, 2006")), "", "L", false)
	pdf.Ln(3)

	embedded := 0
	for i := range d.Reports {
		drawReport(pdf, &d.Reports[i], &embedded)
	}
	if len(d.Reports) == 0 {
		pdf.SetFont("go", "", 11)
		pdf.SetTextColor(60, 60, 60)
		pdf.MultiCell(0, 6, "There is nothing for this selection.", "", "L", false)
	}
	if err := pdf.Error(); err != nil {
		return err
	}
	return pdf.Output(w)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func drawReport(pdf *fpdf.Fpdf, r *Report, embedded *int) {
	// Keep a report's heading with at least its first lines.
	if pdf.GetY() > 235 {
		pdf.AddPage()
	}
	pdf.SetDrawColor(200, 200, 200)
	pdf.Line(marginMM, pdf.GetY(), 216-marginMM, pdf.GetY())
	pdf.Ln(2)

	pdf.SetTextColor(20, 20, 20)
	pdf.SetFont("go", "B", 13)
	lead := strings.TrimSpace(r.Day + "  " + r.Author)
	if r.Lead != "" {
		lead = r.Lead
	}
	pdf.MultiCell(0, 6.5, lead, "", "L", false)
	pdf.SetFont("go", "", 9.5)
	pdf.SetTextColor(90, 90, 90)
	if r.Department != "" {
		pdf.MultiCell(0, 5, r.Department, "", "L", false)
	}
	if r.Subject != "" {
		pdf.SetFont("go", "B", 10.5)
		pdf.SetTextColor(20, 20, 20)
		pdf.MultiCell(0, 5.5, r.Subject, "", "L", false)
	}
	pdf.Ln(1)

	pdf.SetTextColor(20, 20, 20)
	drawBody(pdf, r.Body)

	if len(r.Photos) > 0 {
		pdf.Ln(1)
		pdf.SetFont("go", "B", 10)
		pdf.MultiCell(0, 5.5, "Photos", "", "L", false)
		var notShown []string
		x := marginMM
		rowTop := pdf.GetY()
		rowH := 0.0
		for _, ph := range r.Photos {
			if *embedded >= MaxPhotos {
				notShown = append(notShown, ph.Name)
				continue
			}
			jpg, wpx, hpx, err := prepare(ph.Data)
			if err != nil {
				notShown = append(notShown, ph.Name)
				continue
			}
			w, h := fit(wpx, hpx)
			if x+w > 216-marginMM+0.01 { // next row
				x = marginMM
				rowTop += rowH + 3
				rowH = 0
			}
			if rowTop+h > 279.4-marginMM {
				pdf.AddPage()
				x, rowTop, rowH = marginMM, pdf.GetY(), 0
			}
			name := fmt.Sprintf("p%d", *embedded)
			pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: "JPG"}, bytes.NewReader(jpg))
			pdf.ImageOptions(name, x, rowTop, w, h, false, fpdf.ImageOptions{ImageType: "JPG"}, 0, "")
			*embedded++
			x += w + 4
			if h > rowH {
				rowH = h
			}
		}
		pdf.SetXY(marginMM, rowTop+rowH+3)
		if len(notShown) > 0 {
			pdf.SetFont("go", "I", 9)
			pdf.SetTextColor(90, 90, 90)
			pdf.MultiCell(0, 5, "Not shown in this PDF: "+strings.Join(notShown, ", "), "", "L", false)
			pdf.SetTextColor(20, 20, 20)
		}
	}

	if len(r.Files) > 0 {
		pdf.SetFont("go", "B", 10)
		pdf.MultiCell(0, 5.5, "Attached files", "", "L", false)
		pdf.SetFont("go", "", 10)
		for _, f := range r.Files {
			pdf.MultiCell(0, 5, "  • "+f, "", "L", false)
		}
		pdf.Ln(1)
	}

	if len(r.Comments) > 0 {
		pdf.SetFont("go", "B", 10)
		pdf.MultiCell(0, 5.5, "Comments", "", "L", false)
		for _, c := range r.Comments {
			pdf.SetFont("go", "B", 9.5)
			pdf.SetTextColor(70, 70, 70)
			pdf.MultiCell(0, 5, c.Author+"  "+c.At.Format("Jan 2, 3:04 PM"), "", "L", false)
			pdf.SetFont("go", "", 10)
			pdf.SetTextColor(20, 20, 20)
			pdf.MultiCell(0, 5, plainLine(c.Body), "", "L", false)
			pdf.Ln(1)
		}
	}
	pdf.Ln(4)
}

var (
	reLink   = regexp.MustCompile(`\[([^\]]*)\]\(([^)]*)\)`)
	reBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reItalic = regexp.MustCompile(`\*([^*\n]+)\*`)
	reCode   = regexp.MustCompile("`([^`]*)`")
)

// plainLine strips the Markdown marks the body uses so they do not print as
// asterisks.
func plainLine(s string) string {
	s = reLink.ReplaceAllString(s, "$1 ($2)")
	s = reBold.ReplaceAllString(s, "$1")
	s = reItalic.ReplaceAllString(s, "$1")
	s = reCode.ReplaceAllString(s, "$1")
	return s
}

func drawBody(pdf *fpdf.Fpdf, body string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if strings.TrimSpace(body) == "" {
		pdf.SetFont("go", "I", 10)
		pdf.SetTextColor(110, 110, 110)
		pdf.MultiCell(0, 5.5, "No text.", "", "L", false)
		pdf.SetTextColor(20, 20, 20)
		return
	}
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimRight(line, " \t")
		switch {
		case strings.TrimSpace(t) == "":
			pdf.Ln(2.5)
		case strings.HasPrefix(t, "#"):
			pdf.SetFont("go", "B", 11.5)
			pdf.MultiCell(0, 6, plainLine(strings.TrimSpace(strings.TrimLeft(t, "#"))), "", "L", false)
		case strings.HasPrefix(strings.TrimSpace(t), "- "), strings.HasPrefix(strings.TrimSpace(t), "* "):
			pdf.SetFont("go", "", 10.5)
			pdf.MultiCell(0, 5.5, "  • "+plainLine(strings.TrimSpace(t)[2:]), "", "L", false)
		default:
			// A "**Done:** text" line keeps its lead word bold, which is how
			// the reports are written.
			if m := reLead.FindStringSubmatch(t); m != nil {
				pdf.SetFont("go", "B", 10.5)
				lead := m[1] + " "
				pdf.Write(5.5, lead)
				pdf.SetFont("go", "", 10.5)
				pdf.Write(5.5, plainLine(m[2]))
				pdf.Ln(5.5)
				continue
			}
			pdf.SetFont("go", "", 10.5)
			pdf.MultiCell(0, 5.5, plainLine(t), "", "L", false)
		}
	}
}

var reLead = regexp.MustCompile(`^\*\*([^*]+)\*\*\s*(.*)$`)

// fit scales pixel dimensions into the photo box, in millimetres.
func fit(wpx, hpx int) (float64, float64) {
	w, h := float64(wpx), float64(hpx)
	scale := photoMaxW / w
	if s := photoMaxH / h; s < scale {
		scale = s
	}
	return w * scale, h * scale
}

// prepare decodes any picture the browser could have uploaded, applies the
// camera's rotation, shrinks it, and returns baseline JPEG bytes and the new
// size. A phone holds a portrait photo sideways and records the turn in EXIF;
// without applying it every portrait shot prints on its side.
func prepare(data []byte) ([]byte, int, int, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	img = orient(img, exifOrientation(data))
	b := img.Bounds()
	if long := max(b.Dx(), b.Dy()); long > maxPhotoPx {
		nw := b.Dx() * maxPhotoPx / long
		nh := b.Dy() * maxPhotoPx / long
		dst := image.NewRGBA(image.Rect(0, 0, max(nw, 1), max(nh, 1)))
		xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
		img = dst
		b = img.Bounds()
	}
	// JPEG has no alpha: lay a transparent PNG on white rather than black.
	flat := image.NewRGBA(b)
	draw.Draw(flat, b, image.White, image.Point{}, draw.Src)
	draw.Draw(flat, b, img, b.Min, draw.Over)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, flat, &jpeg.Options{Quality: 82}); err != nil {
		return nil, 0, 0, err
	}
	return out.Bytes(), b.Dx(), b.Dy(), nil
}

// exifOrientation reads the EXIF orientation tag (1 to 8) from a JPEG, or 1.
func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) && data[i] == 0xFF {
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}
		size := int(data[i+2])<<8 | int(data[i+3])
		if size < 2 || i+2+size > len(data) {
			return 1
		}
		seg := data[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(t[:2]) {
	case "II":
		u16 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 }
		u32 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24 }
	case "MM":
		u16 = func(b []byte) int { return int(b[0])<<8 | int(b[1]) }
		u32 = func(b []byte) int { return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]) }
	default:
		return 1
	}
	off := u32(t[4:8])
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := u16(t[off : off+2])
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if u16(t[e:e+2]) == 0x0112 {
			if v := u16(t[e+8 : e+10]); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation so the picture is upright.
func orient(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5
	var dst *image.RGBA
	if swap {
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
	} else {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
