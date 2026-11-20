// Package servicereport turns a department's service history into the two
// things somebody hands a customer: a PDF, drawn by the same code as the daily
// reports, and a CSV of every event.
//
// It is shared by the web page and by hearthctl, so the files an engineer
// downloads and the files generated in bulk are the same document.
package servicereport

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/reportpdf"
)

// Bounds keep one PDF something a person can open and email.
const (
	maxBodyRunes    = 2500
	maxCommentRunes = 1500
)

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + " ..."
}

// oneLine flattens a comment so it sits as one bullet.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

func hours(minutes int) string {
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh %dm", minutes/60, minutes%60)
}

func clock(t time.Time) string {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc = time.UTC
	}
	return t.In(loc).Format("3:04 PM")
}

// Build lays records out as one entry per record per day, oldest day first.
func Build(recs []*docs.ServiceRecord, heading, sub string) reportpdf.Doc {
	type key struct{ day, id string }
	byKey := map[key][]docs.ServiceEvent{}
	rec := map[string]*docs.ServiceRecord{}
	var keys []key
	for _, r := range recs {
		rec[r.ID] = r
		for _, e := range r.Events {
			k := key{e.Day, r.ID}
			if _, seen := byKey[k]; !seen {
				keys = append(keys, k)
			}
			byKey[k] = append(byKey[k], e)
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].day != keys[j].day {
			return keys[i].day < keys[j].day
		}
		a, b := rec[keys[i].id], rec[keys[j].id]
		if a.SpacePath != b.SpacePath {
			return a.SpacePath < b.SpacePath
		}
		return a.Opened.Before(b.Opened)
	})

	doc := reportpdf.Doc{Heading: heading, Subheading: sub, Generated: time.Now(), Unit: "day record"}
	for _, k := range keys {
		r := rec[k.id]
		var b strings.Builder
		for _, e := range byKey[k] {
			switch e.Kind {
			case "opened":
				b.WriteString("- Opened.\n")
				if r.Body != "" {
					b.WriteString("\n" + clip(r.Body, maxBodyRunes) + "\n\n")
				}
			case "completed":
				b.WriteString("- Completed.\n")
			case "time":
				line := "- Time logged: " + hours(e.Minutes)
				if e.Text != "" {
					line += ", " + oneLine(e.Text)
				}
				b.WriteString(line + "\n")
			case "time_total":
				b.WriteString("- Total time tracked on this job: " + hours(e.Minutes) + "\n")
			case "comment":
				b.WriteString(fmt.Sprintf("- %s, %s: %s\n", clock(e.At), e.Who, clip(oneLine(e.Text), maxCommentRunes)))
			}
		}
		subject := r.Title
		if r.Status != "" {
			subject += "  [" + r.Status + "]"
		}
		who := "Not assigned"
		if len(r.Assignees) > 0 {
			who = "Assigned to " + strings.Join(r.Assignees, ", ")
		}
		doc.Reports = append(doc.Reports, reportpdf.Report{
			Lead:       k.day + "  " + r.Job,
			Department: r.SpacePath + "   |   " + who,
			Subject:    subject,
			Body:       b.String(),
		})
	}
	return doc
}

// WriteCSV writes every event, one per row, with the record it belongs to.
func WriteCSV(w io.Writer, recs []*docs.ServiceRecord) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"day", "time", "customer", "end_customer", "department", "job", "record_kind", "title", "status",
		"assigned_to", "event", "who", "minutes", "text", "record_id"}); err != nil {
		return err
	}
	type row struct {
		day, at string
		cells   []string
	}
	var rows []row
	for _, r := range recs {
		for _, e := range r.Events {
			text := e.Text
			if e.Kind == "opened" {
				text = r.Body
			}
			mins := ""
			if e.Kind == "time" {
				mins = fmt.Sprint(e.Minutes)
			}
			rows = append(rows, row{e.Day, e.At.Format(time.RFC3339), []string{
				e.Day, clock(e.At), r.Customer, r.EndCustomer, r.SpacePath, r.Job, r.Kind, r.Title, r.Status,
				strings.Join(r.Assignees, "; "), e.Kind, e.Who, mins, text, r.ID}})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].day != rows[j].day {
			return rows[i].day < rows[j].day
		}
		return rows[i].at < rows[j].at
	})
	for _, r := range rows {
		if err := cw.Write(r.cells); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
