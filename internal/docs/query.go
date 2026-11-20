package docs

// A view is a question asked of the spine: which rows, in what order, grouped
// how. This file compiles that question into one SQL statement.
//
// Everything is compiled, and nothing is post-filtered. Applying a filter in Go
// after the rows come back breaks LIMIT, because the database does not know
// which rows will survive, so page one returns forty rows instead of fifty and
// page two silently skips the ten that were discarded. That failure looks like
// missing data with no error anywhere, which is the worst kind.
//
// The consequence is that a field is only filterable if it has a SQL
// expression, and every one of them is in the table below. Nothing reaches the
// statement that did not come out of that map, which is also why a user-typed
// field name cannot become SQL.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Field names one thing a view can filter, sort, or group on: either a built-in
// column of the spine, or a custom property by its key.
//
// Exactly one of the two is set. A property named "status" and the built-in
// status are different fields, and keeping them in separate slots is what stops
// one being mistaken for the other.
type Field struct {
	Column   string `json:"column,omitempty"`
	Property string `json:"property,omitempty"`
}

// Col and Prop build the two kinds of field.
func Col(name string) Field      { return Field{Column: name} }
func Prop(key string) Field      { return Field{Property: key} }
func (f Field) IsProperty() bool { return f.Property != "" }
func (f Field) Zero() bool       { return f.Column == "" && f.Property == "" }

// Key is the field's stable name in a URL: "status", or "prop.asset_tag".
func (f Field) Key() string {
	if f.IsProperty() {
		return "prop." + f.Property
	}
	return f.Column
}

// ParseField reads the URL form back.
func ParseField(s string) Field {
	if key, ok := strings.CutPrefix(s, "prop."); ok {
		return Prop(key)
	}
	return Col(s)
}

// Filter operators. Deliberately few: each one has to be indexable or be worth
// not being, and an operator nobody can explain in a sentence gets used wrongly.
const (
	OpIs       = "is"
	OpIsNot    = "is_not"
	OpAnyOf    = "any_of"
	OpContains = "contains"
	OpBefore   = "before"
	OpAfter    = "after"
	OpEmpty    = "is_empty"
	OpNotEmpty = "not_empty"
)

var validOps = map[string]bool{
	OpIs: true, OpIsNot: true, OpAnyOf: true, OpContains: true,
	OpBefore: true, OpAfter: true, OpEmpty: true, OpNotEmpty: true,
}

// Filter is one condition. Filters compose with AND, because OR across
// different fields is a query builder, and a query builder is a project.
type Filter struct {
	Field  Field    `json:"field"`
	Op     string   `json:"op"`
	Values []string `json:"values,omitempty"`
}

// Sort is one ordering term. Several compose left to right.
type Sort struct {
	Field Field `json:"field"`
	Desc  bool  `json:"desc,omitempty"`
}

// View kinds. The same query renders four ways; the kind decides which.
const (
	ViewTable    = "table"
	ViewBoard    = "board"
	ViewCalendar = "calendar"
	ViewTimeline = "timeline"
)

var validViewKinds = map[string]bool{
	ViewTable: true, ViewBoard: true, ViewCalendar: true, ViewTimeline: true,
}

// ViewKinds is the offered order, which is also how the tabs read.
var ViewKinds = []string{ViewTable, ViewBoard, ViewCalendar, ViewTimeline}

// ValidViewKind reports whether a name is a way this can render. The database
// has the same CHECK; this is so a bad value fails before the round trip.
func ValidViewKind(k string) bool { return validViewKinds[k] }

