package servicereport

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/reportpdf"
)

func sample() []*docs.ServiceRecord {
	t0 := time.Date(2025, 3, 3, 15, 0, 0, 0, time.UTC)
	return []*docs.ServiceRecord{{
		ID: "r1", Kind: "work_order", Title: "Drive fault", Body: "Replace the drive.", Status: "done",
		SpacePath: "White Team Service / Acme / Line 3", Job: "Line 3", Assignees: []string{"Marco Dell"},
		Opened: t0,
		Events: []docs.ServiceEvent{
			{Day: "2025-03-03", At: t0, Kind: "opened"},
			{Day: "2025-03-03", At: t0.Add(time.Hour), Kind: "comment", Who: "Bob", Text: "Found a failed\ncontactor."},
			{Day: "2025-03-03", At: t0, Kind: "time", Minutes: 90, Text: "travel"},
			{Day: "2025-03-05", At: t0.Add(48 * time.Hour), Kind: "completed"},
			{Day: "2025-03-05", At: t0.Add(48 * time.Hour), Kind: "time_total", Minutes: 600},
		},
	}}
}

func TestBuildMakesOneEntryPerRecordPerDay(t *testing.T) {
	d := Build(sample(), "Acme: service history", "White Team Service / Acme, all dates")
	if len(d.Reports) != 2 {
		t.Fatalf("want 2 entries (two days), got %d", len(d.Reports))
	}
	first := d.Reports[0]
	if !strings.HasPrefix(first.Lead, "2025-03-03") || !strings.Contains(first.Subject, "Drive fault") ||
		!strings.Contains(first.Subject, "[done]") {
		t.Fatalf("first entry heading = %q / %q", first.Lead, first.Subject)
	}
	for _, want := range []string{"Opened.", "Replace the drive.", "Time logged: 1h 30m, travel", "Bob: Found a failed contactor."} {
		if !strings.Contains(first.Body, want) {
			t.Errorf("first entry body lacks %q:\n%s", want, first.Body)
		}
	}
	if !strings.Contains(first.Department, "Assigned to Marco Dell") {
		t.Errorf("assignee missing: %q", first.Department)
	}
	if !strings.Contains(d.Reports[1].Body, "Total time tracked on this job: 10h") {
		t.Errorf("total missing on the last day: %q", d.Reports[1].Body)
	}
	if !strings.Contains(d.Reports[1].Body, "Completed.") {
		t.Errorf("second day lacks Completed: %q", d.Reports[1].Body)
	}
	var out bytes.Buffer
	if err := reportpdf.Write(&out, d); err != nil || !bytes.HasPrefix(out.Bytes(), []byte("%PDF-")) {
		t.Fatalf("pdf: %v", err)
	}
}

func TestCSVHasEveryEventInDayOrder(t *testing.T) {
	var out bytes.Buffer
	if err := WriteCSV(&out, sample()); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][2] != "customer" || rows[0][3] != "end_customer" {
		t.Fatalf("header = %v", rows[0])
	}
	if len(rows) != 6 { // header and five events
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0][0] != "day" || rows[5][10] != "time_total" {
		t.Fatalf("unexpected order: %v", rows)
	}
}
