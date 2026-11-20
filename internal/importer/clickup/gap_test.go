package clickup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// world is a tiny Hearth: enough to run the gap import against and look at what
// it wrote. One struct plays the document store, the lookups and the writers,
// so a second Apply sees what the first one did.
type world struct {
	n        int
	docs     map[string]*wdoc
	refs     map[string]string // "document:clickupid" -> doc id
	comments map[string]bool   // "comment:<id>"
	files    map[string]string // attachment id -> file id
	filesLog []string
	times    []wtime
	totals   map[string][]string
	retired  map[string]bool
	people   map[string]string // active email/name -> principal
	all      []Person
	spaces   map[string]string
	posted   []wcomment
	updates  []string
	assigned map[string][]string
}

type wdoc struct {
	id, title, status string
	updated           time.Time
	version           int64
}

type wtime struct {
	id, doc, who, note string
	minutes            int
}
type wcomment struct{ doc, author, body string }

func newWorld() *world {
	return &world{
		docs: map[string]*wdoc{}, refs: map[string]string{}, comments: map[string]bool{},
		files: map[string]string{}, totals: map[string][]string{}, retired: map[string]bool{},
		people: map[string]string{
			"nina@x.test": "p-nina", "nina alvarez": "p-nina",
			"yara@x.test": "p-yara", "yara stone": "p-yara",
		},
		all: []Person{
			{PrincipalID: "p-nina", DisplayName: "Nina Alvarez", Email: "nina@x.test"},
			{PrincipalID: "p-yara", DisplayName: "Yara Stone", Email: "yara@x.test"},
		},
		spaces: map[string]string{}, assigned: map[string][]string{},
	}
}

func (w *world) next(p string) string { w.n++; return fmt.Sprintf("%s-%d", p, w.n) }

// --- Store ---
func (w *world) Create(_ context.Context, _ string, _ core.Subjects, p docs.CreateParams) (*docs.Document, error) {
	id := w.next("doc")
	w.docs[id] = &wdoc{id: id, title: p.Title, status: statusOf(p), updated: time.Now(), version: 1}
	return &docs.Document{ID: id, Kind: p.Kind, Title: p.Title}, nil
}
func statusOf(p docs.CreateParams) string {
	if p.Task != nil {
		return p.Task.Status
	}
	return ""
}
func (w *world) Update(_ context.Context, _ string, _ core.Subjects, id string, p docs.UpdateParams) (*docs.Document, error) {
	d := w.docs[id]
	if p.Title != nil {
		d.title = *p.Title
	}
	if p.Task != nil && p.Task.Status != nil {
		d.status = *p.Task.Status
	}
	d.version++
	if p.UpdatedAt != nil {
		d.updated = *p.UpdatedAt
	}
	w.updates = append(w.updates, id)
	return &docs.Document{ID: id}, nil
}
func (w *world) FindByExternalID(_ context.Context, _, _, ext string) (string, error) {
	return w.refs["document:"+ext], nil
}
func (w *world) RecordExternalRef(_ context.Context, _ string, ref core.Ref, e docs.ExternalRef) error {
	switch ref.Type {
	case core.EntityMessage:
		w.comments[e.ExternalID] = true
	case core.EntityFile:
		w.files[strings.TrimPrefix(e.ExternalID, attachmentRefPrefix)] = ref.ID
	default:
		w.refs["document:"+e.ExternalID] = ref.ID
	}
	return nil
}
func (w *world) Link(context.Context, string, core.Subjects, core.Ref, core.Ref, string) error {
	return nil
}
func (w *world) SetParent(context.Context, string, core.Subjects, string, string) error { return nil }
func (w *world) UpsertImportedProperty(_ context.Context, _ string, d docs.ImportedProperty) (string, error) {
	return "prop-" + d.ExternalID, nil
}
func (w *world) CreateSpace(_ context.Context, _ string, _ core.Subjects, name, _ string, _ *string) (string, error) {
	if id, ok := w.spaces[name]; ok {
		return id, nil
	}
	id := w.next("space")
	w.spaces[name] = id
	return id, nil
}
func (w *world) FindSpaceByName(_ context.Context, _, name string) (string, error) {
	return w.spaces[name], nil
}
func (w *world) People(context.Context, string) ([]Person, error) { return w.all, nil }
func (w *world) ProvisionPerson(context.Context, string, string, string, string) (string, error) {
	return "", nil
}
func (w *world) LogTime(_ context.Context, _ string, _ core.Subjects, docID string, minutes int, _ time.Time, note string) error {
	id := w.next("agg")
	w.totals[docID] = append(w.totals[docID], id)
	return nil
}
func (w *world) ClearImportedTime(context.Context, string, string, string) error { return nil }
func (w *world) SetEstimate(context.Context, string, core.Subjects, string, *int) error {
	return nil
}
func (w *world) AddChecklistItem(context.Context, string, core.Subjects, string, string) (string, error) {
	return "item", nil
}
func (w *world) SetChecklistItemDone(context.Context, string, core.Subjects, string, bool) error {
	return nil
}
func (w *world) PostComment(_ context.Context, _, principal, docID, body string, _ time.Time, _ string) (string, error) {
	w.posted = append(w.posted, wcomment{docID, principal, body})
	return w.next("msg"), nil
}
func (w *world) Watch(context.Context, string, string) error { return nil }
func (w *world) SetAssignees(context.Context, string, []string, string) error {
	return nil
}
func (w *world) SetProperties(context.Context, string, core.Subjects, string, map[string]string) error {
	return nil
}

