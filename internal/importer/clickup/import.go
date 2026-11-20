package clickup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// Source is the name recorded against every external reference this creates.
const Source = "clickup"

// statusFieldID is the external id of the synthetic field holding the source's
// own status text. Prefixed so it cannot collide with a real ClickUp field id,
// which is always a uuid.
const statusFieldID = "__clickup_status"

// tagsFieldID is the synthetic field holding a task's tags. Hearth has no tag
// table, so this is where they live.
const tagsFieldID = "__clickup_tags"

// ImportedTimeNote marks the entries this importer writes, so a re-run can
// replace its own without touching hours somebody logged by hand.
const ImportedTimeNote = "Imported from ClickUp: total time tracked there"

// commentRefPrefix namespaces a comment's external id away from a task's, so
// both can live in one external_refs table keyed on (source, external_id).
const commentRefPrefix = "comment:"

// Store is the slice of the document service an import needs.
//
// Declared here by the consumer, like docs declares its Authorizer, so the
// dependency points one way and this package can be tested without a database.
type Store interface {
	Create(ctx context.Context, orgID string, actor core.Subjects, p docs.CreateParams) (*docs.Document, error)
	Update(ctx context.Context, orgID string, actor core.Subjects, id string, p docs.UpdateParams) (*docs.Document, error)
	FindByExternalID(ctx context.Context, orgID, source, externalID string) (string, error)
	RecordExternalRef(ctx context.Context, orgID string, ref core.Ref, e docs.ExternalRef) error
	Link(ctx context.Context, orgID string, actor core.Subjects, from, to core.Ref, kind string) error
	// SetParent makes a subtask a real subtask, so a list can fold it under
	// its parent the way the source showed it.
	SetParent(ctx context.Context, orgID string, actor core.Subjects, id, parentID string) error
	UpsertImportedProperty(ctx context.Context, orgID string, d docs.ImportedProperty) (string, error)
	// CreateSpace mirrors the source's own hierarchy, so imported work lands
	// somewhere rather than in one undifferentiated pile.
	CreateSpace(ctx context.Context, orgID string, actor core.Subjects, name, kind string, parentID *string) (string, error)
	// FindSpaceByName looks for a space by name ANYWHERE in the tree, so an
	// import does not undo a reorganisation somebody has done here.
	FindSpaceByName(ctx context.Context, orgID, name string) (string, error)
	// People is how an assignee stops being a line of prose and becomes the
	// field the workload page, the board and every "mine" filter read.
	People(ctx context.Context, orgID string) ([]Person, error)
	// ProvisionPerson creates or updates an account for somebody the source
	// knows about, so their work arrives assigned to them.
	ProvisionPerson(ctx context.Context, orgID, externalID, displayName, email string) (string, error)
	// LogTime and SetEstimate carry the effort the source tracked. A migrated
	// job that lost its hours is one nobody can invoice from or learn from.
	LogTime(ctx context.Context, orgID string, actor core.Subjects, docID string, minutes int, on time.Time, note string) error
	// ClearImportedTime removes entries a previous import wrote for this
	// document, so a re-run replaces the hours instead of adding to them.
	ClearImportedTime(ctx context.Context, orgID, docID, note string) error
	SetEstimate(ctx context.Context, orgID string, actor core.Subjects, docID string, minutes *int) error
	// AddChecklistItem rebuilds the steps a task carried. Returns the item id
	// so a step that was already ticked can be ticked here too.
	AddChecklistItem(ctx context.Context, orgID string, actor core.Subjects, docID, text string) (string, error)
	SetChecklistItemDone(ctx context.Context, orgID string, actor core.Subjects, itemID string, done bool) error
	// PostComment carries a conversation that already happened, with its own
	// author and its own timestamp, and without raising a notification for it.
	PostComment(ctx context.Context, orgID, principalID string, docID, body string, at time.Time, parentID string) (string, error)
	// Watch records that somebody was following this in the source.
	Watch(ctx context.Context, principalID, docID string) error
	// SetAssignees records everybody the task was assigned to, not only the
	// one the task field can hold.
	SetAssignees(ctx context.Context, docID string, principalIDs []string, primary string) error
	SetProperties(ctx context.Context, orgID string, actor core.Subjects, entityID string, values map[string]string) error
}

// Result is what happened, in the terms the person who clicked import cares
// about. Skipped is not an error: it is the second run of the same file doing
// the right thing.
type Result struct {
	Created  int
	Updated  int
	Failed   int
	Total    int
	Kind     string
	Problems []string
}

// Options controls how the import lands.
type Options struct {
	// Kind is what the tasks become. ClickUp calls everything a task, but the
	// reason somebody is leaving ClickUp is usually that their work orders were
	// pretending to be tasks.
	Kind string

	// KindBySpace overrides Kind per ClickUp space, for a whole-workspace
	// import where one answer does not fit: a service desk and an HR request
	// queue are not the same object with a different label. Nil means
	// DefaultKindBySpace.
	KindBySpace map[string]string

	// Progress is called after each list, if set.
	//
	// A migration that prints nothing for twenty minutes is one somebody kills
	// because they cannot tell it apart from a hang. This is how the caller
	// says otherwise; the importer does not choose a format or assume there is
	// a terminal to write to.
	Progress func(done, total int, list List, res *Result)
}

