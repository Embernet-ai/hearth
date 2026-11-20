package clickup

// Filling the gaps the first ClickUp import left, additively.
//
// The first import brought tasks, custom fields and comments across on two days
// in August. It never read attachments, never read past the first 25 comments
// of a task, recorded each task's time as one total under whoever ran the
// import, and could not see anything made afterwards or any space its token
// owner could not open. This reads all of that into a snapshot, compares it
// with what Hearth holds, and writes only what is missing.
//
// The rules it keeps, which are the point of doing it this way:
//
//   - Matched on the ClickUp id the first import stored, so nothing is
//     duplicated and a second run adds nothing.
//   - Nothing edited in Hearth since the import is overwritten. A task changed
//     in ClickUp is brought up to date only if Hearth's copy is untouched.
//   - owner_id and created_by of an existing task are never touched.
//   - New work is assigned by the ClickUp assignee; one with no active account
//     here, or none at all, goes to the fallback person and is flagged.
//   - No notifications and no updated_at bump on anything that existed.
//   - Every row written is announced on the undo writer as it is written.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// Prefixes that keep a comment's, a file's and a time entry's ClickUp id apart
// from a task's in the one external_refs table.
const (
	attachmentRefPrefix = "attachment:"
	// TimeEntryNotePrefix starts the note on a per-person entry written here, so
	// a second run recognises it and the aggregate replacement is traceable.
	TimeEntryNotePrefix = "ClickUp time entry "
)

// Snapshot is a read of ClickUp as it is at one moment.
type Snapshot struct {
	TakenAt     time.Time
	TeamID      string
	Members     []Person
	Lists       []SnapList
	Comments    map[string][]Comment
	Attachments map[string][]Attachment
	TimeEntries []TimeEntry
	// TimeNote says why time could not be read, when it could not.
	TimeNote string
	Problems []string
}

// SnapList is one list and its tasks.
type SnapList struct {
	List
	Tasks []Task
}