// Query is a whole view: what to include, how to order it, how to group it, and
// which columns a table should show.
//
// It serialises to the jsonb stored in saved_views and to the query string in
// the address bar, and those are the same structure on purpose. A saved view
// and a link to a filtered list should not be able to drift apart.
type Query struct {
	Kind    string  `json:"kind,omitempty"`
	SpaceID *string `json:"space_id,omitempty"`

	Filters []Filter `json:"filters,omitempty"`
	Sorts   []Sort   `json:"sorts,omitempty"`
	GroupBy Field    `json:"group_by,omitempty"`
	// Ungrouped says somebody chose "Nothing" for Group by. It is not the same as
	// never having chosen: a board and a table group by status when nothing was
	// said, and without this "Nothing" was read as "nothing was said", so choosing
	// it put the grouping straight back.
	Ungrouped bool    `json:"ungrouped,omitempty"`
	Columns   []Field `json:"columns,omitempty"`

	// Closed asks for done and cancelled rows too. A list hides them by
	// default -- see the exclusion built in the web layer's renderList --
	// and this is what a "Show closed" toggle flips. It is its own field
	// rather than another status filter so it stays independent of the
	// Filter popover's status chips: picking "Done" there still means "only
	// done", not "done as well as everything else".
	Closed bool `json:"closed,omitempty"`

	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// columnKind decides how a value from the URL is turned into a bind parameter.
type columnKind int

const (
	kindText columnKind = iota
	kindNumber
	kindTime
	kindID
)

// column is one built-in field of the spine.
//
// sel and filt differ where it matters: an id is selected as text so Go can
// scan it into a string, and filtered as a uuid so the index is usable. Casting
// the column instead of the parameter is the classic way to make an index
// disappear.
type column struct {
	sel   string
	filt  string
	kind  columnKind
	label string
	// task is true for fields that only exist on things with a lifecycle. A
	// filter on one of these excludes documents that have no task_fields row,
	// which is correct: an asset has no status, so it is not "todo".
	task bool
}

// columns is the whole filterable, sortable, groupable surface. Adding a field
// to a view means adding a row here, and nothing else.
var columns = map[string]column{
	"title":     {sel: "d.title", filt: "d.title", kind: kindText, label: "Title"},
	"kind":      {sel: "d.kind", filt: "d.kind", kind: kindText, label: "Kind"},
	"owner":     {sel: "d.owner_id::text", filt: "d.owner_id", kind: kindID, label: "Owner"},
	"space":     {sel: "d.space_id::text", filt: "d.space_id", kind: kindID, label: "Space"},
	"created":   {sel: "d.created_at", filt: "d.created_at", kind: kindTime, label: "Created"},
	"updated":   {sel: "d.updated_at", filt: "d.updated_at", kind: kindTime, label: "Updated"},
	"status":    {sel: "t.status", filt: "t.status", kind: kindText, label: "Status", task: true},
	"priority":  {sel: "t.priority", filt: "t.priority", kind: kindNumber, label: "Priority", task: true},
	"assignee":  {sel: "t.assignee_id::text", filt: "t.assignee_id", kind: kindID, label: "Assignee", task: true},
	"due":       {sel: "t.due_at", filt: "t.due_at", kind: kindTime, label: "Due", task: true},
	"started":   {sel: "t.started_at", filt: "t.started_at", kind: kindTime, label: "Started", task: true},
	"completed": {sel: "t.completed_at", filt: "t.completed_at", kind: kindTime, label: "Completed", task: true},
}

// ColumnLabel returns the human name for a built-in column.
func ColumnLabel(name string) string {
	if c, ok := columns[name]; ok {
		return c.label
	}
	return name
}

// KnownColumn reports whether a name is a real field.
func KnownColumn(name string) bool { _, ok := columns[name]; return ok }

// TableColumns is the default set a table shows when nobody has chosen.
var TableColumns = []Field{Col("title"), Col("kind"), Col("status"), Col("priority"), Col("due"), Col("updated")}

// SelectableColumns lists the built-in fields a table can show, in the order
// they are offered.
var SelectableColumns = []string{
	"title", "kind", "status", "priority", "assignee", "due",
	"started", "completed", "owner", "created", "updated",
}

// GroupableColumns lists the built-in fields a board can group by. Grouping by
// a date or a title produces one column per row, which is a list with extra
// steps, so those are left out.
var GroupableColumns = []string{"status", "priority", "assignee", "kind", "owner"}

// -----------------------------------------------------------------------------
// URL encoding
// -----------------------------------------------------------------------------

// ParseQuery reads a view out of a query string.
//
// Unknown fields and operators are DROPPED rather than rejected. A link that
// outlived a deleted property should show a slightly wider list, not an error
// page: the person who was sent it did nothing wrong and cannot fix it.
func ParseQuery(v url.Values) Query {
	q := Query{Kind: v.Get("kind")}
	if s := v.Get("space"); s != "" {
		q.SpaceID = &s
	}

	// status= is kept as a plain shorthand because it predates views and is
	// what the filter chips on every list page already link to.
	if s := v.Get("status"); s != "" {
		q.Filters = append(q.Filters, Filter{Field: Col("status"), Op: OpIs, Values: []string{s}})
	}
	q.Closed = v.Get("closed") == "1"

	for _, raw := range v["f"] {
		if f, ok := parseFilter(raw); ok {
			q.Filters = append(q.Filters, f)
		}
	}
	for _, raw := range v["sort"] {
		for _, term := range strings.Split(raw, ",") {
			if s, ok := parseSort(term); ok {
				q.Sorts = append(q.Sorts, s)
			}
		}
	}
	if g := v.Get("group"); g != "" {
		q.GroupBy = ParseField(g)
	} else if v.Has("group") {
		q.Ungrouped = true
	}
	for _, raw := range v["cols"] {
		for _, name := range strings.Split(raw, ",") {
			if name = strings.TrimSpace(name); name != "" {
				q.Columns = append(q.Columns, ParseField(name))
			}
		}
	}
	if n, err := strconv.Atoi(v.Get("offset")); err == nil && n > 0 {
		q.Offset = n
	}
	return q
}

// parseFilter reads "field:op" or "field:op:value,value".
func parseFilter(raw string) (Filter, bool) {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 2 {
		return Filter{}, false
	}
	f := Filter{Field: ParseField(parts[0]), Op: parts[1]}
	if !validOps[f.Op] {
		return Filter{}, false
	}
	if len(parts) == 3 && parts[2] != "" {
		f.Values = strings.Split(parts[2], ",")
	}
	if f.Op != OpEmpty && f.Op != OpNotEmpty && len(f.Values) == 0 {
		return Filter{}, false
	}
	return f, true
}

func parseSort(term string) (Sort, bool) {
	term = strings.TrimSpace(term)
	if term == "" {
		return Sort{}, false
	}
	name, dir, _ := strings.Cut(term, ":")
	return Sort{Field: ParseField(name), Desc: dir == "desc"}, true
}

// Encode renders the query back into a query string, so a view opened from a
// saved row produces the same URL somebody could have typed.
func (q Query) Encode() url.Values {
	v := url.Values{}
	if q.Kind != "" {
		v.Set("kind", q.Kind)
	}
	if q.SpaceID != nil {
		v.Set("space", *q.SpaceID)
	}
	if q.Closed {
		v.Set("closed", "1")
	}
	for _, f := range q.Filters {
		s := f.Field.Key() + ":" + f.Op
		if len(f.Values) > 0 {
			s += ":" + strings.Join(f.Values, ",")
		}
		v.Add("f", s)
	}
	if len(q.Sorts) > 0 {
		terms := make([]string, len(q.Sorts))
		for i, s := range q.Sorts {
			terms[i] = s.Field.Key()
			if s.Desc {
				terms[i] += ":desc"
			}
		}
		v.Set("sort", strings.Join(terms, ","))
	}
	if !q.GroupBy.Zero() {
		v.Set("group", q.GroupBy.Key())
	} else if q.Ungrouped {
		v.Set("group", "")
	}
	if len(q.Columns) > 0 {
		names := make([]string, len(q.Columns))
		for i, c := range q.Columns {
			names[i] = c.Key()
		}
		v.Set("cols", strings.Join(names, ","))
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	return v
}

// WithFilter returns a copy carrying one more condition, for building links.
func (q Query) WithFilter(f Filter) Query {
	out := q
	out.Filters = append(append([]Filter{}, q.Filters...), f)
	return out
}

// Has reports whether a filter on this field with this value is already
// applied, so a control can render itself as on.
func (q Query) Has(field Field, value string) bool {
	for _, f := range q.Filters {
		if f.Field == field && len(f.Values) == 1 && f.Values[0] == value {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Compilation
// -----------------------------------------------------------------------------

// builder accumulates SQL and its bind parameters together, so a fragment can
// never be written with the wrong placeholder number.
type builder struct {
	args []any
}

func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// propValue is the scalar subquery for one property, used for sorting and
// grouping. It reads the value as jsonb, which orders numbers numerically
// because that is how they were stored.
func (b *builder) propValue(orgID, key string) string {
	return fmt.Sprintf(`(SELECT pv.value FROM property_values pv
	         JOIN property_defs pd ON pd.id = pv.def_id
	        WHERE pv.entity_type = 'document' AND pv.entity_id = d.id
	          AND pd.org_id = %s AND pd.key = %s)`, b.arg(orgID), b.arg(key))
}

// propMatches is the filter form, written with @> because that is the shape the
// GIN index on (def_id, value) can actually answer. Equality on the same pair
// indexes the def and then scans every value it has, which was measured rather
// than assumed. See migration 00007.
//
// The value is tried as both a JSON string and a JSON number when the text
// parses as one. A definition has a single data type, so only one of the two
// can ever be present and the OR cannot produce a false match.
func (b *builder) propMatches(orgID, key, raw string) string {
	encoded := []string{mustJSON(raw)}
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		encoded = append(encoded, mustJSON(n))
	}
	terms := make([]string, len(encoded))
	for i, e := range encoded {
		terms[i] = "pv.value @> " + b.arg(e) + "::jsonb"
	}
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM property_values pv
	         JOIN property_defs pd ON pd.id = pv.def_id
	        WHERE pv.entity_type = 'document' AND pv.entity_id = d.id
	          AND pd.org_id = %s AND pd.key = %s AND (%s))`,
		b.arg(orgID), b.arg(key), strings.Join(terms, " OR "))
}

// propSet is "this property has any value at all".
func (b *builder) propSet(orgID, key string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM property_values pv
	         JOIN property_defs pd ON pd.id = pv.def_id
	        WHERE pv.entity_type = 'document' AND pv.entity_id = d.id
	          AND pd.org_id = %s AND pd.key = %s)`, b.arg(orgID), b.arg(key))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Marshalling a string or a float64 cannot fail. If it somehow does,
		// a value that matches nothing is safer than a value that matches all.
		return `null`
	}
	return string(b)
}

// where compiles one filter, or returns "" if it names something that does not
// exist. Dropping is deliberate: see ParseQuery.
func (b *builder) where(orgID string, f Filter) string {
	if f.Field.IsProperty() {
		return b.propWhere(orgID, f)
	}
	c, ok := columns[f.Field.Column]
	if !ok {
		return ""
	}
	// Assignee is really two tables: the primary (t.assignee_id, what the board
	// and grouping use) and the rest (document_assignees). "Assigned to me" has
	// to match either, or a task somebody is on but is not the primary of would
	// not show in their list. Grouping and sorting keep using the column's plain
	// sel, so they stay single-valued.
	if f.Field.Column == "assignee" {
		return b.assigneeWhere(f)
	}
	switch f.Op {
	case OpEmpty:
		return c.filt + " IS NULL"
	case OpNotEmpty:
		return c.filt + " IS NOT NULL"
	}
	if len(f.Values) == 0 {
		return ""
	}

	switch f.Op {
	case OpIs:
		v, ok := b.bind(c.kind, f.Values[0])
		if !ok {
			return ""
		}
		return c.filt + " = " + v
	case OpIsNot:
		v, ok := b.bind(c.kind, f.Values[0])
		if !ok {
			return ""
		}
		return "(" + c.filt + " IS DISTINCT FROM " + v + ")"
	case OpAnyOf:
		list, ok := b.bindList(c.kind, f.Values)
		if !ok {
			return ""
		}
		return c.filt + " = ANY(" + list + ")"
	case OpContains:
		if c.kind != kindText {
			return ""
		}
		// Anchored nowhere, so it is a scan by construction. Acceptable on a
		// title; it is what people mean by "contains".
		return c.filt + " ILIKE '%' || " + b.arg(f.Values[0]) + " || '%'"
	case OpBefore:
		v, ok := b.bind(c.kind, f.Values[0])
		if !ok {
			return ""
		}
		return c.filt + " < " + v
	case OpAfter:
		v, ok := b.bind(c.kind, f.Values[0])
		if !ok {
			return ""
		}
		return c.filt + " > " + v
	}
	return ""
}

// assigneeWhere matches the primary assignee or any of the extra assignees.
// member(v) is the extra-assignee test for one person; anyAssigned is "has any
// assignee at all", for the empty/not-empty cases.
func (b *builder) assigneeWhere(f Filter) string {
	member := func(v string) string {
		return "EXISTS (SELECT 1 FROM document_assignees da WHERE da.document_id = d.id AND da.principal_id = " + v + ")"
	}
	anyAssigned := "EXISTS (SELECT 1 FROM document_assignees da WHERE da.document_id = d.id)"
	switch f.Op {
	case OpEmpty:
		return "(t.assignee_id IS NULL AND NOT " + anyAssigned + ")"
	case OpNotEmpty:
		return "(t.assignee_id IS NOT NULL OR " + anyAssigned + ")"
	}
	if len(f.Values) == 0 {
		return ""
	}
	switch f.Op {
	case OpIs:
		v, ok := b.bind(kindID, f.Values[0])
		if !ok {
			return ""
		}
		return "(t.assignee_id = " + v + " OR " + member(v) + ")"
	case OpIsNot:
		v, ok := b.bind(kindID, f.Values[0])
		if !ok {
			return ""
		}
		return "(t.assignee_id IS DISTINCT FROM " + v + " AND NOT " + member(v) + ")"
	case OpAnyOf:
		list, ok := b.bindList(kindID, f.Values)
		if !ok {
			return ""
		}
		return "(t.assignee_id = ANY(" + list + ") OR EXISTS (SELECT 1 FROM document_assignees da WHERE da.document_id = d.id AND da.principal_id = ANY(" + list + ")))"
	}
	return ""
}

func (b *builder) propWhere(orgID string, f Filter) string {
	key := f.Field.Property
	switch f.Op {
	case OpEmpty:
		return "NOT " + b.propSet(orgID, key)
	case OpNotEmpty:
		return b.propSet(orgID, key)
	}
	if len(f.Values) == 0 {
		return ""
	}
	switch f.Op {
	case OpIs:
		return b.propMatches(orgID, key, f.Values[0])
	case OpIsNot:
		return "NOT " + b.propMatches(orgID, key, f.Values[0])
	case OpAnyOf:
		terms := make([]string, 0, len(f.Values))
		for _, v := range f.Values {
			terms = append(terms, b.propMatches(orgID, key, v))
		}
		return "(" + strings.Join(terms, " OR ") + ")"
	case OpContains:
		// #>> '{}' unwraps a jsonb scalar to text. Not indexable, and honest
		// about it: substring search across every value of a property is a
		// scan whichever way it is written.
		return fmt.Sprintf(`EXISTS (SELECT 1 FROM property_values pv
		         JOIN property_defs pd ON pd.id = pv.def_id
		        WHERE pv.entity_type = 'document' AND pv.entity_id = d.id
		          AND pd.org_id = %s AND pd.key = %s
		          AND pv.value #>> '{}' ILIKE '%%' || %s || '%%')`,
			b.arg(orgID), b.arg(key), b.arg(f.Values[0]))
	}
	return ""
}

// bind turns one URL string into a typed parameter. A value that will not parse
// makes the whole filter vanish rather than becoming a zero that matches rows
// nobody asked for.
func (b *builder) bind(k columnKind, raw string) (string, bool) {
	switch k {
	case kindNumber:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return "", false
		}
		return b.arg(n), true
	case kindTime:
		t, ok := parseDate(raw)
		if !ok {
			return "", false
		}
		return b.arg(t), true
	case kindID:
		if !looksLikeUUID(raw) {
			return "", false
		}
		return b.arg(raw) + "::uuid", true
	default:
		return b.arg(raw), true
	}
}

