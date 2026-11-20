package search_test

// Attachment text extraction. No database and no cluster: this is about bytes.

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/search"
)

// buildPDF assembles a small but genuine PDF with a Flate-compressed content
// stream, which is the shape every generated document has.
//
// Written rather than checked in as a fixture, so what is being parsed is
// visible in the test instead of being an opaque blob somebody has to hexdump
// to understand.
func buildPDF(t *testing.T, content string) []byte {
	t.Helper()
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	pdf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	pdf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	pdf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>\nendobj\n")
	fmt.Fprintf(&pdf, "4 0 obj\n<< /Length %d /Filter /FlateDecode >>\nstream\n", compressed.Len())
	pdf.Write(compressed.Bytes())
	pdf.WriteString("\nendstream\nendobj\n")
	pdf.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return pdf.Bytes()
}

func TestExtractPDFText(t *testing.T) {
	// The four operators that put glyphs on a page, as an exporter emits them.
	content := `BT
/F1 12 Tf
72 720 Td
(Preventive maintenance schedule for the Line 3 conveyor.) Tj
T*
(Replace the outboard bearing every 500 run hours.) '
[(Torque the) -250 (retaining bolts to 45 Nm.)] TJ
<48657820656E636F646564207465787420746F6F> Tj
ET`

	text, status, err := search.Extract("application/pdf", "manual.pdf",
		bytes.NewReader(buildPDF(t, content)))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if status != search.StatusDone {
		t.Fatalf("status = %q, want done. Text: %q", status, text)
	}

	for _, want := range []string{
		"Preventive maintenance schedule",
		"outboard bearing every 500 run hours",
		// A kerned array has to come back as one phrase rather than as
		// fragments with the kerning numbers in between.
		"Torque the retaining bolts to 45 Nm",
		// And a hex string, which is how a PDF writes text that will not
		// survive being a literal.
		"Hex encoded text too",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("extracted text is missing %q.\nGot: %q", want, text)
		}
	}
}

// A scan is a picture of words. It has to be reported as unsupported rather
// than as an empty success, or "we never looked" and "there is nothing in it"
// become the same answer and neither can be fixed.
func TestAScannedPDFIsUnsupportedRatherThanEmpty(t *testing.T) {
	// A content stream that draws an image and shows no text.
	content := "q 612 0 0 792 0 0 cm /Im0 Do Q"

	text, status, err := search.Extract("application/pdf", "scan.pdf",
		bytes.NewReader(buildPDF(t, content)))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if status != search.StatusUnsupported {
		t.Errorf("status = %q, want unsupported. Text: %q", status, text)
	}
	if text != "" {
		t.Errorf("text = %q, want empty", text)
	}
}

// Encoded gibberish from a font this cannot read must not reach the index. A
// few thousand junk terms in the dictionary never match a query and slow every
// one down.
func TestGibberishIsRejected(t *testing.T) {
	var junk strings.Builder
	for i := 0; i < 200; i++ {
		junk.WriteString("(\x01\x02\x03\x04\x05\x06\x07\x0e\x0f) Tj\n")
	}
	text, status, err := search.Extract("application/pdf", "cid.pdf",
		bytes.NewReader(buildPDF(t, junk.String())))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if status != search.StatusUnsupported {
		t.Errorf("status = %q, want unsupported. Text: %q", status, text)
	}
}

func TestExtractPlainText(t *testing.T) {
	cases := []struct {
		contentType, filename, body string
		want                        string
	}{
		{"text/plain", "notes.txt", "Torque spec is 45 Nm", search.StatusDone},
		{"", "export.csv", "asset,tag\nconveyor,CV-3", search.StatusDone},
		{"application/octet-stream", "runbook.md", "# Restart procedure", search.StatusDone},
		{"application/json", "config.json", `{"line":3}`, search.StatusDone},
		// A format with no words in it.
		{"image/png", "photo.png", "\x89PNG\r\n", search.StatusUnsupported},
	}
	for _, tc := range cases {
		text, status, err := search.Extract(tc.contentType, tc.filename, strings.NewReader(tc.body))
		if err != nil {
			t.Fatalf("%s: %v", tc.filename, err)
		}
		if status != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.filename, status, tc.want)
		}
		if tc.want == search.StatusDone && text != tc.body {
			t.Errorf("%s: text = %q, want %q", tc.filename, text, tc.body)
		}
	}
}

// A binary misnamed as text must not be indexed. Its bytes become thousands of
// terms that match nothing.
func TestABinaryPretendingToBeTextIsNotIndexed(t *testing.T) {
	body := "plausible start\x00\x00\x00\xff\xfe binary tail"
	text, status, err := search.Extract("text/plain", "trap.txt", strings.NewReader(body))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if status != search.StatusDone {
		t.Errorf("status = %q", status)
	}
	if text != "" {
		t.Errorf("a file with NUL bytes was indexed as text: %q", text)
	}
}