// Run imports the export. Existing tasks are updated rather than duplicated.
//
// One task at a time, not one big transaction. A 4000-task import that fails on
// row 3999 and rolls back everything is worse than one that imports 3998 and
// tells you which two were bad, because the second can be fixed and re-run and
// the first can only be re-attempted and hoped over.
func Run(ctx context.Context, store Store, orgID string, actor core.Subjects, r io.Reader, opt Options) (*Result, error) {
	tasks, err := Parse(r)
	if err != nil {
		return nil, err
	}
	kind := opt.Kind
	if kind == "" {
		kind = docs.KindTask
	}
	return runTasks(ctx, store, orgID, actor, tasks, kind)
}

// runTasks imports an already-parsed batch, and is the shared body behind both
// a file import and a whole-workspace one. Splitting it out is what keeps the
// two paths from drifting: parent linking, field registration and re-run
// matching all behave the same whichever way the tasks arrived.
func runTasks(ctx context.Context, store Store, orgID string, actor core.Subjects,
	tasks []Task, kind string) (*Result, error) {

	return runTasksInSpace(ctx, store, orgID, actor, tasks, kind, nil)
}

// runTasksInSpace is runTasks with somewhere to file the results.
func runTasksInSpace(ctx context.Context, store Store, orgID string, actor core.Subjects,
	tasks []Task, kind string, spaceID *string) (*Result, error) {

	res, _, err := runTasksInSpaceIDs(ctx, store, orgID, actor, tasks, kind, spaceID)
	return res, err
}

// runTasksInSpaceIDs also returns what each source task became.
func runTasksInSpaceIDs(ctx context.Context, store Store, orgID string, actor core.Subjects,
	tasks []Task, kind string, spaceID *string) (*Result, map[string]string, error) {

	// Definitions first, so a value has something to be keyed by.
	//
	// Once per distinct field rather than once per task: the workspace this was
	// built against has 62 definitions spread over 1,132 tasks, and upserting
	// per task would be thousands of writes to produce sixty-two rows.
	res := &Result{Total: len(tasks), Kind: kind}
	people := resolvePeople(ctx, store, orgID)
	fieldIDs, defProblems := upsertFieldDefs(ctx, store, orgID, tasks)
	res.Problems = append(res.Problems, defProblems...)
	// Remembered so a parent/child relationship inside the export becomes a real
	// link, in whichever order the rows arrive.
	byExternal := make(map[string]string, len(tasks))

	for _, t := range tasks {
		id, err := upsert(ctx, store, orgID, actor, t, kind, fieldIDs, spaceID, people, res)
		if err != nil {
			res.Failed++
			if len(res.Problems) < 20 {
				res.Problems = append(res.Problems,
					fmt.Sprintf("%s: %v", firstNonEmpty(t.Title, t.ExternalID), err))
			}
			continue
		}
		if t.ExternalID != "" {
			byExternal[t.ExternalID] = id
		}
	}

	// Parent links, once every id is known.
	for _, t := range tasks {
		if t.Parent == "" {
			continue
		}
		child, ok := byExternal[t.ExternalID]
		parent, ok2 := byExternal[t.Parent]
		if !ok || !ok2 {
			continue
		}
		_ = store.Link(ctx, orgID, actor,
			core.Ref{Type: core.EntityDocument, ID: parent},
			core.Ref{Type: core.EntityDocument, ID: child}, docs.LinkConcerns)
		_ = store.SetParent(ctx, orgID, actor, child, parent)
	}

	// Dependencies, links and watchers, also once every id is known.
	//
	// A dependency on a task outside this list resolves to nothing and is
	// skipped rather than guessed at: the pair will be linked when the other
	// list is imported, because ClickUp reports the relationship from both
	// sides and the second side will find the first already present.
	ref := func(id string) core.Ref { return core.Ref{Type: core.EntityDocument, ID: id} }
	for _, t := range tasks {
		self, ok := byExternal[t.ExternalID]
		if !ok {
			continue
		}
		for _, other := range t.DependsOn {
			if id, ok := byExternal[other]; ok {
				_ = store.Link(ctx, orgID, actor, ref(id), ref(self), docs.LinkBlocks)
			}
		}
		for _, other := range t.Blocks {
			if id, ok := byExternal[other]; ok {
				_ = store.Link(ctx, orgID, actor, ref(self), ref(id), docs.LinkBlocks)
			}
		}
		for _, other := range t.Linked {
			if id, ok := byExternal[other]; ok {
				_ = store.Link(ctx, orgID, actor, ref(self), ref(id), docs.LinkRelates)
			}
		}
		// Watching is how somebody keeps getting told about a job they are not
		// assigned to, and it is the only record of who cared about it.
		for _, w := range t.Watchers {
			if pid, ok := people[w]; ok {
				_ = store.Watch(ctx, pid, self)
			}
		}
	}

	return res, byExternal, nil
}