func (b *builder) bindList(k columnKind, raw []string) (string, bool) {
	switch k {
	case kindNumber:
		out := make([]int, 0, len(raw))
		for _, s := range raw {
			n, err := strconv.Atoi(s)
			if err != nil {
				return "", false
			}
			out = append(out, n)
		}
		return b.arg(out), true
	case kindTime:
		out := make([]time.Time, 0, len(raw))
		for _, s := range raw {
			t, ok := parseDate(s)
			if !ok {
				return "", false
			}
			out = append(out, t)
		}
		return b.arg(out), true
	case kindID:
		for _, s := range raw {
			if !looksLikeUUID(s) {
				return "", false
			}
		}
		return b.arg(raw) + "::uuid[]", true
	default:
		return b.arg(raw), true
	}
}

// parseDate accepts a date or a full timestamp, because a due-date control
// sends the first and a saved view can carry the second.
func parseDate(raw string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// compile builds the WHERE clause shared by Search and Count, so a count can
// never disagree with the rows it is counting.
func (b *builder) compile(orgID string, actor core.Subjects, q Query) string {
	var sb strings.Builder
	sb.WriteString("d.org_id = " + b.arg(orgID) + " AND d.deleted_at IS NULL")

	// The permission filter, in the query. Everything else in this file is
	// about correctness of results; this one is about not leaking them.
	//
	// Through core so a super admin gets the same answer here as on the
	// document page. They used to get different ones: the capability let them
	// into the approvals queue and the grant did not let them open what it
	// listed, so the queue showed entries whose documents returned 404.
	sb.WriteString(" AND " + actor.GrantsExists(core.EntityDocument, "d.id", b.arg))

	if q.Kind != "" {
		sb.WriteString(" AND d.kind = " + b.arg(q.Kind))
	}
	if q.SpaceID != nil && looksLikeUUID(*q.SpaceID) {
		// The subtree, not the one node.
		//
		// Spaces are a hierarchy -- department, sub-department, project -- and
		// the work sits at the bottom of it. A department holds no documents
		// directly, so an exact match makes "show me Engineering's work" return
		// nothing at all while Engineering plainly has hundreds of jobs under
		// it. That reads as a broken page rather than as a narrow filter.
		//
		// Depth is bounded so a cycle introduced by a bad move cannot spin
		// here; MoveSpace refuses cycles, and this does not depend on that
		// staying true.
		sb.WriteString(` AND d.space_id IN (
			WITH RECURSIVE subtree AS (
			    SELECT id, 0 AS depth FROM spaces WHERE id = ` + b.arg(*q.SpaceID) + `::uuid
			  UNION ALL
			    SELECT c.id, p.depth + 1 FROM spaces c
			    JOIN subtree p ON c.parent_id = p.id
			    WHERE p.depth < 10
			) SELECT id FROM subtree)`)
	}
	for _, f := range q.Filters {
		if frag := b.where(orgID, f); frag != "" {
			sb.WriteString(" AND (" + frag + ")")
		}
	}
	return sb.String()
}

// orderBy compiles the sort terms.
//
// It always ends with d.id. Without a total order, two rows that tie on the
// sort key can come back in either order between two queries, so paging through
// a list can show the same row twice and never show another one at all.
func (b *builder) orderBy(orgID string, q Query) string {
	terms := make([]string, 0, len(q.Sorts)+1)
	for _, s := range q.Sorts {
		var expr string
		if s.Field.IsProperty() {
			expr = b.propValue(orgID, s.Field.Property)
		} else if c, ok := columns[s.Field.Column]; ok {
			expr = c.sel
		} else {
			continue
		}
		dir := " ASC"
		if s.Desc {
			dir = " DESC"
		}
		// NULLS LAST in both directions. A row with no due date is not the most
		// urgent thing on the list, which is what NULLS FIRST on an ascending
		// sort would put it.
		terms = append(terms, expr+dir+" NULLS LAST")
	}
	if len(terms) == 0 {
		terms = append(terms, "d.updated_at DESC")
	}
	terms = append(terms, "d.id")
	return strings.Join(terms, ", ")
}

// -----------------------------------------------------------------------------
// Reading
// -----------------------------------------------------------------------------

// selectList is the columns every read of the spine pulls. One constant, so a
// scanner written against it cannot fall out of step with a caller.
const selectList = `d.id::text, d.org_id::text, d.kind, d.title, d.body, d.body_format,
	       d.space_id::text, d.channel_id::text, d.owner_id::text, d.version,
	       d.created_at, d.updated_at, d.parent_id::text,
	       t.status, t.priority, t.assignee_id::text, t.due_at, t.started_at, t.completed_at`

// docScan holds the loosely typed destinations the LEFT JOIN forces, and turns
// them into a Document once the row has been read.
type docScan struct {
	d                       Document
	status, assignee        any
	due, started, completed any
	priority                *int16
}

func (s *docScan) dest() []any {
	return []any{
		&s.d.ID, &s.d.OrgID, &s.d.Kind, &s.d.Title, &s.d.Body, &s.d.BodyFormat,
		&s.d.SpaceID, &s.d.ChannelID, &s.d.OwnerID, &s.d.Version,
		&s.d.CreatedAt, &s.d.UpdatedAt, &s.d.ParentID,
		&s.status, &s.priority, &s.assignee, &s.due, &s.started, &s.completed,
	}
}

// done assembles the document. Task fields are present only when the LEFT JOIN
// found a row, which is how a work order gets a lifecycle and an asset does not.
func (s *docScan) done() *Document {
	d := s.d
	if s.status != nil {
		t := &TaskFields{}
		if v, ok := s.status.(string); ok {
			t.Status = v
		}
		if s.priority != nil {
			t.Priority = *s.priority
		}
		if v, ok := s.assignee.(string); ok {
			t.AssigneeID = &v
		}
		if v, ok := s.due.(time.Time); ok {
			// A due date is a date, entered and stored as midnight UTC. The driver
			// hands it back in the server's own zone, which west of UTC is the evening
			// before, so every page that printed it printed the day before and the
			// date field on the document form saved it a day earlier each time. It is
			// read as the UTC instant it was written as.
			v = v.UTC()
			t.DueAt = &v
		}
		if v, ok := s.started.(time.Time); ok {
			t.StartedAt = &v
		}
		if v, ok := s.completed.(time.Time); ok {
			t.CompletedAt = &v
		}
		d.Task = t
	}
	return &d
}

// Row is a document plus the value of whatever field the view groups by.
type Row struct {
	*Document
	// Group is the grouped field rendered as text, empty when the field is
	// unset on this row or the view groups by nothing.
	Group string
}

// maxRows bounds a view. A board with ten thousand cards is not a board, and an
// unbounded query is how one slow page takes the database with it.
const maxRows = 500

// Search runs a compiled view and returns the rows the caller may see.
func (s *Service) Search(ctx context.Context, orgID string, actor core.Subjects, q Query) ([]*Row, error) {
	limit := q.Limit
	if limit <= 0 || limit > maxRows {
		limit = maxRows
	}

	b := &builder{}
	groupExpr := "''"
	if !q.GroupBy.Zero() {
		if q.GroupBy.IsProperty() {
			// #>> '{}' unwraps the scalar, so a grouped column header reads
			// "CV-3" rather than "\"CV-3\"".
			groupExpr = "COALESCE(" + b.propValue(orgID, q.GroupBy.Property) + " #>> '{}', '')"
		} else if c, ok := columns[q.GroupBy.Column]; ok {
			groupExpr = "COALESCE(" + c.sel + "::text, '')"
		}
	}

	where := b.compile(orgID, actor, q)
	order := b.orderBy(orgID, q)
	sql := fmt.Sprintf(`SELECT %s, %s AS grp
		FROM documents d
		LEFT JOIN task_fields t ON t.document_id = d.id
		WHERE %s
		ORDER BY %s
		LIMIT %s OFFSET %s`,
		selectList, groupExpr, where, order, b.arg(limit), b.arg(q.Offset))

	rows, err := s.db.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, fmt.Errorf("docs: search: %w", err)
	}
	defer rows.Close()

	var out []*Row
	for rows.Next() {
		var sc docScan
		var group string
		if err := rows.Scan(append(sc.dest(), &group)...); err != nil {
			return nil, fmt.Errorf("docs: scan search row: %w", err)
		}
		out = append(out, &Row{Document: sc.done(), Group: group})
	}
	return out, rows.Err()
}