// --- GapData ---
func (w *world) TaskDocs(context.Context) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range w.refs {
		out[strings.TrimPrefix(k, "document:")] = v
	}
	return out, nil
}
func (w *world) DocUpdated(context.Context) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for id, d := range w.docs {
		out[id] = d.updated
	}
	return out, nil
}
func (w *world) DocVersions(context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for id, d := range w.docs {
		out[id] = d.version
	}
	return out, nil
}
func (w *world) KnownComments(context.Context) (map[string]bool, error) { return w.comments, nil }
func (w *world) KnownAttachments(context.Context) (map[string]string, error) {
	return w.files, nil
}
func (w *world) KnownTimeEntries(context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	for _, t := range w.times {
		rest := strings.TrimPrefix(t.note, TimeEntryNotePrefix)
		if i := strings.IndexAny(rest, ": "); i > 0 {
			rest = rest[:i]
		}
		out[rest] = true
	}
	return out, nil
}
func (w *world) ImportedTotals(context.Context) (map[string][]string, error) {
	out := map[string][]string{}
	for d, ids := range w.totals {
		for _, id := range ids {
			if !w.retired[id] {
				out[d] = append(out[d], id)
			}
		}
	}
	return out, nil
}
func (w *world) ActivePeople(context.Context) (map[string]string, error) { return w.people, nil }
func (w *world) TopSpaces(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

// --- writers ---
func (w *world) AttachNew(_ context.Context, _, _, docID, name, _ string, size int64, r io.Reader) (string, error) {
	b, _ := io.ReadAll(r)
	if int64(len(b)) != size {
		return "", fmt.Errorf("short read")
	}
	w.filesLog = append(w.filesLog, docID+":"+name)
	return w.next("file"), nil
}
func (w *world) LogFor(_ context.Context, _, who, docID string, minutes int, _ time.Time, note string) (string, error) {
	id := w.next("t")
	w.times = append(w.times, wtime{id: id, doc: docID, who: who, note: note, minutes: minutes})
	return id, nil
}
func (w *world) Retire(_ context.Context, ids []string) error {
	for _, id := range ids {
		w.retired[id] = true
	}
	return nil
}
func (w *world) Download(_ context.Context, url string) (io.ReadCloser, int64, string, error) {
	body := "bytes of " + url
	return io.NopCloser(strings.NewReader(body)), int64(len(body)), "image/jpeg", nil
}
func (w *world) AssignQuietly(_ context.Context, _, _ string, rows []docs.Reassignment) error {
	for _, r := range rows {
		w.assigned[r.DocID] = r.Assignees
	}
	return nil
}

func ts(s string) *time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return &t
}

