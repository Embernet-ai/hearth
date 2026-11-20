// Package importer is leaving the old tool, whichever one it was.
//
// # One engine, five vocabularies
//
// Jira calls it a Summary, Asana calls it a Name, Monday calls it an Item, and
// Trello calls it a Card. Underneath, every one of them exports the same six
// things: an id, a title, some text, a state, a priority, and a date. So there
// is one parser that reads a table, one set of aliases per source that says
// which column is which, and one upsert path.
//
// The alternative is five importers, and the fifth one is written eighteen
// months later by somebody who does not know the first four had the same bug.
//
// # Re-runnable, all of them
//
// Every row that carries an id is matched against external_refs, so running
// the same export twice updates rather than duplicates. That is the property
// people actually need: an import is never right the first time, and one that
// cannot be re-run means starting again with a clean database.
//
// A row with no id is created every time, and that is the honest behaviour
// rather than a bug. There is nothing to recognise it by, and guessing from the
// title would merge two genuinely different jobs called "Service the pump".
package importer

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// maxImport bounds a file. Ten megabytes of CSV is roughly forty thousand
// rows, which is more than anybody has, and reading an unbounded upload into
// memory is how one person takes the process down.
const maxImport = 10 << 20

var (
	ErrEmpty   = errors.New("importer: that export contained no rows")
	ErrUnknown = errors.New("importer: that is not a format this reads")
)

// Sources this understands.
const (
	Jira    = "jira"
	Asana   = "asana"
	Monday  = "monday"
	Trello  = "trello"
	Generic = "csv"
)

// Sources in the order the form offers them.
var Sources = []string{Jira, Asana, Monday, Trello, Generic}

var validSources = map[string]bool{
	Jira: true, Asana: true, Monday: true, Trello: true, Generic: true,
}

// Task is one row, normalised.
type Task struct {
	ExternalID string
	Title      string
	Body       string
	Status     string
	Priority   int16
	Assignees  []string
	DueAt      *time.Time
	List       string
	URL        string
	Parent     string
	// Extra is everything the source had that has no field here. Kept rather
	// than dropped: a column somebody bothered to fill in is a column they
	// will look for afterwards, and losing it silently is how an import gets
	// blamed for data that was never actually lost.
	Extra map[string]string
}

// aliases maps a source's column names onto the fields above.
//
// Lowercased and stripped of punctuation before matching, so "Due Date",
// "due_date", and "DueDate" are the same column. Written out per source rather
// than guessed, because a heuristic that gets Monday's "Person" wrong maps
// every assignee onto the title.
var aliases = map[string]map[string]string{
	Jira: {
		"issuekey": "id", "key": "id", "issueid": "id",
		"summary": "title", "description": "body", "status": "status",
		"priority": "priority", "assignee": "assignee", "duedate": "due",
		"project": "list", "parent": "parent", "parentkey": "parent",
	},
	Asana: {
		"taskid": "id", "id": "id",
		"name": "title", "notes": "body", "description": "body",
		"duedate": "due", "duedon": "due",
		"assignee": "assignee", "projects": "list", "section": "list",
		"parenttask": "parent", "priority": "priority",
		// Asana exports completion as a column rather than a status.
		"completedat": "completed",
	},
	Monday: {
		"id": "id", "itemid": "id",
		"name": "title", "item": "title", "notes": "body",
		"status": "status", "priority": "priority", "person": "assignee",
		"owner": "assignee", "date": "due", "duedate": "due", "group": "list",
	},
	Trello: {
		"cardid": "id", "id": "id", "shortlink": "id",
		"cardname": "title", "name": "title", "title": "title",
		"carddescription": "body", "description": "body", "desc": "body",
		"listname": "status", "list": "status", "labels": "priority",
		"members": "assignee", "duedate": "due", "due": "due",
		"boardname": "list", "cardurl": "url", "url": "url",
	},
	Generic: {
		"id": "id", "externalid": "id", "reference": "id", "ref": "id",
		"title": "title", "name": "title", "subject": "title",
		"body": "body", "description": "body", "details": "body", "notes": "body",
		"status": "status", "state": "status",
		"priority": "priority", "assignee": "assignee", "owner": "assignee",
		"due": "due", "duedate": "due", "url": "url", "parent": "parent",
		"list": "list", "project": "list", "group": "list",
	},
}