// Count returns how many rows the view matches, ignoring limit and offset.
//
// It compiles the same WHERE as Search, from the same code, which is the only
// way a count and a list can be guaranteed to agree.
func (s *Service) Count(ctx context.Context, orgID string, actor core.Subjects, q Query) (int, error) {
	b := &builder{}
	where := b.compile(orgID, actor, q)
	sql := `SELECT count(*) FROM documents d
	        LEFT JOIN task_fields t ON t.document_id = d.id
	        WHERE ` + where

	var n int
	if err := s.db.QueryRow(ctx, sql, b.args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("docs: count: %w", err)
	}
	return n, nil
}

// PropertyDefByKey loads one definition, for a board that needs the option
// order of the select it is grouped by.
func (s *Service) PropertyDefByKey(ctx context.Context, orgID, key string) (*PropertyDef, error) {
	var p PropertyDef
	var rawOptions []byte
	err := s.db.QueryRow(ctx, `
		SELECT id::text, key::text, display_name, data_type, options
		FROM property_defs WHERE org_id = $1 AND key = $2`, orgID, key).
		Scan(&p.ID, &p.Key, &p.Label, &p.DataType, &rawOptions)
	switch {
	case err == pgx.ErrNoRows:
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("docs: property def %q: %w", key, err)
	}
	if len(rawOptions) > 0 {
		_ = json.Unmarshal(rawOptions, &p.Options)
	}
	return &p, nil
}