func fixture(t *testing.T, w *world) (*Snapshot, Deps, *bytes.Buffer) {
	t.Helper()
	// Two tasks already in Hearth from the first import.
	a := w.next("doc")
	w.docs[a] = &wdoc{id: a, title: "Old title A", status: "todo", updated: ts("2026-08-20T00:00:00Z").UTC(), version: 3}
	w.refs["document:A"] = a
	b := w.next("doc")
	w.docs[b] = &wdoc{id: b, title: "Edited in Hearth B", status: "todo", updated: ts("2026-09-10T00:00:00Z").UTC(), version: 5}
	w.refs["document:B"] = b
	w.totals[a] = []string{"agg-a"}
	w.comments["comment:c-known"] = true
	w.files["att-known"] = "file-old"

	snap := &Snapshot{
		TakenAt: time.Now().UTC(), TeamID: "t1",
		Lists: []SnapList{{List: List{ID: "l1", Name: "Leads", Space: "Sales"}, Tasks: []Task{
			{ExternalID: "A", Title: "New title A", Status: "in progress", UpdatedAt: ts("2026-09-02T00:00:00Z")},
			{ExternalID: "B", Title: "ClickUp title B", Status: "done", UpdatedAt: ts("2026-09-03T00:00:00Z")},
			{ExternalID: "C", Title: "New, gone assignee", Assignees: []string{"Departed Person"}},
			{ExternalID: "D", Title: "New, nobody"},
			{ExternalID: "E", Title: "New, Nina", Assignees: []string{"Nina Alvarez"}},
		}}},
		Comments: map[string][]Comment{
			"A": {
				{ID: "c-known", Body: "already here", Author: "nina@x.test", At: ts("2026-08-10T00:00:00Z")},
				{ID: "c-new", Body: "added later", Author: "nina@x.test", At: ts("2026-09-01T00:00:00Z")},
				{ID: "c-stranger", Body: "from a stranger", Author: "stranger@x.test", At: ts("2026-09-01T01:00:00Z")},
			},
		},
		Attachments: map[string][]Attachment{
			"A": {
				{ID: "att-known", Title: "old.jpg", URL: "https://example.test/old"},
				{ID: "att-new", Title: "panel", Extension: "jpg", URL: "https://example.test/new", Size: 10},
				{ID: "att-gone", Title: "x", URL: "https://example.test/x", Deleted: true},
			},
		},
		TimeEntries: []TimeEntry{
			{ID: "te1", TaskID: "A", UserID: "9", Username: "Nina Alvarez", Email: "nina@x.test", Start: *ts("2026-08-15T14:00:00Z"), Minutes: 90, Note: "wiring"},
			{ID: "te2", TaskID: "A", UserID: "8", Username: "Departed Person", Email: "gone@x.test", Start: *ts("2026-08-16T14:00:00Z"), Minutes: 30},
			{ID: "te3", TaskID: "ZZ", UserID: "9", Username: "Nina Alvarez", Email: "nina@x.test", Start: *ts("2026-08-17T14:00:00Z"), Minutes: 5},
		},
	}
	var undo bytes.Buffer
	d := Deps{
		Store: w, Data: w, Files: w, Time: w, Download: w, Assign: w,
		OrgID: "org", Fallback: "p-yara", ActorID: "p-owner",
		Cutoff: *ts("2026-08-27T14:45:00Z"), EditedAfter: *ts("2026-08-27T15:15:00Z"),
		Undo: &undo,
	}
	return snap, d, &undo
}