// Parse reads an export for one source.
//
// The format is sniffed rather than asked for. CSV and JSON are trivially
// distinguishable and a format dropdown beside a source dropdown is two things
// to get wrong instead of one.
func Parse(source string, r io.Reader) ([]Task, error) {
	if !validSources[source] {
		return nil, fmt.Errorf("%w: %q", ErrUnknown, source)
	}
	data, err := io.ReadAll(io.LimitReader(r, maxImport))
	if err != nil {
		return nil, fmt.Errorf("importer: read: %w", err)
	}
	// The byte order mark is written as an escape rather than pasted in,
	// because a BOM anywhere but the first byte of a Go source file is a
	// compile error even inside a string literal. Excel puts one at the front
	// of every CSV it writes, and without this the first column name never
	// matches an alias, which loses the id column and makes the whole import
	// unre-runnable.
	trimmed := strings.TrimLeft(string(data), " \t\r\n\uFEFF")
	if trimmed == "" {
		return nil, ErrEmpty
	}

	var tasks []Task
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		tasks, err = parseJSON(source, []byte(trimmed))
	} else {
		tasks, err = parseCSV(source, strings.NewReader(trimmed))
	}
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, ErrEmpty
	}
	return tasks, nil
}

func parseCSV(source string, r io.Reader) ([]Task, error) {
	cr := csv.NewReader(r)
	// Exports from every one of these tools have ragged rows, because a
	// description with a newline in it is common and their writers are not
	// careful. Refusing the file over one bad row loses the other 3999.
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("importer: read the header row: %w", err)
	}
	fields := make([]string, len(header))
	for i, h := range header {
		fields[i] = normalise(h)
	}

	var out []Task
	for {
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A row that cannot be read is skipped rather than fatal, for the
			// same reason: 3999 rows imported beats none.
			continue
		}
		values := map[string]string{}
		raw := map[string]string{}
		for i, cell := range row {
			if i >= len(fields) {
				break
			}
			cell = strings.TrimSpace(cell)
			if cell == "" {
				continue
			}
			raw[header[i]] = cell
			if field, ok := aliases[source][fields[i]]; ok {
				// First column wins. Jira exports twelve columns called
				// "Comment" and several called "Labels", and taking the last
				// one means the earliest and usually fullest is discarded.
				if _, seen := values[field]; !seen {
					values[field] = cell
				}
			}
		}
		if t, ok := taskFrom(source, values, raw); ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// parseJSON reads an export that is an array of objects, or an object with one