// Tasks returns every task in the snapshot once.
func (s *Snapshot) AllTasks() []Task {
	seen := map[string]bool{}
	var out []Task
	for _, l := range s.Lists {
		for _, t := range l.Tasks {
			if t.ExternalID != "" && !seen[t.ExternalID] {
				seen[t.ExternalID] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// TakeSnapshot reads the workspace. It never writes anything anywhere.
func TakeSnapshot(ctx context.Context, c *Client, logf func(string, ...any)) (*Snapshot, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &Snapshot{
		TakenAt: time.Now().UTC(), Comments: map[string][]Comment{}, Attachments: map[string][]Attachment{},
	}
	var err error
	if s.TeamID, err = c.TeamID(ctx); err != nil {
		return nil, err
	}
	if s.Members, err = c.Members(ctx); err != nil {
		return nil, err
	}
	logf("workspace %s, %d members", s.TeamID, len(s.Members))

	lists, err := c.Lists(ctx)
	if err != nil {
		return nil, err
	}
	logf("%d lists", len(lists))
	for i, l := range lists {
		// Every list is read, including ones that report no tasks: the count is
		// the open ones, and a list holding only closed work reports zero.
		tasks, err := c.TasksIn(ctx, l.ID)
		if err != nil {
			s.Problems = append(s.Problems, fmt.Sprintf("list %q in %q: %v", l.Name, l.Space, err))
			continue
		}
		if len(tasks) == 0 {
			continue
		}
		s.Lists = append(s.Lists, SnapList{List: l, Tasks: tasks})
		if (i+1)%25 == 0 {
			logf("read %d of %d lists", i+1, len(lists))
		}
	}

	all := s.AllTasks()
	logf("%d tasks; reading comments and attachments for each", len(all))
	for i, t := range all {
		if ctx.Err() != nil {
			return s, ctx.Err()
		}
		cms, err := c.CommentsAll(ctx, t.ExternalID)
		if err != nil {
			s.Problems = append(s.Problems, fmt.Sprintf("comments on %s: %v", t.ExternalID, err))
		} else if len(cms) > 0 {
			s.Comments[t.ExternalID] = cms
		}
		atts, err := c.AttachmentsOn(ctx, t.ExternalID)
		if err != nil {
			s.Problems = append(s.Problems, fmt.Sprintf("attachments on %s: %v", t.ExternalID, err))
		} else if len(atts) > 0 {
			s.Attachments[t.ExternalID] = atts
		}
		if (i+1)%100 == 0 {
			logf("read %d of %d tasks", i+1, len(all))
		}
	}

	// Time, person by person, a year at a time.
	from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Now().UTC().Add(24 * time.Hour)
	foreign := 0
	for _, m := range s.Members {
		for start := from; start.Before(now); start = start.AddDate(1, 0, 0) {
			end := start.AddDate(1, 0, 0)
			if end.After(now) {
				end = now
			}
			es, err := c.TimeEntries(ctx, s.TeamID, m.ExternalID, start, end)
			if err != nil {
				s.Problems = append(s.Problems, fmt.Sprintf("time for %s %d: %v", m.Email, start.Year(), err))
				continue
			}
			for _, e := range es {
				if e.UserID != "" && e.UserID != m.ExternalID {
					foreign++
					continue
				}
				s.TimeEntries = append(s.TimeEntries, e)
			}
		}
	}
	if len(s.TimeEntries) == 0 {
		s.TimeNote = "no time entries were readable with this token"
	} else if foreign > 0 {
		s.TimeNote = fmt.Sprintf("%d entries came back under another user than the one asked for, so they were dropped", foreign)
	}
	logf("%d time entries", len(s.TimeEntries))
	return s, nil
}

// GapData is what Hearth already holds, read in bulk.
type GapData interface {
	// TaskDocs maps a ClickUp task id to the document it became.
	TaskDocs(ctx context.Context) (map[string]string, error)
	// DocUpdated maps a document id to its updated_at.
	DocUpdated(ctx context.Context) (map[string]time.Time, error)
	// DocVersions maps a document id to its version, for an undo line.
	DocVersions(ctx context.Context) (map[string]int64, error)
	KnownComments(ctx context.Context) (map[string]bool, error)
	// KnownAttachments maps a ClickUp attachment id to the file it became.
	KnownAttachments(ctx context.Context) (map[string]string, error)
	KnownTimeEntries(ctx context.Context) (map[string]bool, error)
	// ImportedTotals maps a document id to the ids of the one-total entries the
	// first import wrote for it.
	ImportedTotals(ctx context.Context) (map[string][]string, error)
	// ActivePeople maps lowercased email and display name to a principal id,
	// for people who can still sign in.
	ActivePeople(ctx context.Context) (map[string]string, error)
	// TopSpaces maps the name of each top-level department to its id, preferring a real department over a stray folder of the same name.
	TopSpaces(ctx context.Context) (map[string]string, error)
}

// SpaceCount is the comparison for one ClickUp space.
type SpaceCount struct {
	Space                                string
	Lists                                int
	Tasks, InHearth, Missing             int
	ChangedAfterCutoff, ChangedUntouched int
	ChangedEdited                        int
	Comments, CommentsMissing            int
	Attachments, AttachmentsMissing      int
	AttachmentsDeleted                   int
	AttachmentBytesMissing               int64
}

// UserTime is one person's time in ClickUp against what Hearth holds.
type UserTime struct {
	Name           string
	Email          string
	Entries        int
	Minutes        int
	Missing        int
	MissingMinutes int
	HasAccount     bool
}

// Comparison is the inventory: ClickUp as read, against Hearth as it is.
type Comparison struct {
	TakenAt  time.Time
	Cutoff   time.Time
	Spaces   []SpaceCount
	Totals   SpaceCount
	Time     []UserTime
	TimeNote string
	// NewSpaces are ClickUp spaces Hearth holds nothing from.
	NewSpaces []string
	// TotalsToReplace is how many one-total entries the per-person time would
	// replace.
	TotalsToReplace int
	// Unresolved assignee names, and how many new tasks name them.
	GoneAssignees map[string]int
	Problems      []string
}

// Compare lays a snapshot against Hearth. It writes nothing.
func Compare(ctx context.Context, snap *Snapshot, data GapData, cutoff, editedAfter time.Time) (*Comparison, error) {
	taskDocs, err := data.TaskDocs(ctx)
	if err != nil {
		return nil, err
	}
	updated, err := data.DocUpdated(ctx)
	if err != nil {
		return nil, err
	}
	knownC, err := data.KnownComments(ctx)
	if err != nil {
		return nil, err
	}
	knownA, err := data.KnownAttachments(ctx)
	if err != nil {
		return nil, err
	}
	knownT, err := data.KnownTimeEntries(ctx)
	if err != nil {
		return nil, err
	}
	totals, err := data.ImportedTotals(ctx)
	if err != nil {
		return nil, err
	}
	active, err := data.ActivePeople(ctx)
	if err != nil {
		return nil, err
	}

	cmp := &Comparison{TakenAt: snap.TakenAt, Cutoff: cutoff, TimeNote: snap.TimeNote,
		GoneAssignees: map[string]int{}, Problems: snap.Problems}
	bySpace := map[string]*SpaceCount{}
	spaceOf := map[string]string{}
	count := func(name string) *SpaceCount {
		sc := bySpace[name]
		if sc == nil {
			sc = &SpaceCount{Space: name}
			bySpace[name] = sc
		}
		return sc
	}
	for _, l := range snap.Lists {
		count(l.Space).Lists++
		for _, t := range l.Tasks {
			if _, dup := spaceOf[t.ExternalID]; dup {
				continue
			}
			spaceOf[t.ExternalID] = l.Space
			sc := count(l.Space)
			sc.Tasks++
			doc, have := taskDocs[t.ExternalID]
			if !have {
				sc.Missing++
				for _, n := range t.Assignees {
					if _, ok := active[strings.ToLower(strings.TrimSpace(n))]; !ok {
						cmp.GoneAssignees[n]++
					}
				}
				continue
			}
			sc.InHearth++
			if t.UpdatedAt != nil && t.UpdatedAt.After(cutoff) {
				sc.ChangedAfterCutoff++
				if u, ok := updated[doc]; ok && u.After(editedAfter) {
					sc.ChangedEdited++
				} else {
					sc.ChangedUntouched++
				}
			}
		}
	}
	for ext, cs := range snap.Comments {
		sc := count(spaceOf[ext])
		for _, c := range cs {
			sc.Comments++
			if !knownC[commentRefPrefix+c.ID] && !knownC[c.ID] {
				sc.CommentsMissing++
			}
		}
	}
	for ext, as := range snap.Attachments {
		sc := count(spaceOf[ext])
		for _, a := range as {
			if a.Deleted || a.Hidden {
				sc.AttachmentsDeleted++
				continue
			}
			sc.Attachments++
			if _, ok := knownA[a.ID]; !ok {
				sc.AttachmentsMissing++
				sc.AttachmentBytesMissing += a.Size
			}
		}
	}
	for name, sc := range bySpace {
		if sc.InHearth == 0 && sc.Tasks > 0 {
			cmp.NewSpaces = append(cmp.NewSpaces, name)
		}
	}
	sort.Strings(cmp.NewSpaces)

	names := sort.StringSlice{}
	for n := range bySpace {
		names = append(names, n)
	}
	names.Sort()
	for _, n := range names {
		sc := bySpace[n]
		cmp.Spaces = append(cmp.Spaces, *sc)
		t := &cmp.Totals
		t.Lists += sc.Lists
		t.Tasks += sc.Tasks
		t.InHearth += sc.InHearth
		t.Missing += sc.Missing
		t.ChangedAfterCutoff += sc.ChangedAfterCutoff
		t.ChangedUntouched += sc.ChangedUntouched
		t.ChangedEdited += sc.ChangedEdited
		t.Comments += sc.Comments
		t.CommentsMissing += sc.CommentsMissing
		t.Attachments += sc.Attachments
		t.AttachmentsMissing += sc.AttachmentsMissing
		t.AttachmentsDeleted += sc.AttachmentsDeleted
		t.AttachmentBytesMissing += sc.AttachmentBytesMissing
	}
	cmp.Totals.Space = "TOTAL"

	byUser := map[string]*UserTime{}
	docsWithEntries := map[string]bool{}
	for _, e := range snap.TimeEntries {
		key := e.UserID
		u := byUser[key]
		if u == nil {
			u = &UserTime{Name: e.Username, Email: e.Email}
			_, byEmail := active[e.Email]
			_, byName := active[strings.ToLower(e.Username)]
			u.HasAccount = byEmail || byName
			byUser[key] = u
		}
		u.Entries++
		u.Minutes += e.Minutes
		if !knownT[e.ID] {
			u.Missing++
			u.MissingMinutes += e.Minutes
			if doc, ok := taskDocs[e.TaskID]; ok {
				docsWithEntries[doc] = true
			}
		}
	}
	for _, u := range byUser {
		cmp.Time = append(cmp.Time, *u)
	}
	sort.Slice(cmp.Time, func(i, j int) bool { return cmp.Time[i].MissingMinutes > cmp.Time[j].MissingMinutes })
	for doc := range docsWithEntries {
		cmp.TotalsToReplace += len(totals[doc])
	}
	return cmp, nil
}

// Markdown renders the comparison for a person to read.
func (c *Comparison) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ClickUp against Hearth\n\nRead %s. Tasks changed in ClickUp after %s are counted as changed.\n\n",
		c.TakenAt.Format("2006-01-02 15:04 UTC"), c.Cutoff.Format("2006-01-02 15:04 UTC"))
	b.WriteString("| Space | Lists | Tasks in ClickUp | In Hearth | Missing | Changed after cutoff | of those untouched here | edited here | Comments | Missing | Attachments | Missing | Deleted or hidden |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	row := func(s SpaceCount) {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n", s.Space, s.Lists, s.Tasks,
			s.InHearth, s.Missing, s.ChangedAfterCutoff, s.ChangedUntouched, s.ChangedEdited, s.Comments,
			s.CommentsMissing, s.Attachments, s.AttachmentsMissing, s.AttachmentsDeleted)
	}
	for _, s := range c.Spaces {
		row(s)
	}
	row(c.Totals)
	fmt.Fprintf(&b, "\nAttachment bytes missing: %.1f MB.\n", float64(c.Totals.AttachmentBytesMissing)/1e6)
	if len(c.NewSpaces) > 0 {
		fmt.Fprintf(&b, "\nSpaces ClickUp has that Hearth holds nothing from: %s.\n", strings.Join(c.NewSpaces, ", "))
	} else {
		b.WriteString("\nNo space exists in ClickUp that Hearth holds nothing from.\n")
	}
	b.WriteString("\n## Time\n\n")
	if c.TimeNote != "" {
		fmt.Fprintf(&b, "%s.\n\n", c.TimeNote)
	}
	b.WriteString("| Person | Entries | Hours | Missing entries | Missing hours | Has an account here |\n|---|---|---|---|---|---|\n")
	for _, u := range c.Time {
		fmt.Fprintf(&b, "| %s | %d | %.1f | %d | %.1f | %v |\n", firstNonEmpty(u.Name, u.Email), u.Entries,
			float64(u.Minutes)/60, u.Missing, float64(u.MissingMinutes)/60, u.HasAccount)
	}
	fmt.Fprintf(&b, "\nOne-total entries the first import wrote that per-person entries would replace: %d.\n", c.TotalsToReplace)
	if len(c.GoneAssignees) > 0 {
		b.WriteString("\n## New tasks naming someone with no active account here (they go to the fallback person)\n\n")
		type kv struct {
			k string
			v int
		}
		var l []kv
		for k, v := range c.GoneAssignees {
			l = append(l, kv{k, v})
		}
		sort.Slice(l, func(i, j int) bool { return l[i].v > l[j].v })
		for _, x := range l {
			fmt.Fprintf(&b, "- %s: %d\n", x.k, x.v)
		}
	}
	if len(c.Problems) > 0 {
		fmt.Fprintf(&b, "\n## Read problems (%d)\n\n", len(c.Problems))
		for i, p := range c.Problems {
			if i >= 40 {
				b.WriteString("- and more\n")
				break
			}
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Applying
// ---------------------------------------------------------------------------

// FileStore puts a downloaded attachment into object storage and attaches it.
type FileStore interface {
	AttachNew(ctx context.Context, orgID, uploaderID, docID, name, contentType string, size int64, r io.Reader) (fileID string, err error)
}

// TimeStore writes per-person time and retires the one-total entries.
type TimeStore interface {
	LogFor(ctx context.Context, orgID, principalID, docID string, minutes int, start time.Time, note string) (entryID string, err error)
	// Retire hides entries without deleting them, so an undo can bring them back.
	Retire(ctx context.Context, entryIDs []string) error
}

// Downloader opens an attachment's bytes.
type Downloader interface {
	Download(ctx context.Context, url string) (io.ReadCloser, int64, string, error)
}

// Assigner puts assignees on documents quietly: no event, no updated_at.
type Assigner interface {
	AssignQuietly(ctx context.Context, orgID, actorID string, rows []docs.Reassignment) error
}

// Deps is everything Apply needs.
type Deps struct {
	Store    Store
	Data     GapData
	Files    FileStore
	Time     TimeStore
	Download Downloader
	Assign   Assigner

	OrgID    string
	Actor    core.Subjects
	ActorID  string
	Fallback string // principal id that gets what has no active assignee
	// Cutoff is when the last import finished. EditedAfter is the time after
	// which a Hearth document counts as edited here.
	Cutoff, EditedAfter time.Time
	// Undo receives one line per row written.
	Undo io.Writer
	Logf func(string, ...any)
	// SkipUpdates, SkipAttachments, SkipTime, SkipComments turn a phase off.
	SkipUpdates, SkipAttachments, SkipTime, SkipComments bool
}

// Applied counts what a run wrote.
type Applied struct {
	TasksCreated, TasksUpdated, TasksSkippedEdited int
	AssignedToFallback, NoAssignee                 int
	CommentsPosted, CommentsByUnknownAuthor        int
	FilesAttached, FilesFailed                     int
	TimeEntries, TimeToFallback, TimeNoTask        int
	TotalsRetired                                  int
	Problems                                       []string
}

func (d *Deps) undo(format string, args ...any) {
	if d.Undo != nil {
		fmt.Fprintf(d.Undo, format+"\n", args...)
	}
}

func (d *Deps) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

// Apply writes what is missing.
func Apply(ctx context.Context, d Deps, snap *Snapshot) (*Applied, error) {
	out := &Applied{}
	docMap, err := d.Data.TaskDocs(ctx)
	if err != nil {
		return nil, err
	}
	updated, err := d.Data.DocUpdated(ctx)
	if err != nil {
		return nil, err
	}
	versions, err := d.Data.DocVersions(ctx)
	if err != nil {
		return nil, err
	}
	active, err := d.Data.ActivePeople(ctx)
	if err != nil {
		return nil, err
	}

	people := resolvePeople(ctx, d.Store, d.OrgID)
	tree := &spaceTree{made: map[string]string{}}
	// Where a ClickUp space's department is, chosen once. Two top-level spaces
	// can share a name (a department and a stray folder of the same name), and
	// a lookup by name alone would pick either.
	if top, err := d.Data.TopSpaces(ctx); err == nil {
		for name, id := range top {
			tree.made["top:"+name] = id
		}
	}
	kinds := map[string]string{}
	for _, l := range snap.Lists {
		k := docs.KindTask
		if kk, ok := DefaultKindBySpace[l.Space]; ok {
			k = kk
		}
		for _, t := range l.Tasks {
			if _, set := kinds[t.ExternalID]; !set {
				kinds[t.ExternalID] = k
			}
		}
	}
	newDocs := map[string]string{} // ClickUp task id -> new document id
	newTasks := map[string]Task{}

	// 1. New tasks, list by list, into the department the list belongs to.
	for _, l := range snap.Lists {
		var fresh []Task
		for _, t := range l.Tasks {
			if _, have := docMap[t.ExternalID]; !have && newDocs[t.ExternalID] == "" {
				fresh = append(fresh, t)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		kind := docs.KindTask
		if k, ok := DefaultKindBySpace[l.Space]; ok {
			kind = k
		}
		place := l.List
		place.Space = topLevelName(l.Space)
		spaceID := tree.spaceFor(ctx, d.Store, d.OrgID, d.Actor, place)
		res, ids, err := runTasksInSpaceIDs(ctx, d.Store, d.OrgID, d.Actor, fresh, kind, spaceID)
		if err != nil {
			out.Problems = appendCapped(out.Problems, fmt.Sprintf("list %q in %q: %v", l.Name, l.Space, err))
			continue
		}
		out.Problems = appendCapped(out.Problems, res.Problems...)
		for ext, id := range ids {
			newDocs[ext] = id
			docMap[ext] = id
			d.undo("document %s clickup:%s", id, ext)
		}
		for _, t := range fresh {
			newTasks[t.ExternalID] = t
		}
		out.TasksCreated += res.Created
		d.logf("list %s / %s: %d new tasks", l.Space, l.Name, res.Created)
	}

	// 2. New tasks get their assignees by the rule: the person ClickUp named if
	// they are active here, otherwise the fallback, flagged.
	var rows []docs.Reassignment
	for ext, id := range newDocs {
		t := newTasks[ext]
		if !docs.HasTaskFields(kinds[ext]) {
			continue
		}
		ids, primary, gone := AssigneeRule(t.Assignees, active, d.Fallback)
		if len(t.Assignees) == 0 {
			out.NoAssignee++
		} else if gone {
			out.AssignedToFallback++
		}
		rows = append(rows, docs.Reassignment{DocID: id, Assignees: ids, Primary: primary, SkipExpect: true})
	}
	if len(rows) > 0 {
		sort.Slice(rows, func(i, j int) bool { return rows[i].DocID < rows[j].DocID })
		if err := d.Assign.AssignQuietly(ctx, d.OrgID, d.ActorID, rows); err != nil {
			out.Problems = appendCapped(out.Problems, "assigning new tasks: "+err.Error())
		}
	}

	// 3. Existing tasks changed in ClickUp since the cutoff, but only where
	// nobody has touched Hearth's copy.
	if !d.SkipUpdates {
		for _, t := range snap.AllTasks() {
			doc, have := docMap[t.ExternalID]
			if !have || newDocs[t.ExternalID] != "" || t.UpdatedAt == nil || !t.UpdatedAt.After(d.Cutoff) {
				continue
			}
			if u, ok := updated[doc]; ok && u.After(d.EditedAfter) {
				out.TasksSkippedEdited++
				continue
			}
			status := MapStatus(t.Status)
			title := strings.TrimSpace(t.Title)
			if title == "" {
				title = "Untitled ClickUp task"
			}
			body := buildBody(t)
			d.undo("update %s clickup:%s from_version %d", doc, t.ExternalID, versions[doc])
			if _, err := d.Store.Update(ctx, d.OrgID, d.Actor, doc, docs.UpdateParams{
				Title: &title, Body: &body, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
				Task: &docs.TaskPatch{
					Status: &status, Priority: &t.Priority, DueAt: t.DueAt,
					StartedAt: t.StartAt, CompletedAt: t.CompletedAt, KeepSubtasks: true,
				},
			}); err != nil {
				out.Problems = appendCapped(out.Problems, fmt.Sprintf("update %s: %v", t.ExternalID, err))
				continue
			}
			out.TasksUpdated++
		}
	}

	// 4. Comments, every page and every reply, that are not here yet.
	if !d.SkipComments {
		known, err := d.Data.KnownComments(ctx)
		if err != nil {
			return out, err
		}
		for ext, cs := range snap.Comments {
			doc, have := docMap[ext]
			if !have {
				continue
			}
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			byExternal := map[string]string{}
			for _, cm := range cs {
				if known[commentRefPrefix+cm.ID] || known[cm.ID] {
					continue
				}
				author := d.Fallback
				body := cm.Body
				if id, ok := people[cm.Author]; ok {
					author = id
				} else {
					// Nobody here to credit. Said so in the text rather than
					// quietly posted as whoever ran the import.
					name := cm.Author
					if name == "" {
						name = "someone"
					}
					body = "From " + name + " in ClickUp:\n\n" + body
					out.CommentsByUnknownAuthor++
				}
				at := time.Time{}
				if cm.At != nil {
					at = *cm.At
				}
				parent := ""
				if cm.ParentID != "" {
					parent = byExternal[cm.ParentID]
				}
				id, err := d.Store.PostComment(ctx, d.OrgID, author, doc, body, at, parent)
				if err != nil {
					out.Problems = appendCapped(out.Problems, fmt.Sprintf("comment %s: %v", cm.ID, err))
					continue
				}
				byExternal[cm.ID] = id
				d.undo("message %s clickup:comment:%s doc %s", id, cm.ID, doc)
				if err := d.Store.RecordExternalRef(ctx, d.OrgID,
					core.Ref{Type: core.EntityMessage, ID: id},
					docs.ExternalRef{Source: Source, ExternalID: commentRefPrefix + cm.ID}); err != nil {
					out.Problems = appendCapped(out.Problems, fmt.Sprintf("comment ref %s: %v", cm.ID, err))
				}
				out.CommentsPosted++
			}
		}
	}

	// 5. Attachments, downloaded and stored in Hearth's object storage.
	if !d.SkipAttachments {
		knownA, err := d.Data.KnownAttachments(ctx)
		if err != nil {
			return out, err
		}
		exts := make([]string, 0, len(snap.Attachments))
		for ext := range snap.Attachments {
			exts = append(exts, ext)
		}
		sort.Strings(exts)
		for _, ext := range exts {
			doc, have := docMap[ext]
			if !have {
				continue
			}
			for _, a := range snap.Attachments[ext] {
				if a.Deleted || a.Hidden || a.URL == "" {
					continue
				}
				if _, ok := knownA[a.ID]; ok {
					continue
				}
				if ctx.Err() != nil {
					return out, ctx.Err()
				}
				rc, size, ctype, err := d.Download.Download(ctx, a.URL)
				if err != nil {
					out.FilesFailed++
					out.Problems = appendCapped(out.Problems, fmt.Sprintf("download %s (%s): %v", a.ID, a.Filename(), err))
					continue
				}
				if size < 0 {
					// Unknown length: Put needs one, so read it in. Attachments
					// are bounded by the store's own limit.
					buf, rerr := io.ReadAll(io.LimitReader(rc, 513<<20))
					rc.Close()
					if rerr != nil {
						out.FilesFailed++
						continue
					}
					rc, size = io.NopCloser(strings.NewReader(string(buf))), int64(len(buf))
				}
				if ctype == "" || strings.HasPrefix(ctype, "application/octet-stream") {
					ctype = a.MimeType
				}
				fileID, err := d.Files.AttachNew(ctx, d.OrgID, d.ActorID, doc, a.Filename(), ctype, size, rc)
				rc.Close()
				if err != nil {
					out.FilesFailed++
					out.Problems = appendCapped(out.Problems, fmt.Sprintf("attach %s (%s): %v", a.ID, a.Filename(), err))
					continue
				}
				d.undo("file %s clickup:attachment:%s doc %s", fileID, a.ID, doc)
				if err := d.Store.RecordExternalRef(ctx, d.OrgID,
					core.Ref{Type: core.EntityFile, ID: fileID},
					docs.ExternalRef{Source: Source, ExternalID: attachmentRefPrefix + a.ID, URL: a.URL}); err != nil {
					out.Problems = appendCapped(out.Problems, fmt.Sprintf("file ref %s: %v", a.ID, err))
				}
				out.FilesAttached++
			}
		}
	}

	// 6. Time, on the right person, and the one-total entries it replaces.
	if !d.SkipTime && len(snap.TimeEntries) > 0 {
		knownT, err := d.Data.KnownTimeEntries(ctx)
		if err != nil {
			return out, err
		}
		totals, err := d.Data.ImportedTotals(ctx)
		if err != nil {
			return out, err
		}
		touched := map[string]bool{}
		for _, e := range snap.TimeEntries {
			if knownT[e.ID] {
				continue
			}
			doc, have := docMap[e.TaskID]
			if !have {
				out.TimeNoTask++
				continue
			}
			who, ok := active[e.Email]
			if !ok {
				who, ok = active[strings.ToLower(e.Username)]
			}
			note := TimeEntryNotePrefix + e.ID
			if e.Note != "" {
				note += ": " + e.Note
			}
			if !ok {
				who = d.Fallback
				out.TimeToFallback++
				note += " [logged in ClickUp by " + firstNonEmpty(e.Username, e.Email) + ", who has no active account here]"
			}
			id, err := d.Time.LogFor(ctx, d.OrgID, who, doc, e.Minutes, e.Start, note)
			if err != nil {
				out.Problems = appendCapped(out.Problems, fmt.Sprintf("time %s: %v", e.ID, err))
				continue
			}
			d.undo("time %s clickup:time:%s doc %s", id, e.ID, doc)
			touched[doc] = true
			out.TimeEntries++
		}
		var retire []string
		for doc := range touched {
			retire = append(retire, totals[doc]...)
		}
		sort.Strings(retire)
		if len(retire) > 0 {
			for _, id := range retire {
				d.undo("retired-time %s", id)
			}
			if err := d.Time.Retire(ctx, retire); err != nil {
				out.Problems = appendCapped(out.Problems, "retiring one-total time: "+err.Error())
			} else {
				out.TotalsRetired = len(retire)
			}
		}
	}
	return out, nil
}

// AssigneeRule is the rule for a new task. A name that resolves to an active
// account is kept; one that does not, and a task with no names at all, goes to
// the fallback. gone reports whether a named person was replaced.
func AssigneeRule(names []string, active map[string]string, fallback string) (ids []string, primary string, gone bool) {
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, n := range names {
		if id, ok := active[strings.ToLower(strings.TrimSpace(n))]; ok {
			add(id)
		} else {
			gone = true
			add(fallback)
		}
	}
	if len(ids) == 0 {
		add(fallback)
	}
	if len(ids) > 0 {
		primary = ids[0]
	}
	return ids, primary, gone
}

// topLevelName maps a ClickUp space to the department that holds its work
// here. Ops was folded into Operations (migration 00032), and a lookup for the
// old name would build a second, empty Ops beside it.
func topLevelName(space string) string {
	if strings.EqualFold(space, "Ops") {
		return "Operations"
	}
	return space
}
