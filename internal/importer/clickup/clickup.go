// Package clickup imports a ClickUp export into the entity spine.
//
// ClickUp exports two ways and people bring whichever one they clicked: a CSV
// from the list view, and a JSON blob from the API or the backup. Both are
// accepted here, because telling somebody their export is the wrong shape is
// not an answer when the tool gave them that file.
//
// A task becomes a document with a task kind. That is the whole mapping, and it
// is only this small because the spine already carries status, priority,
// assignee, and due date. Nothing about ClickUp leaks past this package.
package clickup

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

// Task is the normalised shape both formats reduce to.
type Task struct {
	ExternalID string
	Title      string
	Body       string
	Status     string
	Priority   int16
	Assignees  []string
	DueAt      *time.Time
	StartAt    *time.Time
	List       string
	URL        string
	Parent     string
	Tags       []string
	// Estimate and Spent are what the source tracked against this task. Kept as
	// durations rather than the milliseconds the export carries, because every
	// consumer of them wants a duration and converting once here is the only
	// place the unit can be got wrong.
	Estimate time.Duration
	Spent    time.Duration
	// Checklists are flattened to their items. ClickUp nests them under a
	// named checklist per task; Hearth has one list per document, so the
	// checklist's name is prefixed onto its items rather than lost -- a task
	// with "Prep" and "Teardown" checklists reads wrong as one flat run of
	// steps with no idea which phase they belong to.
	Checklist []ChecklistItem

	// The source's own history. Without these every imported document claims
	// to have been created on migration day, which sorts every list wrong and
	// makes "recently updated" meaningless across the whole workspace.
	CreatedAt   *time.Time
	UpdatedAt   *time.Time
	CompletedAt *time.Time
	// Creator is the address of whoever raised it there, so it can be
	// attributed to them here rather than to whoever ran the import.
	Creator string
	// CustomID is the human-facing identifier, like DEV-1234. It is the number
	// people say out loud, and it is not the id the API uses.
	CustomID string
	// DependsOn is the tasks this one is waiting on, and Blocks the ones it
	// holds up. Both become links here.
	DependsOn []string
	Blocks    []string
	// Linked is a plain association rather than an ordering.
	Linked []string
	// Watchers are the addresses following this task.
	Watchers []string
	// Fields are the custom field values that were actually SET. A definition
	// with no value is not carried on the task: the definitions travel
	// separately, and repeating all sixty-two on every task would make an
	// export mostly nulls.
	Fields []FieldValue
}

// ChecklistItem is one step, and whether it is ticked.
type ChecklistItem struct {
	Text string
	Done bool
}

// FieldValue is one custom field's value on one task, carrying enough of its
// definition to be stored and rendered without a second lookup into the source.
//
// Value is deliberately `any`. The types run from a string to a currency amount
// to a list of option ids, and flattening them all to text here would throw
// away the distinction that makes a number sortable and a date a date.
type FieldValue struct {
	FieldID string
	Name    string
	// Type is normalised to this application's vocabulary, not ClickUp's. The
	// mapping lives in normaliseType and it is the only place a vendor's type
	// names appear.
	Type string
	// Options are the choices a dropdown or label field offers, in the order
	// the source lists them. Carried across because a select with no options
	// is a text box wearing a costume: nothing can validate it, nothing can
	// filter on it, and the nineteen dropdowns in this workspace would all
	// have arrived that way.
	Options []string
	Value   any
}

var ErrEmpty = errors.New("clickup: the export contained no tasks")

// Parse sniffs the format and returns normalised tasks.
//
// Sniffing rather than asking, because the two exports are trivially
// distinguishable and a format dropdown is one more thing to get wrong.
func Parse(r io.Reader) ([]Task, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxImport))
	if err != nil {
		return nil, fmt.Errorf("clickup: read: %w", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, ErrEmpty
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return parseJSON([]byte(trimmed))
	}
	return parseCSV(strings.NewReader(trimmed))
}

// maxImport bounds what a single upload can be. Large enough for a real
// workspace export, small enough that it cannot be used to exhaust memory.
const maxImport = 64 << 20

// ---------------------------------------------------------------------------
// CSV
// ---------------------------------------------------------------------------