// PropertyDefsFor lists the definitions that apply to a kind, without values,
// for building filter and group controls.
func (s *Service) PropertyDefsFor(ctx context.Context, orgID, kind string) ([]PropertyDef, error) {
	sql := `SELECT id::text, key::text, display_name, data_type, options
	        FROM property_defs WHERE org_id = $1`
	args := []any{orgID}
	if kind != "" {
		sql += ` AND $2 = ANY(applies_to)`
		args = append(args, kind)
	}
	sql += ` ORDER BY display_name`

	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("docs: property defs: %w", err)
	}
	defer rows.Close()

	var out []PropertyDef
	for rows.Next() {
		var p PropertyDef
		var rawOptions []byte
		if err := rows.Scan(&p.ID, &p.Key, &p.Label, &p.DataType, &rawOptions); err != nil {
			return nil, fmt.Errorf("docs: scan property def: %w", err)
		}
		if len(rawOptions) > 0 {
			_ = json.Unmarshal(rawOptions, &p.Options)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetField applies a card being dragged into a different board column: the
// field the board groups by takes the value of the column it landed in.
//
// No version check, deliberately, unlike every other write in this package. A
// drag carries exactly one field and cannot overwrite a body or a title, so the
// thing optimistic concurrency exists to protect is not at risk. Requiring the
// version instead would mean a board that anybody else has touched refuses
// every drag until it is reloaded, and a board that argues with you is a board
// people stop using.
func (s *Service) SetField(ctx context.Context, orgID string, actor core.Subjects,
	docID string, field Field, value string) error {

	if field.IsProperty() {
		def, err := s.PropertyDefByKey(ctx, orgID, field.Property)
		if err != nil {
			return err
		}
		return s.SetProperties(ctx, orgID, actor, docID, map[string]string{def.ID: value})
	}

	patch := &TaskPatch{}
	switch field.Column {
	case "status":
		if !validStatuses[value] {
			return fmt.Errorf("%w: %q", ErrField, value)
		}
		patch.Status = &value
	case "priority":
		n, err := strconv.ParseInt(value, 10, 16)
		if err != nil {
			return fmt.Errorf("%w: priority %q is not a number", ErrField, value)
		}
		p := int16(n)
		patch.Priority = &p
	case "assignee":
		if value == "" {
			patch.ClearAssignee = true
		} else if looksLikeUUID(value) {
			patch.AssigneeID = &value
		} else {
			return fmt.Errorf("%w: %q is not a principal", ErrField, value)
		}
	default:
		// Kind and owner are shown on a board and are not moved by dragging.
		// Refusing here rather than silently doing nothing means a board that
		// offers a drag it cannot honour fails visibly in testing.
		return fmt.Errorf("%w: %s cannot be set by moving a card", ErrField, field.Key())
	}

	_, err := s.Update(ctx, orgID, actor, docID, UpdateParams{Task: patch})
	return err
}

// -----------------------------------------------------------------------------
// Grouping
// -----------------------------------------------------------------------------

// Group is one column of a board.
type Group struct {
	Value string
	Label string
	Rows  []*Row
}

// GroupRows arranges search results into board columns.
//
// This is rendering, not filtering: the rows arrived already restricted and
// already ordered, and arranging them into columns cannot change which ones a
// caller sees. The ordering of the columns themselves comes from `order` when
// the field has a natural sequence, so a board reads todo to done rather than
// alphabetically, which would put "blocked" first and "todo" last.
func GroupRows(rows []*Row, order []string, labels map[string]string) []Group {
	byValue := map[string][]*Row{}
	var seen []string
	for _, r := range rows {
		if _, ok := byValue[r.Group]; !ok {
			seen = append(seen, r.Group)
		}
		byValue[r.Group] = append(byValue[r.Group], r)
	}

	label := func(v string) string {
		if l, ok := labels[v]; ok {
			return l
		}
		if v == "" {
			return "Not set"
		}
		// A status reads as words; any other value is left as it is.
		return StatusLabel(v)
	}

	var out []Group
	placed := map[string]bool{}
	// The declared order first, including columns that are empty: a board that
	// hides "blocked" whenever nothing is blocked has nowhere to drag a card
	// that just became blocked.
	for _, v := range order {
		out = append(out, Group{Value: v, Label: label(v), Rows: byValue[v]})
		placed[v] = true
	}
	// Then anything the rows turned up that the order did not predict, in the
	// order the rows arrived, which is the sort the caller asked for.
	for _, v := range seen {
		if !placed[v] {
			out = append(out, Group{Value: v, Label: label(v), Rows: byValue[v]})
		}
	}
	// The unset column goes last and only when it has something in it.
	if !placed[""] {
		for i, g := range out {
			if g.Value == "" && len(g.Rows) == 0 {
				out = append(out[:i], out[i+1:]...)
				break
			}
		}
	}
	return out
}

// SearchWatched is Search narrowed to documents this person follows.
//
// Watching is how somebody keeps hearing about work they are not assigned to,
// and it is the only record of what they care about. Without this a dashboard
// can show what is theirs and what is everybody's, and nothing in between --
// which is where most of the useful answers live.
//
// The permission predicate is unchanged. Following something is not a grant,
// and a watch on a document somebody has since lost access to must not become
// a way back in: the join narrows the set, it never widens it.
func (s *Service) SearchWatched(ctx context.Context, orgID string, actor core.Subjects,
	principalID string, q Query) ([]*Row, error) {

	limit := q.Limit
	if limit <= 0 || limit > maxRows {
		limit = maxRows
	}
	b := &builder{}
	where := b.compile(orgID, actor, q)
	order := b.orderBy(orgID, q)
	sql := fmt.Sprintf(`SELECT %s, '' AS grp
		FROM documents d
		LEFT JOIN task_fields t ON t.document_id = d.id
		JOIN watches w ON w.entity_type = 'document' AND w.entity_id = d.id
		    AND w.principal_id = %s
		WHERE %s
		ORDER BY %s
		LIMIT %s OFFSET %s`,
		selectList, b.arg(principalID), where, order, b.arg(limit), b.arg(q.Offset))

	rows, err := s.db.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, fmt.Errorf("docs: watched: %w", err)
	}
	defer rows.Close()

	var out []*Row
	for rows.Next() {
		var sc docScan
		var group string
		if err := rows.Scan(append(sc.dest(), &group)...); err != nil {
			return nil, fmt.Errorf("docs: scan watched row: %w", err)
		}
		out = append(out, &Row{Document: sc.done(), Group: group})
	}
	return out, rows.Err()
}