func TestApplyAddsOnlyWhatIsMissingAndKeepsTheRules(t *testing.T) {
	w := newWorld()
	snap, d, undo := fixture(t, w)

	res, err := Apply(context.Background(), d, snap)
	if err != nil {
		t.Fatal(err)
	}
	if res.TasksCreated != 3 {
		t.Fatalf("created %d tasks, want C, D, E", res.TasksCreated)
	}
	// Assignment by the rule: gone and nobody go to Yara, Nina stays Nina.
	byTitle := map[string][]string{}
	for id, who := range w.assigned {
		byTitle[w.docs[id].title] = who
	}
	if got := byTitle["New, gone assignee"]; len(got) != 1 || got[0] != "p-yara" {
		t.Fatalf("gone assignee went to %v", got)
	}
	if got := byTitle["New, nobody"]; len(got) != 1 || got[0] != "p-yara" {
		t.Fatalf("no assignee went to %v", got)
	}
	if got := byTitle["New, Nina"]; len(got) != 1 || got[0] != "p-nina" {
		t.Fatalf("named active assignee went to %v", got)
	}
	if res.AssignedToFallback != 1 || res.NoAssignee != 1 {
		t.Fatalf("flags: fallback %d, none %d", res.AssignedToFallback, res.NoAssignee)
	}

	// A changed in ClickUp and untouched here: brought up to date. B was edited
	// here: left alone.
	if w.docs[w.refs["document:A"]].title != "New title A" || w.docs[w.refs["document:A"]].status != "doing" && w.docs[w.refs["document:A"]].status != "in_progress" {
		t.Fatalf("A = %+v", w.docs[w.refs["document:A"]])
	}
	if b := w.docs[w.refs["document:B"]]; b.title != "Edited in Hearth B" || b.version != 5 {
		t.Fatalf("B was overwritten: %+v", b)
	}
	if res.TasksUpdated != 1 || res.TasksSkippedEdited != 1 {
		t.Fatalf("updated %d, skipped %d", res.TasksUpdated, res.TasksSkippedEdited)
	}

	// Comments: the known one is skipped, the stranger's is credited in the text.
	if res.CommentsPosted != 2 || res.CommentsByUnknownAuthor != 1 {
		t.Fatalf("comments posted %d, unknown %d", res.CommentsPosted, res.CommentsByUnknownAuthor)
	}
	var stranger *wcomment
	for i := range w.posted {
		if strings.Contains(w.posted[i].body, "from a stranger") {
			stranger = &w.posted[i]
		}
	}
	if stranger == nil || !strings.HasPrefix(stranger.body, "From stranger@x.test in ClickUp:") || stranger.author != "p-yara" {
		t.Fatalf("stranger's comment = %+v", stranger)
	}

	// Attachments: only the new, live one.
	if res.FilesAttached != 1 || len(w.filesLog) != 1 || !strings.HasSuffix(w.filesLog[0], ":panel.jpg") {
		t.Fatalf("files: %+v %v", res, w.filesLog)
	}

	// Time: Nina's on Nina; the departed person's on Yara, flagged; the one
	// for a task Hearth does not have is counted and not invented. The task's
	// one-total entry is retired.
	if res.TimeEntries != 2 || res.TimeToFallback != 1 || res.TimeNoTask != 1 {
		t.Fatalf("time: %+v", res)
	}
	for _, e := range w.times {
		switch {
		case strings.Contains(e.note, "te1") && e.who != "p-nina":
			t.Fatalf("te1 went to %s", e.who)
		case strings.Contains(e.note, "te2") && (e.who != "p-yara" || !strings.Contains(e.note, "no active account")):
			t.Fatalf("te2 = %+v", e)
		}
	}
	if !w.retired["agg-a"] || res.TotalsRetired != 1 {
		t.Fatalf("the one-total entry was not retired: %v", w.retired)
	}

	// Every write is on the undo log.
	for _, want := range []string{"document ", "update ", "message ", "file ", "time ", "retired-time agg-a"} {
		if !strings.Contains(undo.String(), want) {
			t.Errorf("undo log lacks %q", want)
		}
	}
}