func parseCSV(r io.Reader) ([]Task, error) {
	cr := csv.NewReader(r)
	// ClickUp writes ragged rows when a custom field is missing from some tasks,
	// so a strict field count rejects perfectly good exports.
	cr.FieldsPerRecord = -1

	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("clickup: read header: %w", err)
	}
	idx := make(map[string]int, len(head))
	for i, h := range head {
		idx[normaliseHeader(h)] = i
	}

	get := func(rec []string, names ...string) string {
		for _, n := range names {
			if i, ok := idx[n]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
		}
		return ""
	}

	var out []Task
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("clickup: read row: %w", err)
		}

		t := Task{
			ExternalID: get(rec, "taskid", "id"),
			Title:      get(rec, "taskname", "name", "task"),
			Body:       get(rec, "taskcontent", "description", "content"),
			Status:     get(rec, "status"),
			List:       get(rec, "listname", "list"),
			URL:        get(rec, "taskurl", "url"),
			Parent:     get(rec, "parenttaskid", "parent"),
		}
		if t.Title == "" && t.ExternalID == "" {
			continue // a trailing blank line, not a task
		}
		t.Priority = parsePriority(get(rec, "priority"))
		t.DueAt = parseTime(get(rec, "duedate", "duedatetext", "due"))
		if a := get(rec, "assignees", "assignee"); a != "" {
			t.Assignees = splitList(a)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

// normaliseHeader flattens "Task Name", "task_name", and "TaskName" to one key,
// because the export headers differ between ClickUp views and versions.
func normaliseHeader(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// JSON
// ---------------------------------------------------------------------------

// jsonTask mirrors only the fields that map onto something real. Everything
// else in a ClickUp export is deliberately ignored rather than stored, because
// data nothing reads is data that goes stale and misleads.
type jsonTask struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	TextContent string `json:"text_content"`
	URL         string `json:"url"`
	Status      struct {
		Status string `json:"status"`
	} `json:"status"`
	// Priority is an object when set and null when not, which is why it cannot
	// be a plain string.
	Priority *struct {
		Priority string `json:"priority"`
	} `json:"priority"`
	Assignees []struct {
		Username string `json:"username"`
		Email    string `json:"email"`
	} `json:"assignees"`
	// Timestamps are unix milliseconds as a STRING. Not a number.
	DueDate string `json:"due_date"`
	List    struct {
		Name string `json:"name"`
	} `json:"list"`
	Parent   string `json:"parent"`
	CustomID string `json:"custom_id"`
	// Unix milliseconds as strings, like every other date in this export.
	DateCreated  string `json:"date_created"`
	DateUpdated  string `json:"date_updated"`
	DateClosed   string `json:"date_closed"`
	DateDone     string `json:"date_done"`
	Dependencies []struct {
		TaskID    string `json:"task_id"`
		DependsOn string `json:"depends_on"`
		Type      int    `json:"type"`
	} `json:"dependencies"`
	LinkedTasks []struct {
		TaskID string `json:"task_id"`
		LinkID string `json:"link_id"`
	} `json:"linked_tasks"`
	Watchers []struct {
		Username string `json:"username"`
		Email    string `json:"email"`
	} `json:"watchers"`
	Creator struct {
		Username string `json:"username"`
		Email    string `json:"email"`
	} `json:"creator"`

	StartDate string `json:"start_date"`
	Tags      []struct {
		Name string `json:"name"`
	} `json:"tags"`
	// Estimates and tracked time are milliseconds, and the export is
	// inconsistent about whether they arrive as a number or a string. A
	// json.Number takes both without a custom unmarshaller.
	TimeEstimate json.Number `json:"time_estimate"`
	TimeSpent    json.Number `json:"time_spent"`
	Checklists   []struct {
		Name  string `json:"name"`
		Items []struct {
			Name     string `json:"name"`
			Resolved bool   `json:"resolved"`
		} `json:"items"`
	} `json:"checklists"`
	CustomFields []struct {
		ID         string          `json:"id"`
		Name       string          `json:"name"`
		Type       string          `json:"type"`
		Value      json.RawMessage `json:"value"`
		TypeConfig struct {
			Options []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Label string `json:"label"`
			} `json:"options"`
		} `json:"type_config"`
	} `json:"custom_fields"`
}