// upsertFieldDefs registers every distinct custom field the export mentions and
// returns the source id -> definition id map the values are keyed by.
//
// A field that fails to register is not fatal. Its values are dropped and the
// problem is reported, because the alternative is refusing an entire import
// over one field of a type this version does not know.
func upsertFieldDefs(ctx context.Context, store Store, orgID string, tasks []Task) (map[string]string, []string) {
	seen := map[string]FieldValue{}
	for _, t := range tasks {
		for _, f := range t.Fields {
			if f.FieldID != "" {
				if _, ok := seen[f.FieldID]; !ok {
					seen[f.FieldID] = f
				}
			}
		}
	}

	// The source's own status, kept verbatim alongside the mapped one.
	//
	// Hearth's lifecycle is five states and that is deliberate -- boards,
	// filters, the calendar, the timeline and the metrics all work on it, and
	// widening it to fit one importer would push a vendor's vocabulary through
	// the whole application.
	//
	// But mapping is lossy and the loss is real. These eight spaces use two
	// different status sets, and both "done" and "closed (hidden)" collapse to
	// done while "to do" and "not started" both collapse to todo. Somebody
	// asking "was this closed or actually finished" has nowhere to look once
	// that is gone.
	//
	// So the raw value is stored as a field. The lifecycle stays canonical and
	// the detail survives, which is the same trade the mapped priority makes.
	tags := map[string]bool{}
	for _, t := range tasks {
		for _, g := range t.Tags {
			if g = strings.TrimSpace(g); g != "" {
				tags[g] = true
			}
		}
	}
	if len(tags) > 0 {
		opts := make([]string, 0, len(tags))
		for v := range tags {
			opts = append(opts, v)
		}
		sort.Strings(opts)
		// A field rather than a tag table, because this application has no tag
		// table -- there is nowhere else for these to go. As a select they are
		// at least filterable, which is most of what a tag was for.
		seen[tagsFieldID] = FieldValue{
			FieldID: tagsFieldID,
			Name:    "ClickUp tags",
			Type:    "select",
			Options: opts,
		}
	}

	statuses := map[string]bool{}
	for _, t := range tasks {
		if v := strings.TrimSpace(t.Status); v != "" {
			statuses[v] = true
		}
	}
	if len(statuses) > 0 {
		opts := make([]string, 0, len(statuses))
		for v := range statuses {
			opts = append(opts, v)
		}
		sort.Strings(opts)
		seen[statusFieldID] = FieldValue{
			FieldID: statusFieldID,
			Name:    "ClickUp status",
			Type:    "select",
			Options: opts,
		}
	}

	ids := make(map[string]string, len(seen))
	var problems []string
	for extID, f := range seen {
		id, err := store.UpsertImportedProperty(ctx, orgID, docs.ImportedProperty{
			Label:      f.Name,
			DataType:   f.Type,
			Options:    f.Options,
			Source:     Source,
			ExternalID: extID,
		})
		if err != nil {
			if len(problems) < 10 {
				problems = append(problems, fmt.Sprintf("custom field %q: %v", f.Name, err))
			}
			continue
		}
		ids[extID] = id
	}
	return ids, problems
}

func upsert(ctx context.Context, store Store, orgID string, actor core.Subjects,
	t Task, kind string, fieldIDs map[string]string, spaceID *string,
	people map[string]string, res *Result) (string, error) {

	status := MapStatus(t.Status)
	title := strings.TrimSpace(t.Title)
	if title == "" {
		title = "Untitled ClickUp task"
	}
	body := buildBody(t)

	// Only tasks that carried an id can be matched on a re-run. One without an
	// id is created every time, which is the honest behaviour: there is nothing
	// to recognise it by.
	var existing string
	if t.ExternalID != "" {
		var err error
		existing, err = store.FindByExternalID(ctx, orgID, Source, t.ExternalID)
		if err != nil {
			return "", err
		}
	}

	if existing != "" {
		// Everything the create path sets, set again. A re-run exists to
		// correct what an earlier one got wrong, and a correction that only
		// touches title, body and status leaves the assignee, the dates and
		// the custom values exactly as broken as they were.
		_, err := store.Update(ctx, orgID, actor, existing, docs.UpdateParams{
			Title:     &title,
			Body:      &body,
			CreatedAt: t.CreatedAt,
			UpdatedAt: t.UpdatedAt,
			Task: &docs.TaskPatch{
				Status:      &status,
				Priority:    &t.Priority,
				DueAt:       t.DueAt,
				StartedAt:   t.StartAt,
				CompletedAt: t.CompletedAt,
				AssigneeID:  matchAssignee(t.Assignees, people),
				// A history brought over says what was finished: closing the
				// rest on the way in would invent work done.
				KeepSubtasks: true,
			},
		})
		if err != nil {
			return "", err
		}
		setValues(ctx, store, orgID, actor, existing, t, fieldIDs)
		setEffort(ctx, store, orgID, actor, existing, t)
		setAssignees(ctx, store, existing, t, people)
		res.Updated++
		return existing, nil
	}

	p := docs.CreateParams{
		Kind: kind, Title: title, Body: body, SpaceID: spaceID,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		// Attributed to whoever raised it there, where that person exists
		// here. Otherwise it falls back to the importer, which is the honest
		// answer rather than a guess.
		CreatedBy: deref(matchAssignee([]string{t.Creator}, people)),
	}
	if docs.HasTaskFields(kind) {
		p.Task = &docs.TaskFields{
			Status: status, Priority: t.Priority,
			DueAt:       t.DueAt,
			StartedAt:   t.StartAt,
			CompletedAt: t.CompletedAt,
			AssigneeID:  matchAssignee(t.Assignees, people),
		}
	}
	d, err := store.Create(ctx, orgID, actor, p)
	if err != nil {
		return "", err
	}
	res.Created++
	setValues(ctx, store, orgID, actor, d.ID, t, fieldIDs)
	setEffort(ctx, store, orgID, actor, d.ID, t)
	setChecklist(ctx, store, orgID, actor, d.ID, t)
	setAssignees(ctx, store, d.ID, t, people)

	if t.ExternalID != "" {
		if err := store.RecordExternalRef(ctx, orgID, d.Ref(), docs.ExternalRef{
			Source: Source, ExternalID: t.ExternalID, URL: t.URL,
		}); err != nil {
			// The document exists and is usable; only the ability to recognise
			// it on a future import is lost. Worth reporting, not worth failing.
			return d.ID, fmt.Errorf("imported but not linked to its ClickUp id: %w", err)
		}
	}
	return d.ID, nil
}