func TestASecondRunAddsNothing(t *testing.T) {
	w := newWorld()
	snap, d, _ := fixture(t, w)
	if _, err := Apply(context.Background(), d, snap); err != nil {
		t.Fatal(err)
	}
	var undo2 bytes.Buffer
	d.Undo = &undo2
	res, err := Apply(context.Background(), d, snap)
	if err != nil {
		t.Fatal(err)
	}
	if res.TasksCreated != 0 || res.CommentsPosted != 0 || res.FilesAttached != 0 || res.TimeEntries != 0 || res.TotalsRetired != 0 {
		t.Fatalf("a second run wrote again: %+v", res)
	}
	if undo2.Len() != 0 {
		t.Fatalf("a second run logged writes: %s", undo2.String())
	}
}

func TestCompareCountsWhatIsMissing(t *testing.T) {
	w := newWorld()
	snap, d, _ := fixture(t, w)
	cmp, err := Compare(context.Background(), snap, w, d.Cutoff, d.EditedAfter)
	if err != nil {
		t.Fatal(err)
	}
	s := cmp.Totals
	if s.Tasks != 5 || s.InHearth != 2 || s.Missing != 3 {
		t.Fatalf("tasks: %+v", s)
	}
	if s.ChangedAfterCutoff != 2 || s.ChangedUntouched != 1 || s.ChangedEdited != 1 {
		t.Fatalf("changed: %+v", s)
	}
	if s.Comments != 3 || s.CommentsMissing != 2 || s.Attachments != 2 || s.AttachmentsMissing != 1 || s.AttachmentsDeleted != 1 {
		t.Fatalf("comments and files: %+v", s)
	}
	if cmp.GoneAssignees["Departed Person"] != 1 {
		t.Fatalf("gone assignees: %v", cmp.GoneAssignees)
	}
	if cmp.TotalsToReplace != 1 {
		t.Fatalf("totals to replace = %d", cmp.TotalsToReplace)
	}
	md := cmp.Markdown()
	for _, want := range []string{"| Sales |", "TOTAL", "Departed Person"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	// Reading wrote nothing.
	if len(w.posted)+len(w.filesLog)+len(w.times) != 0 || len(w.docs) != 2 {
		t.Fatal("Compare wrote something")
	}
}

func TestAssigneeRule(t *testing.T) {
	active := map[string]string{"nina alvarez": "n", "ivan brooks": "i"}
	cases := []struct {
		names []string
		want  []string
		gone  bool
	}{
		{nil, []string{"Y"}, false},
		{[]string{"Nina Alvarez"}, []string{"n"}, false},
		{[]string{"Nobody Here"}, []string{"Y"}, true},
		{[]string{"Nina Alvarez", "Nobody Here", "Ivan Brooks"}, []string{"n", "Y", "i"}, true},
		{[]string{"Nobody Here", "Other Gone"}, []string{"Y"}, true},
	}
	for _, c := range cases {
		ids, primary, gone := AssigneeRule(c.names, active, "Y")
		if strings.Join(ids, ",") != strings.Join(c.want, ",") || gone != c.gone || primary != c.want[0] {
			t.Errorf("%v: got %v primary %s gone %v, want %v gone %v", c.names, ids, primary, gone, c.want, c.gone)
		}
	}
}

func TestOpsIsTheOperationsDepartment(t *testing.T) {
	if topLevelName("Ops") != "Operations" || topLevelName("Sales") != "Sales" {
		t.Fatal("the folded Ops space must resolve to Operations")
	}
}

func TestAttachmentFilenameKeepsItsExtension(t *testing.T) {
	cases := map[string]Attachment{
		"panel.jpg":        {ID: "1", Title: "panel", Extension: "jpg"},
		"panel.jpg ":       {ID: "1", Title: "panel.jpg", Extension: "jpg"},
		"attachment-9.pdf": {ID: "9", Title: "", Extension: "pdf"},
	}
	for want, a := range cases {
		if got := a.Filename(); got != strings.TrimSpace(want) {
			t.Errorf("Filename() = %q, want %q", got, want)
		}
	}
}