func parseJSON(data []byte) ([]Task, error) {
	// Three shapes in the wild: a bare array, {"tasks": [...]}, and the backup
	// format that nests them under a list.
	var envelope struct {
		Tasks []jsonTask `json:"tasks"`
	}
	var bare []jsonTask

	var raw []jsonTask
	switch {
	case json.Unmarshal(data, &envelope) == nil && len(envelope.Tasks) > 0:
		raw = envelope.Tasks
	case json.Unmarshal(data, &bare) == nil && len(bare) > 0:
		raw = bare
	default:
		return nil, fmt.Errorf("clickup: unrecognised JSON export shape")
	}

	out := make([]Task, 0, len(raw))
	for _, j := range raw {
		body := j.Description
		if body == "" {
			body = j.TextContent
		}
		t := Task{
			ExternalID: j.ID,
			Title:      j.Name,
			Body:       body,
			Status:     j.Status.Status,
			URL:        j.URL,
			List:       j.List.Name,
			Parent:     j.Parent,
		}
		if j.Priority != nil {
			t.Priority = parsePriority(j.Priority.Priority)
		}
		t.DueAt = parseUnixMillis(j.DueDate)
		t.CreatedAt = parseUnixMillis(j.DateCreated)
		t.UpdatedAt = parseUnixMillis(j.DateUpdated)
		// date_done is when it was marked complete; date_closed is when the
		// status moved into a closed one. Either answers "when did this
		// finish", and done is the more specific of the two.
		if t.CompletedAt = parseUnixMillis(j.DateDone); t.CompletedAt == nil {
			t.CompletedAt = parseUnixMillis(j.DateClosed)
		}
		t.CustomID = strings.TrimSpace(j.CustomID)
		for _, d := range j.Dependencies {
			// ClickUp reports the pair from both sides. The row where this
			// task is the dependant tells us what it waits on; the other tells
			// us what it blocks.
			switch {
			case d.TaskID == j.ID && d.DependsOn != "":
				t.DependsOn = append(t.DependsOn, d.DependsOn)
			case d.DependsOn == j.ID && d.TaskID != "":
				t.Blocks = append(t.Blocks, d.TaskID)
			}
		}
		for _, l := range j.LinkedTasks {
			if l.TaskID != "" && l.TaskID != j.ID {
				t.Linked = append(t.Linked, l.TaskID)
			}
		}
		for _, w := range j.Watchers {
			if w.Email != "" {
				t.Watchers = append(t.Watchers, strings.ToLower(w.Email))
			} else if w.Username != "" {
				t.Watchers = append(t.Watchers, strings.ToLower(w.Username))
			}
		}
		if j.Creator.Email != "" {
			t.Creator = j.Creator.Email
		} else {
			t.Creator = j.Creator.Username
		}
		t.StartAt = parseUnixMillis(j.StartDate)
		t.Estimate = parseMillisDuration(j.TimeEstimate)
		t.Spent = parseMillisDuration(j.TimeSpent)
		for _, tag := range j.Tags {
			if tag.Name != "" {
				t.Tags = append(t.Tags, tag.Name)
			}
		}
		for _, cl := range j.Checklists {
			for _, it := range cl.Items {
				text := strings.TrimSpace(it.Name)
				if text == "" {
					continue
				}
				if n := strings.TrimSpace(cl.Name); n != "" {
					text = n + ": " + text
				}
				t.Checklist = append(t.Checklist, ChecklistItem{Text: text, Done: it.Resolved})
			}
		}
		for _, f := range j.CustomFields {
			v := decodeFieldValue(f.Value)
			if v == nil {
				// Not set. ClickUp returns the definition on every task
				// whether or not anybody filled it in, and carrying those
				// would turn 853 real values into tens of thousands of nulls.
				continue
			}
			opts := make([]string, 0, len(f.TypeConfig.Options))
			for _, o := range f.TypeConfig.Options {
				// ClickUp calls it name on a dropdown and label on a label
				// field. Same thing to a reader.
				if n := o.Name; n != "" {
					opts = append(opts, n)
				} else if o.Label != "" {
					opts = append(opts, o.Label)
				}
			}
			t.Fields = append(t.Fields, FieldValue{
				FieldID: f.ID,
				Name:    f.Name,
				Type:    normaliseType(f.Type),
				Options: opts,
				Value:   v,
			})
		}
		for _, a := range j.Assignees {
			if a.Username != "" {
				t.Assignees = append(t.Assignees, a.Username)
			} else if a.Email != "" {
				t.Assignees = append(t.Assignees, a.Email)
			}
		}
		if t.Title == "" && t.ExternalID == "" {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Field mapping
// ---------------------------------------------------------------------------

// MapStatus turns a ClickUp status into one of ours.
//
// ClickUp statuses are user-defined, so this cannot be a lookup table. It
// matches on the status TYPE words people actually use, and anything unknown
// becomes todo rather than being dropped. Importing a task into the wrong
// column is recoverable; not importing it at all is not.
func MapStatus(s string) string {
	switch n := strings.ToLower(strings.TrimSpace(s)); {
	case n == "":
		return "todo"
	// Negations first, and this is not hypothetical tidiness. "not started" is
	// the default open status in five of the eight spaces this was written
	// against -- roughly 700 tasks -- and it CONTAINS "started", so the
	// in-progress rule below claimed it and imported every one of them as work
	// somebody had begun. A substring match cannot see a negation, so the
	// negations have to be read first.
	case containsAny(n, "not started", "not-started", "notstarted",
		"not begun", "never started"):
		return "todo"
	case containsAny(n, "complete", "done", "closed", "resolved", "shipped"):
		return "done"
	case containsAny(n, "cancel", "won't do", "wont do", "rejected", "duplicate", "archived"):
		return "cancelled"
	case containsAny(n, "block", "waiting", "on hold", "hold", "paused", "stuck"):
		return "blocked"
	case containsAny(n, "progress", "doing", "active", "review", "testing", "started"):
		return "in_progress"
	default:
		return "todo"
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

// parsePriority maps ClickUp's four levels onto a number where higher is more
// urgent. ClickUp stores 1 as urgent and 4 as low, which is backwards from
// every intuition, so it is inverted here rather than confusing every reader
// of the list view forever.
func parsePriority(s string) int16 {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "urgent", "1":
		return 3
	case "high", "2":
		return 2
	case "normal", "medium", "3":
		return 1
	case "low", "4":
		return 0
	default:
		return 0
	}
}

func parseUnixMillis(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" || s == "0" {
		return nil
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

// parseTime accepts the several date shapes the CSV export uses depending on
// workspace locale, and unix millis, since the CSV sometimes carries those too.
func parseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "null") {
		return nil
	}
	if t := parseUnixMillis(s); t != nil && len(s) >= 10 && !strings.ContainsAny(s, "-/: ") {
		return t
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
		"01/02/2006",
		"01/02/2006 15:04",
		"Mon Jan 2 15:04:05 2006",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.Trim(strings.TrimSpace(f), `"[]`); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Custom fields
// ---------------------------------------------------------------------------

// normaliseType maps ClickUp's type vocabulary onto property_defs.data_type.
//
// The mapping is lossy on purpose. ClickUp distinguishes `short_text` from
// `text` by the height of the input box and `automatic_progress` from
// `manual_progress` by who moves the slider. Neither difference survives being
// stored, and pretending otherwise means every consumer switches on seventeen
// cases to render seven.
//
// The types that fall through to text -- location, progress, formula,
// attachment, signature, relation -- do so because nothing renders them
// differently yet. They keep their value, which is the part that matters; a
// type nothing reads is a longer CHECK constraint and no more.
//
// An unrecognised type becomes text rather than an error. A field this code has
// never seen still holds something somebody typed, and dropping it because the
// vendor shipped a feature is the wrong trade.
func normaliseType(t string) string {
	switch t {
	case "number":
		return "number"
	case "currency":
		return "currency"
	case "date":
		return "date"
	case "drop_down", "labels":
		return "select"
	case "checkbox":
		return "boolean"
	case "url":
		return "url"
	case "email":
		return "email"
	case "phone":
		return "phone"
	case "users":
		return "principal"
	default:
		return "text"
	}
}

// decodeFieldValue turns a raw custom field value into something storable, and
// returns nil for every shape that means "nobody filled this in".
//
// The empty cases are not all the same shape and that is the point: ClickUp
// omits the key, sends null, sends "", and sends [] depending on the type, and
// a check for only one of them lets the other three through as real values.
func decodeFieldValue(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Undecodable is still evidence somebody set something, so it is kept
		// as the literal text rather than dropped.
		return strings.TrimSpace(string(raw))
	}
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return x
	case []any:
		if len(x) == 0 {
			return nil
		}
		return x
	case map[string]any:
		if len(x) == 0 {
			return nil
		}
		return x
	}
	return v
}

// parseMillisDuration reads the milliseconds the export uses for estimates and
// tracked time. Zero and absent are the same thing here -- a task with no time
// on it -- so neither is distinguished.
func parseMillisDuration(n json.Number) time.Duration {
	if n == "" {
		return 0
	}
	ms, err := n.Int64()
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