// buildBody keeps the things that have no column of their own, rather than
// discarding them. Assignees are names from another system and cannot be
// resolved to principals here, so they are recorded as text instead of being
// silently dropped or, worse, guessed at by matching on a display name.
func buildBody(t Task) string {
	var b strings.Builder
	b.WriteString(t.Body)
	var notes []string
	if len(t.Assignees) > 0 {
		notes = append(notes, "Assigned in ClickUp to "+strings.Join(t.Assignees, ", "))
	}
	if t.List != "" {
		notes = append(notes, "ClickUp list: "+t.List)
	}
	if t.URL != "" {
		notes = append(notes, "Original: "+t.URL)
	}
	if len(notes) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("---\n")
		for _, n := range notes {
			b.WriteString("\n- " + n)
		}
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return "unnamed task"
}

// setValues writes the task's custom values against the definitions registered
// for them.
//
// Failure here is deliberately not fatal and not counted as a failed task. The
// document exists with its title, body, status and assignee; losing a custom
// value is a smaller loss than refusing the row that carries them, and a task
// reported as failed is one somebody goes looking for in the source.
func setValues(ctx context.Context, store Store, orgID string, actor core.Subjects,
	docID string, t Task, fieldIDs map[string]string) {

	values := fieldValues(t, fieldIDs)
	if len(values) == 0 {
		return
	}
	_ = store.SetProperties(ctx, orgID, actor, docID, values)
}

