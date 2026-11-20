package web

// writeXLSX has no database dependency -- a real Postgres is not needed to
// prove it produces a file Excel can actually open, only that the bytes it
// writes round-trip back through the same library that wrote them.

import (
	"net/http/httptest"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestWriteXLSXRoundTrips(t *testing.T) {
	rec := httptest.NewRecorder()
	err := writeXLSX(rec, "test report",
		[]string{"Person", "Hours"},
		[][]string{
			{"Alice", "8h"},
			{"Bob", "7h 30m"},
		},
		"Total", "15h 30m")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if rec.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Errorf("wrong content type: %q", rec.Header().Get("Content-Type"))
	}
	if got := rec.Header().Get("Content-Disposition"); got == "" {
		t.Error("no Content-Disposition header, so the browser will not offer a filename")
	}

	f, err := excelize.OpenReader(rec.Body)
	if err != nil {
		t.Fatalf("the file this wrote is not a file excelize can open: %v", err)
	}
	defer f.Close()

	const sheet = "Time report"
	for cell, want := range map[string]string{
		"A1": "Person", "B1": "Hours",
		"A2": "Alice", "B2": "8h",
		"A3": "Bob", "B3": "7h 30m",
		// Row 4 is the deliberate blank separator before the total.
		"A5": "Total", "B5": "15h 30m",
	} {
		got, err := f.GetCellValue(sheet, cell)
		if err != nil {
			t.Fatalf("read %s: %v", cell, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", cell, got, want)
		}
	}
}

func TestWriteXLSXWithNoRows(t *testing.T) {
	// The whole-org summary and a person's own history can both legitimately
	// be empty -- a new hire, or a range nobody logged anything in. The total
	// row still has to land somewhere sane rather than colliding with the
	// header.
	rec := httptest.NewRecorder()
	if err := writeXLSX(rec, "empty", []string{"Person", "Hours"}, nil, "Total", "0m"); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := excelize.OpenReader(rec.Body)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	got, err := f.GetCellValue("Time report", "A3")
	if err != nil {
		t.Fatalf("read A3: %v", err)
	}
	if got != "Total" {
		t.Errorf("total label landed at A3 = %q, want %q", got, "Total")
	}
}
