package crmsync

// Notes and calls as Comments, one way.
//
// Hearth's timeline is where people write down a call or a note about a company, a person or a deal. EmberOps gets
// each one as a Comment on the matching record, so somebody working a Customer, Lead, Contact or Opportunity in
// EmberOps reads what was said. Hearth owns the text: a Comment edited in EmberOps is put back, and one for an
// activity that was removed in Hearth is left as it is. A note about several records is one Comment on each.
//
// A Comment's key is "<activity id>:<record id>", so sending it twice is one Comment. The Comments EmberOps already
// holds are read in one list and only a new or changed one is sent. If EmberOps refuses the first Comment (it has not
// been given the field yet) the pass stops there, with one line, and not one failure per note.

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

type activity struct {
	ID, Kind, Title, Body, Direction, Disposition, Author string
	At                                                    time.Time
	DurationMS                                            int64
	TargetType, TargetID                                  string
}

func (s *Syncer) activities(ctx context.Context) ([]activity, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id::text, a.kind, a.title, a.body, a.occurred_at, coalesce(a.direction, ''), a.disposition,
		       coalesce(a.duration_ms, 0), coalesce(p.display_name, ''), t.target_type, t.target_id::text
		FROM crm_activities a
		JOIN crm_activity_targets t ON t.activity_id = a.id
		LEFT JOIN principals p ON p.id = a.created_by
		WHERE a.org_id = $1 AND a.deleted_at IS NULL
		ORDER BY a.occurred_at, a.id, t.target_type, t.target_id`, s.org)
	if err != nil {
		return nil, fmt.Errorf("crmsync: read timeline: %w", err)
	}
	defer rows.Close()
	var out []activity
	for rows.Next() {
		var a activity
		if err := rows.Scan(&a.ID, &a.Kind, &a.Title, &a.Body, &a.At, &a.Direction, &a.Disposition, &a.DurationMS,
			&a.Author, &a.TargetType, &a.TargetID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// commentHTML is what a note or call looks like in EmberOps: a heading line that says what it was, then the words.
func commentHTML(a activity) string {
	head := "Hearth note"
	if a.Kind == "call" {
		head = "Hearth call"
		if a.Direction != "" {
			head += ", " + a.Direction
		}
		if a.DurationMS > 0 {
			head += fmt.Sprintf(", %d min", (a.DurationMS+59999)/60000)
		}
		if strings.TrimSpace(a.Disposition) != "" {
			head += ", " + strings.TrimSpace(a.Disposition)
		}
	}
	when := a.At.UTC().Format("2006-01-02 15:04") + " UTC"
	if a.Author != "" {
		when += ", " + a.Author
	}
	out := "<p><strong>" + html.EscapeString(head) + "</strong><br>" + html.EscapeString(when) + "</p>"
	for _, part := range []string{a.Title, a.Body} {
		if t := strings.TrimSpace(part); t != "" {
			out += "<p>" + strings.ReplaceAll(html.EscapeString(t), "\n", "<br>") + "</p>"
		}
	}
	return out
}

// recordFor is the EmberOps record a timeline target is shown on: a company's Customer, else its Supplier, else
// its Lead; a person's Contact; a deal's Opportunity. It says nothing when EmberOps has none.
func (s *Syncer) recordFor(ctx context.Context, targetType, id string) (dt, name string) {
	var order []string
	switch targetType {
	case "company":
		order = []string{"Customer", "Supplier", "Lead"}
	case "contact":
		order = []string{"Contact"}
	case "deal":
		order = []string{"Opportunity"}
	}
	for _, d := range order {
		if row, ok, err := s.known(ctx, d, id); err == nil && ok {
			if n, _ := row["name"].(string); n != "" {
				return d, n
			}
		}
	}
	return "", ""
}

func (s *Syncer) timeline(ctx context.Context, rep *Report, write bool) {
	// Records were made earlier in this run, so read what EmberOps has now.
	for _, dt := range []string{"Customer", "Supplier", "Lead", "Contact", "Opportunity"} {
		delete(s.idx, dt)
		delete(s.byID, dt)
	}
	acts, err := s.activities(ctx)
	if err != nil {
		s.add(rep, Action{"comment", "", "timeline", "failed", err.Error()})
		return
	}
	existing, err := s.ops.ListAll(ctx, emberops.ListQuery{Doctype: "Comment",
		Fields:  []string{"name", "hearth_id", "content", "reference_doctype", "reference_name", "comment_by", "comment_type"},
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "is", Value: "set"}}}, readAll)
	if err != nil {
		s.add(rep, Action{"comment", "", "timeline", "failed", "read the Comments EmberOps has: " + err.Error()})
		return
	}
	have := make(map[string]emberops.Row, len(existing))
	for _, r := range existing {
		if h, _ := r["hearth_id"].(string); h != "" {
			have[h] = r
		}
	}
	for _, a := range acts {
		dt, name := s.recordFor(ctx, a.TargetType, a.TargetID)
		if dt == "" {
			continue
		}
		key := a.ID + ":" + a.TargetID
		doc := map[string]any{"comment_type": "Comment", "reference_doctype": dt, "reference_name": name,
			"content": commentHTML(a)}
		if a.Author != "" {
			doc["comment_by"] = a.Author
		}
		row, ok := have[key]
		verb := "create"
		if ok {
			verb = "update"
			if sameDoc(doc, row, func(string, string) string { return "" }) {
				s.add(rep, Action{"comment", key, name, "same", ""})
				continue
			}
		}
		if !write {
			s.add(rep, Action{"comment", key, name, "would " + verb, dt})
			continue
		}
		if _, err := s.send(ctx, "Comment", key, doc); err != nil {
			s.add(rep, Action{"comment", key, name, "failed", "EmberOps refused the Comment, so the timeline pass stopped: " + err.Error()})
			return
		}
		s.add(rep, Action{"comment", key, name, verb, dt})
	}
}