// fieldValues keys a task's custom values by definition id, as the strings the
// properties API takes.
//
// A value whose definition failed to register is dropped rather than stored
// under the source's own id: a values map mixing two key spaces is one nothing
// can render, and the failure is already reported against the definition.
func fieldValues(t Task, fieldIDs map[string]string) map[string]string {
	if len(fieldIDs) == 0 {
		return nil
	}
	out := make(map[string]string, len(t.Fields)+1)
	if id, ok := fieldIDs[tagsFieldID]; ok && len(t.Tags) > 0 {
		out[id] = strings.Join(t.Tags, ", ")
	}
	if id, ok := fieldIDs[statusFieldID]; ok {
		if v := strings.TrimSpace(t.Status); v != "" {
			out[id] = v
		}
	}
	for _, f := range t.Fields {
		id, ok := fieldIDs[f.FieldID]
		if !ok {
			continue
		}
		if v := stringifyValue(f.Value); v != "" {
			out[id] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stringifyValue renders a decoded custom value as text.
//
// Lists and objects become JSON rather than Go's default formatting: a
// multi-select rendered by %v is `[a b c]`, which is not a value anything can
// read back.
func stringifyValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		// Whole numbers should not arrive as 1.2e+06. Every JSON number decodes
		// as a float64 and most custom values here are integers.
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Importing a whole workspace
// ---------------------------------------------------------------------------

// DefaultKindBySpace is the mapping proposed from reading the Fireball
// Industries workspace on 2026-08-18, and it is a default rather than a rule:
// `kind` is a CHECK constraint on a column, so any of these can be changed in
// bulk afterwards.
//
// The reasoning, space by space, because "everything becomes a task" is what
// this is here to avoid:
//
//	White Team Service  18 folders of service delivery. These are work orders
//	                    in everything but name, and calling them tasks is the
//	                    exact confusion the importer's own comment warns about.
//	HR                  Time-off requests, salaries, interview recommendations,
//	                    e-signature routing. None of these has a lifecycle a
//	                    task board models, and forcing them into one produces a
//	                    column of things that are never "done". As documents
//	                    they arrive intact and readable.
//	everything else     Ordinary work with an assignee and a status.
//
// Sales is deliberately a task rather than anything cleverer. Its custom fields
// say Lead/Deal/Account/Contact, and hearth already has contacts and companies
// -- but a CRM record and the work of chasing it are different objects, and
// inventing a mapping that silently creates companies from task rows is worse
// than importing the work and linking it by hand.
var DefaultKindBySpace = map[string]string{
	"White Team Service": docs.KindWorkOrder,
	"HR":                 docs.KindDoc,
}

// WorkspaceResult is the per-space breakdown of a whole-workspace import.
type WorkspaceResult struct {
	Result
	// BySpace is keyed by space name, so somebody reading the outcome sees it
	// in the same shape they see their workspace.
	BySpace map[string]*Result
	// Documents maps each source task id to what it became here, so a later
	// pass -- comments, say -- can attach to the right thing without walking
	// the whole workspace again.
	Documents map[string]string
}

// Fetcher is the reading half of the ClickUp client, declared here so a
// workspace import can be tested without a network.
type Fetcher interface {
	Lists(ctx context.Context) ([]List, error)
	TasksIn(ctx context.Context, listID string) ([]Task, error)
	Members(ctx context.Context) ([]Person, error)
}

// CommentFetcher reads a task's discussion. Separate from Fetcher because it
// costs a request per task and is therefore optional.
type CommentFetcher interface {
	CommentsOn(ctx context.Context, taskID string) ([]Comment, error)
}

// ProvisionMembers creates an account for everybody the workspace knows about,
// so their work arrives assigned to them rather than to nobody.
//
// Safe to do before an SSO rollout, and this is the part worth being sure of:
// a provisioned account carries a credential row with an empty password hash,
// which exists precisely so the address resolves. Hearth's OIDC sign-in adopts
// an existing account by that address. So somebody provisioned here and signing
// in through Vord tomorrow lands on this same principal, with these same tasks
// already assigned, rather than on a second empty one.
//
// Returns how many accounts now exist for the workspace's members.
func ProvisionMembers(ctx context.Context, store Store, orgID string, f Fetcher) (int, []string, error) {
	members, err := f.Members(ctx)
	if err != nil {
		return 0, nil, err
	}
	var problems []string
	n := 0
	for _, m := range members {
		if _, err := store.ProvisionPerson(ctx, orgID, m.ExternalID, m.DisplayName, m.Email); err != nil {
			problems = appendCapped(problems, fmt.Sprintf("%s: %v", m.Email, err))
			continue
		}
		n++
	}
	return n, problems, nil
}

// RunWorkspace imports every list in the workspace, choosing each list's kind
// from the space it sits in.
//
// One list at a time, and a list that fails does not stop the others, for the
// same reason a task that fails does not stop its list: a migration that
// abandons 8,000 rows because list 41 of 167 timed out is one nobody can
// finish, and re-running is safe because every task is matched on its ClickUp
// id.
func RunWorkspace(ctx context.Context, store Store, orgID string, actor core.Subjects,
	f Fetcher, opt Options) (*WorkspaceResult, error) {

	lists, err := f.Lists(ctx)
	if err != nil {
		return nil, err
	}

	kinds := opt.KindBySpace
	if kinds == nil {
		kinds = DefaultKindBySpace
	}
	fallback := opt.Kind
	if fallback == "" {
		fallback = docs.KindTask
	}

	out := &WorkspaceResult{BySpace: map[string]*Result{}, Documents: map[string]string{}}
	tree := &spaceTree{made: map[string]string{}}

	// Counted before the loop so progress can say "of what". Lists with no
	// tasks are skipped, so counting all of them would report a total the run
	// never reaches.
	total := 0
	for _, l := range lists {
		if l.Tasks > 0 {
			total++
		}
	}

	done := 0
	for _, l := range lists {
		if l.Tasks == 0 {
			continue
		}
		done++
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		kind := fallback
		if k, ok := kinds[l.Space]; ok {
			kind = k
		}

		tasks, err := f.TasksIn(ctx, l.ID)
		if err != nil {
			out.Problems = appendCapped(out.Problems,
				fmt.Sprintf("list %q in %q: %v", l.Name, l.Space, err))
			continue
		}
		spaceID := tree.spaceFor(ctx, store, orgID, actor, l)
		res, ids, err := runTasksInSpaceIDs(ctx, store, orgID, actor, tasks, kind, spaceID)
		for ext, id := range ids {
			out.Documents[ext] = id
		}
		if err != nil {
			out.Problems = appendCapped(out.Problems,
				fmt.Sprintf("list %q in %q: %v", l.Name, l.Space, err))
			continue
		}

		sp := out.BySpace[l.Space]
		if sp == nil {
			sp = &Result{Kind: kind}
			out.BySpace[l.Space] = sp
		}
		sp.Created += res.Created
		sp.Updated += res.Updated
		sp.Failed += res.Failed
		sp.Total += res.Total
		out.Created += res.Created
		out.Updated += res.Updated
		out.Failed += res.Failed
		out.Total += res.Total
		out.Problems = appendCapped(out.Problems, res.Problems...)

		if opt.Progress != nil {
			opt.Progress(done, total, l, res)
		}
	}
	return out, nil
}

// appendCapped keeps a problem list readable. Twenty is enough to see the shape
// of what went wrong; four thousand is a wall nobody reads, and the counts
// already say how much failed.
func appendCapped(dst []string, add ...string) []string {
	for _, s := range add {
		if len(dst) >= 20 {
			return dst
		}
		dst = append(dst, s)
	}
	return dst
}

// ---------------------------------------------------------------------------
// Mirroring the hierarchy
// ---------------------------------------------------------------------------

// spaceTree builds Hearth spaces to match the source's own shape, once each.
//
// ClickUp nests space -> folder -> list, and Hearth's spaces table has carried
// parent_id and three kinds since the substrate landed while nothing ever set
// space_id on anything. An import is the first thing with a real hierarchy to
// put there, and filing 1,132 tasks into one undifferentiated pile would waste
// the only chance to do it from a structure somebody already curated.
//
// The mapping, and why each kind:
//
//	ClickUp space   -> area     the top of a tree somebody organises by
//	ClickUp folder  -> folder   a grouping inside it
//	ClickUp list    -> project  the thing tasks actually belong to
//
// A list with no folder hangs directly off the area, because that is exactly
// what it is in ClickUp and inventing an intermediate "General" folder would be
// a structure nobody asked for.
type spaceTree struct {
	// made is keyed by the path built so far, so "Ops" and "Ops/Safety" and
	// "Ops/Safety/Weekly" each get created exactly once no matter how many
	// lists arrive under them.
	made map[string]string
}

// spaceFor returns the Hearth space a list's tasks belong in, creating the
// chain above it if this is the first list to need it.
//
// Failure is soft on purpose: a task with no space is still a task, and losing
// 1,132 rows because one space could not be created would be the wrong trade.
// The document simply lands unfiled, which is where every document was before
// this existed.
func (t *spaceTree) spaceFor(ctx context.Context, store Store, orgID string,
	actor core.Subjects, l List) *string {

	// The source's top level is matched by name anywhere, not only at the root.
	//
	// This is what stops an import undoing the org chart. Departments here are
	// arranged the way the company is -- Red Team sits under Engineering --
	// while ClickUp has it at the top with everything else. Matching only at
	// the root meant the import could not see the moved one, created a second
	// Red Team beside Engineering, and filed all the work into that: the org
	// chart still looked right and every department under it was empty.
	parent := t.ensureAnywhere(ctx, store, orgID, actor, l.Space)
	if l.Folder != "" {
		parent = t.ensure(ctx, store, orgID, actor, l.Folder, "folder", parent,
			l.Space+"/"+l.Folder)
	}
	// The list itself. Named with its folder in the key rather than only its
	// own name, because two folders in one space routinely both contain a list
	// called "Backlog" and they are not the same list.
	return t.ensure(ctx, store, orgID, actor, l.Name, "project", parent,
		l.Space+"/"+l.Folder+"/"+l.Name)
}

func (t *spaceTree) ensure(ctx context.Context, store Store, orgID string,
	actor core.Subjects, name, kind string, parent *string, key string) *string {

	if id, ok := t.made[key]; ok {
		if id == "" {
			return nil
		}
		return &id
	}
	id, err := store.CreateSpace(ctx, orgID, actor, name, kind, parent)
	if err != nil {
		// Remembered as a failure so the next list under it does not retry and
		// fail again once per list.
		t.made[key] = ""
		return nil
	}
	t.made[key] = id
	return &id
}

// ---------------------------------------------------------------------------
// People
// ---------------------------------------------------------------------------

// Person is the slice of an account an import needs to recognise an assignee.
type Person struct {
	PrincipalID string
	DisplayName string
	Email       string
	// ExternalID is the source's own id for this person, so a re-run updates
	// the same account instead of matching on a display name somebody changed.
	ExternalID string
}

// resolvePeople builds the lookup an assignee is matched against.
//
// Keyed by both email and display name, lowercased, because ClickUp gives a
// username on some exports and an email on others and the same person has to
// be found either way.
//
// Failure returns an empty map rather than an error. An import that refuses to
// run because the directory could not be read is worse than one that brings
// the work in unassigned: the titles, bodies, statuses and dates are still
// right, and the assignee is a field somebody can set.
func resolvePeople(ctx context.Context, store Store, orgID string) map[string]string {
	list, err := store.People(ctx, orgID)
	if err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(list)*2)
	for _, p := range list {
		if e := strings.ToLower(strings.TrimSpace(p.Email)); e != "" {
			out[e] = p.PrincipalID
		}
		if n := strings.ToLower(strings.TrimSpace(p.DisplayName)); n != "" {
			// Names collide in a way emails do not. First writer wins and the
			// second is left unassigned rather than assigned to the wrong
			// person, which is the failure nobody would notice.
			if _, taken := out[n]; !taken {
				out[n] = p.PrincipalID
			}
		}
	}
	return out
}

// matchAssignee picks the principal for a task's assignees.
//
// ClickUp allows several and Hearth's task fields hold one, so the first that
// matches somebody here wins. The full list stays in the body either way, so
// nothing is lost by taking one -- and a task assigned to a real person is
// worth more than a task assigned to nobody because it named two.
func matchAssignee(names []string, people map[string]string) *string {
	if len(people) == 0 {
		return nil
	}
	for _, n := range names {
		if id, ok := people[strings.ToLower(strings.TrimSpace(n))]; ok {
			return &id
		}
	}
	return nil
}

// setEffort carries the estimate and the tracked time across.
//
// A quarter of the tasks in this workspace carry tracked time and a tenth carry
// an estimate. Dropping them makes a migrated job impossible to invoice from
// and impossible to learn from: "how long did that actually take" is the
// question the next quote depends on.
//
// Recorded as ONE entry per task rather than the individual sessions. ClickUp
// reports time_spent as a total on the task; the underlying intervals are a
// separate endpoint per task, which for 1,132 tasks is 1,132 more requests to
// reconstruct a breakdown nothing here displays. The total is what the rollups
// read.
//
// The entry is attributed to whoever runs the import, because time_spent is a
// workspace total and does not say who spent it. That is honest rather than
// convenient: inventing a per-person split would put hours against names that
// never worked them, and a timesheet that is wrong per-person is worse than one
// that is only right in total.
func setEffort(ctx context.Context, store Store, orgID string, actor core.Subjects,
	docID string, t Task) {

	if t.Estimate > 0 {
		if m := int(t.Estimate.Minutes()); m > 0 && m <= 100000 {
			_ = store.SetEstimate(ctx, orgID, actor, docID, &m)
		}
	}
	// Replace rather than append. LogTime inserts, so without this every
	// re-run would add a second copy of the same hours and a task worked for
	// four would report twelve after three passes. Only entries this importer
	// wrote are removed -- time somebody logged here by hand is theirs.
	_ = store.ClearImportedTime(ctx, orgID, docID, ImportedTimeNote)

	if t.Spent > 0 {
		// Rounded up, because a task with forty seconds on it did have work
		// done on it and zero minutes says it did not.
		m := int((t.Spent + 59*time.Second).Minutes())
		if m > 0 && m <= 100000 {
			// Dated to the task's start where there is one, so the hours land
			// in the period the work happened rather than all on migration
			// day, which would make every report for this month wrong.
			on := time.Now()
			if t.StartAt != nil {
				on = *t.StartAt
			} else if t.DueAt != nil {
				on = *t.DueAt
			}
			_ = store.LogTime(ctx, orgID, actor, docID, m, on, ImportedTimeNote)
		}
	}
}

// setChecklist rebuilds a task's steps.
//
// Only on creation, never on an update. Checklist items have no stable id from
// the source, so there is nothing to match them on, and re-running the import
// would otherwise append the same steps again on every pass -- a task with four
// steps would have twelve after three runs. Losing an edit somebody made here
// is the smaller harm than silently multiplying the list.
func setChecklist(ctx context.Context, store Store, orgID string, actor core.Subjects,
	docID string, t Task) {

	for _, item := range t.Checklist {
		id, err := store.AddChecklistItem(ctx, orgID, actor, docID, item.Text)
		if err != nil {
			// One bad step is not a reason to abandon the rest of the list.
			continue
		}
		if item.Done {
			_ = store.SetChecklistItemDone(ctx, orgID, actor, id, true)
		}
	}
}

// deref turns an optional principal id into the empty string when absent,
// which is what CreateParams reads as "use the acting principal".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------------------------------------------------------------------------
// The knowledge base
// ---------------------------------------------------------------------------

// DocFetcher reads the workspace's documents.
type DocFetcher interface {
	Docs(ctx context.Context, teamID string) ([]Doc, error)
}

// RunDocs imports the workspace's documents as wiki pages.
//
// A second pass rather than part of the task walk, because they are a different
// thing: prose somebody wrote, not work somebody owes. They arrive as kind
// wiki, under a space of their own, with their original dates.
//
// Idempotent on the document's ClickUp id, same as tasks, so re-running updates
// rather than duplicating.
func RunDocs(ctx context.Context, store Store, orgID string, actor core.Subjects,
	f DocFetcher, teamID string, opt Options) (*Result, error) {

	list, err := f.Docs(ctx, teamID)
	if err != nil {
		return nil, err
	}
	res := &Result{Total: len(list), Kind: docs.KindWiki}

	// One space to hold them. The tasks mirror ClickUp's tree because they had
	// one; documents are workspace-wide there and would otherwise land unfiled.
	var spaceID *string
	if id, err := store.CreateSpace(ctx, orgID, actor, "Knowledge base", "area", nil); err == nil {
		spaceID = &id
	}

	for _, d := range list {
		title := strings.TrimSpace(d.Name)
		if title == "" {
			title = "Untitled ClickUp doc"
		}
		existing, err := store.FindByExternalID(ctx, orgID, Source, d.ID)
		if err != nil {
			res.Failed++
			continue
		}

		if existing != "" {
			body := d.Content
			if _, err := store.Update(ctx, orgID, actor, existing, docs.UpdateParams{
				Title: &title, Body: &body,
			}); err != nil {
				res.Failed++
				res.Problems = appendCapped(res.Problems, fmt.Sprintf("%s: %v", title, err))
				continue
			}
			res.Updated++
			continue
		}

		doc, err := store.Create(ctx, orgID, actor, docs.CreateParams{
			Kind: docs.KindWiki, Title: title, Body: d.Content,
			BodyFormat: "markdown",
			SpaceID:    spaceID,
			CreatedAt:  d.CreatedAt, UpdatedAt: d.UpdatedAt,
		})
		if err != nil {
			res.Failed++
			res.Problems = appendCapped(res.Problems, fmt.Sprintf("%s: %v", title, err))
			continue
		}
		res.Created++
		_ = store.RecordExternalRef(ctx, orgID, doc.Ref(), docs.ExternalRef{
			Source: Source, ExternalID: d.ID,
		})
		if opt.Progress != nil {
			opt.Progress(res.Created+res.Updated, len(list), List{Name: title}, res)
		}
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Discussion
// ---------------------------------------------------------------------------

// RunComments brings the conversation across, for tasks already imported.
//
// A separate pass, and last, for a reason worth stating: it costs one request
// per task against a limit of a hundred a minute, so on this workspace it is
// twelve minutes of doing nothing else. Running it after the work means a run
// somebody kills half way through has already delivered the tasks, and only the
// discussion is missing -- which can be filled in later by running this again.
//
// Threading is preserved where ClickUp reports a parent. Replies whose parent
// has not been seen yet are attached flat rather than dropped: an out-of-order
// reply is still something somebody said.
func RunComments(ctx context.Context, store Store, orgID string, actor core.Subjects,
	f CommentFetcher, tasks map[string]string, people map[string]string,
	fallbackPrincipal string, progress func(done, total int)) (int, []string) {

	posted := 0
	var problems []string
	done := 0
	for externalID, docID := range tasks {
		done++
		if progress != nil {
			progress(done, len(tasks))
		}
		if ctx.Err() != nil {
			return posted, problems
		}
		comments, err := f.CommentsOn(ctx, externalID)
		if err != nil {
			problems = appendCapped(problems, fmt.Sprintf("comments on %s: %v", externalID, err))
			continue
		}

		// Oldest first, so a reply is written after the thing it replies to and
		// the parent id is already known.
		sort.SliceStable(comments, func(i, j int) bool {
			if comments[i].At == nil || comments[j].At == nil {
				return false
			}
			return comments[i].At.Before(*comments[j].At)
		})

		byExternal := make(map[string]string, len(comments))
		for _, cm := range comments {
			// Already imported? Skip it.
			//
			// PostComment inserts, so without this a second comments pass adds
			// a second copy of every remark and a thread of four reads as
			// eight. Recorded against the comment's own ClickUp id, prefixed
			// so it cannot collide with a task id in the same table.
			key := commentRefPrefix + cm.ID
			if seen, err := store.FindByExternalID(ctx, orgID, Source, key); err == nil && seen != "" {
				byExternal[cm.ID] = seen
				continue
			}
			author := fallbackPrincipal
			if id, ok := people[cm.Author]; ok {
				author = id
			}
			at := time.Time{}
			if cm.At != nil {
				at = *cm.At
			}
			parent := ""
			if cm.ParentID != "" {
				parent = byExternal[cm.ParentID]
			}
			id, err := store.PostComment(ctx, orgID, author, docID, cm.Body, at, parent)
			if err != nil {
				continue
			}
			byExternal[cm.ID] = id
			_ = store.RecordExternalRef(ctx, orgID,
				core.Ref{Type: core.EntityMessage, ID: id},
				docs.ExternalRef{Source: Source, ExternalID: key})
			posted++
		}
	}
	return posted, problems
}

// setAssignees records everybody the task was assigned to.
//
// ClickUp allows several and task_fields holds one, so the column keeps the
// first that resolves and this keeps the rest. Without it every assignee after
// the first survives only as prose in the body: readable by a person, invisible
// to the workload page, the board and every "assigned to me" filter.
//
// Names that resolve to nobody here are skipped rather than guessed at. They
// remain in the body, which is where somebody can still read who it was.
func setAssignees(ctx context.Context, store Store, docID string, t Task, people map[string]string) {
	if len(t.Assignees) == 0 || len(people) == 0 {
		return
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(t.Assignees))
	for _, name := range t.Assignees {
		id, ok := people[strings.ToLower(strings.TrimSpace(name))]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}
	// The first resolvable one is primary, matching what the task field holds,
	// so the two cannot disagree about who owns it.
	_ = store.SetAssignees(ctx, docID, ids, ids[0])
}

// ensureAnywhere finds the source's top-level space wherever it has been filed,
// and only creates one if it is genuinely absent.
//
// Deliberately different from ensure, which matches on a parent: the levels
// below the top are the source's own structure and belong where the source puts
// them, but the top level is the one a person reorganises into a department.
func (t *spaceTree) ensureAnywhere(ctx context.Context, store Store, orgID string,
	actor core.Subjects, name string) *string {

	key := "top:" + name
	if id, ok := t.made[key]; ok {
		if id == "" {
			return nil
		}
		return &id
	}
	if id, err := store.FindSpaceByName(ctx, orgID, name); err == nil && id != "" {
		t.made[key] = id
		return &id
	}
	id, err := store.CreateSpace(ctx, orgID, actor, name, "area", nil)
	if err != nil {
		t.made[key] = ""
		return nil
	}
	t.made[key] = id
	return &id
}
