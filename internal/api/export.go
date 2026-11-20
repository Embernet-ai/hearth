package api

// Export, because a product that cannot be left is not really self-hosted
// whatever the licence says.
//
// # What makes an export real
//
// Not that it produces a file. Any product produces a file. What makes it real
// is that the file goes back in: an export that cannot be re-imported into an
// empty instance is a backup nobody has restored, and everybody who has run
// one of those knows how that ends.
//
// So the format is the importer's own generic shape. Not a bespoke dump format
// with its own reader written once and never exercised, but the same columns
// the generic CSV importer already reads, which means the path back in is a
// path that runs every time somebody leaves another tool.
//
// # Permission filtered
//
// An export contains exactly what the caller can see. That is not a limitation
// to apologise for: an admin exporting everything is an admin whose grants say
// everything, and a token that could dump rows its owner cannot read would be
// the privileged back door this package exists to not have.

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Embernet-ai/hearth/internal/docs"
)

// exportLimit bounds one file. Twenty thousand rows is a large estate, and an
// unbounded export is a way to hold a connection open until something gives.
const exportLimit = 20000

// export streams everything the caller can see, as CSV or JSON.
//
// The columns are the ones the generic importer reads back, deliberately. An
// export whose own product cannot re-import it is a file, not an exit.
func (s *Service) export(w http.ResponseWriter, r *http.Request) {
	c := from(r)
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}

	rows, err := s.docs.Search(r.Context(), c.OrgID, c.Subjects, docs.Query{
		Kind: r.URL.Query().Get("kind"), Limit: exportLimit,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not export")
		return
	}

	stamp := time.Now().Format("2006-01-02")
	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition",
			`attachment; filename="hearth-export-`+stamp+`.json"`)
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, exportRow(row.Document))
		}
		_ = json.NewEncoder(w).Encode(out)

	default:
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition",
			`attachment; filename="hearth-export-`+stamp+`.csv"`)
		cw := csv.NewWriter(w)
		// These header names are the generic importer's aliases. Renaming one
		// here without renaming it there breaks the round trip silently, which
		// is why the acceptance suite exports and re-imports rather than
		// eyeballing the file.
		_ = cw.Write([]string{
			"id", "kind", "title", "body", "status", "priority", "due", "url",
		})
		for _, row := range rows {
			d := row.Document
			status, priority, due := "", "", ""
			if d.Task != nil {
				status = d.Task.Status
				priority = strconv.Itoa(int(d.Task.Priority))
				if d.Task.DueAt != nil {
					due = d.Task.DueAt.Format(time.RFC3339)
				}
			}
			_ = cw.Write([]string{
				d.ID, d.Kind, d.Title, d.Body, status, priority, due, "",
			})
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			// The status line is long gone, so this can only be logged by the
			// caller noticing a truncated file. Nothing better is available.
			_, _ = fmt.Fprintf(w, "\n# export truncated: %v\n", err)
		}
	}
}

func exportRow(d *docs.Document) map[string]any {
	out := map[string]any{
		"id": d.ID, "kind": d.Kind, "title": d.Title, "body": d.Body,
	}
	if d.Task != nil {
		out["status"] = d.Task.Status
		out["priority"] = d.Task.Priority
		if d.Task.DueAt != nil {
			out["due"] = d.Task.DueAt.Format(time.RFC3339)
		}
	}
	return out
}