// array inside it, which is what all five produce in their JSON form.
func parseJSON(source string, data []byte) ([]Task, error) {
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		var wrapper map[string]json.RawMessage
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			return nil, fmt.Errorf("importer: parse JSON: %w", err)
		}
		// Trello nests the cards; Jira nests the issues; Asana nests data.
		found := false
		for _, key := range []string{"cards", "issues", "data", "items", "tasks", "results"} {
			if raw, ok := wrapper[key]; ok {
				if err := json.Unmarshal(raw, &rows); err == nil {
					found = true
					break
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("importer: parse JSON: %w", err)
		}
	}

	var out []Task
	for _, row := range rows {
		values := map[string]string{}
		raw := map[string]string{}
		for key, value := range row {
			text := flatten(value)
			if text == "" {
				continue
			}
			raw[key] = text
			if field, ok := aliases[source][normalise(key)]; ok {
				if _, seen := values[field]; !seen {
					values[field] = text
				}
			}
		}
		if t, ok := taskFrom(source, values, raw); ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// taskFrom assembles one row. It reports false for a row with no title, which
// is a blank line or a trailing separator rather than a task.
func taskFrom(source string, v, raw map[string]string) (Task, bool) {
	title := strings.TrimSpace(v["title"])
	if title == "" {
		return Task{}, false
	}
	t := Task{
		ExternalID: v["id"],
		Title:      title,
		Body:       v["body"],
		Status:     MapStatus(source, v["status"], v["completed"]),
		Priority:   MapPriority(source, v["priority"]),
		DueAt:      parseTime(v["due"]),
		List:       v["list"],
		URL:        v["url"],
		Parent:     v["parent"],
	}
	if a := v["assignee"]; a != "" {
		t.Assignees = splitList(a)
	}
	// Everything the aliases did not claim, so a column somebody filled in is
	// still there afterwards.
	t.Extra = map[string]string{}
	for key, value := range raw {
		if _, claimed := aliases[source][normalise(key)]; !claimed && len(t.Extra) < 20 {
			t.Extra[key] = value
		}
	}
	return t, true
}

// MapStatus turns whatever the source called it into one of the five.
//
// Unknown becomes todo rather than being dropped or refused. A status this does
// not recognise is a column somebody renamed, and losing the row over it is the
// wrong trade: a work order in the wrong column can be moved, and one that
// never imported has to be found first.
func MapStatus(source, status, completed string) string {
	if completed != "" && !strings.EqualFold(completed, "false") {
		return "done"
	}
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case s == "":
		return "todo"
	case containsAny(s, "done", "complete", "closed", "resolved", "finished", "shipped"):
		return "done"
	case containsAny(s, "cancel", "won't", "wont", "abandon", "rejected", "duplicate"):
		return "cancelled"
	case containsAny(s, "block", "waiting", "on hold", "impediment", "stuck"):
		return "blocked"
	case containsAny(s, "progress", "doing", "started", "review", "testing", "active"):
		return "in_progress"
	default:
		return "todo"
	}
}

// MapPriority normalises a priority onto zero to three.
//
// Higher is more urgent here, which is the opposite of Jira's numbering, where
// 1 is Highest. Passing it through would make every critical issue read as low
// and every trivial one as urgent, which is the sort of thing nobody notices
// until a shutdown.
func MapPriority(source, v string) int16 {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "" {
		return 1
	}
	switch {
	case containsAny(s, "urgent", "critical", "highest", "blocker", "p0", "immediate"):
		return 3
	case containsAny(s, "high", "major", "p1", "important"):
		return 2
	case containsAny(s, "low", "minor", "trivial", "p3", "p4", "lowest"):
		return 0
	case containsAny(s, "normal", "medium", "p2"):
		return 1
	}
	// A bare number. Jira and Monday both do this, and both count downwards.
	if n, err := strconv.Atoi(s); err == nil {
		switch source {
		case Jira, Monday:
			switch {
			case n <= 1:
				return 3
			case n == 2:
				return 2
			case n == 3:
				return 1
			default:
				return 0
			}
		default:
			if n < 0 {
				return 0
			}
			if n > 3 {
				return 3
			}
			return int16(n)
		}
	}
	return 1
}

// -----------------------------------------------------------------------------

// normalise reduces a column name to something comparable.
func normalise(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// flatten turns a JSON value into text. An array becomes a comma-separated
// list, an object becomes its name or its id, because that is what every one of
// these exports nests inside one.
func flatten(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s := flatten(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		// The keys these tools actually nest a name under. Trello uses
		// fullName, Jira uses displayName, Monday uses text, and every one of
		// them falls back to an id. Missing one turns an assignee into an
		// empty string, which reads as "unassigned" and is worse than a name
		// nobody recognises.
		for _, key := range []string{
			"name", "fullName", "displayName", "username", "text",
			"title", "value", "emailAddress", "id", "key",
		} {
			if inner, ok := t[key]; ok {
				if s := flatten(inner); s != "" {
					return s
				}
			}
		}
		return ""
	default:
		return ""
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '|'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseTime accepts the handful of shapes these tools emit.
func parseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Milliseconds since the epoch, which Trello and Monday both use.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1_000_000_000 {
		if n > 100_000_000_000 {
			n /= 1000
		}
		t := time.Unix(n, 0).UTC()
		return &t
	}
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05",
		"2006-01-02T15:04:05", "2006-01-02", "02/01/2006", "01/02/2006",
		"2 Jan 2006", "Mon Jan 2 15:04:05 2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
