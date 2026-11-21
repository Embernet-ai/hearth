// Package core is the shared domain vocabulary: entity kinds, access levels,
// and subject keys. It holds types and no logic, and it depends on nothing.
//
// It exists so that `tasks` and `docs` and `search` can talk about the same
// nouns without importing each other. It is emphatically NOT a place to put
// models or database structs. The predecessor had a shared data-access package
// that reached 24,769 lines precisely because it started as somewhere sensible
// to put one shared thing.
package core

import (
	"fmt"
	"strings"
)

// EntityType names a kind of thing that can be linked, granted, or given
// properties. Stored as text in the database rather than an enum, because a new
// entity type should be a code change and not a migration.
type EntityType string

const (
	EntityDocument EntityType = "document"
	EntityChannel  EntityType = "channel"
	EntitySpace    EntityType = "space"
	EntityContact  EntityType = "contact"
	EntityCompany  EntityType = "company"
	// EntityDeal is a sales opportunity (internal/crm).
	EntityDeal EntityType = "deal"
	EntityFile EntityType = "file"
	// EntityMessage and EntityPrincipal exist because a mention is a link, and
	// a link needs both ends named. "This message mentions this person" and
	// "this message references this work order" are the same relation as every
	// other one in entity_links, which is why they go there rather than into a
	// mentions table that would need its own indexes and its own cleanup.
	EntityMessage   EntityType = "message"
	EntityPrincipal EntityType = "principal"
	// EntityTimeOff is a time-off request (internal/timeoff), so a
	// notification about one can link to it.
	EntityTimeOff EntityType = "time_off"
	// EntityDepartmentRequest is somebody asking which departments they belong
	// in (internal/deptreq), for the same reason.
	EntityDepartmentRequest EntityType = "department_request"
)

// Ref points at one specific thing.
type Ref struct {
	Type EntityType
	ID   string
}

func (r Ref) String() string { return string(r.Type) + ":" + r.ID }

// AccessLevel is ordered: view < comment < edit < admin. The ordering is the
// whole point, because effective access is the maximum over every applicable
// grant.
type AccessLevel string

const (
	AccessNone    AccessLevel = ""
	AccessView    AccessLevel = "view"
	AccessComment AccessLevel = "comment"
	AccessEdit    AccessLevel = "edit"
	AccessAdmin   AccessLevel = "admin"
)

// rank orders the levels. AccessNone is deliberately zero so that the zero value
// of AccessLevel denies, rather than granting view by accident.
func (a AccessLevel) rank() int {
	switch a {
	case AccessView:
		return 1
	case AccessComment:
		return 2
	case AccessEdit:
		return 3
	case AccessAdmin:
		return 4
	default:
		return 0
	}
}

// AtLeast reports whether a is sufficient for the required level.
func (a AccessLevel) AtLeast(required AccessLevel) bool {
	return a.rank() >= required.rank() && a.rank() > 0
}

// Max returns the higher of two levels.
func Max(a, b AccessLevel) AccessLevel {
	if a.rank() >= b.rank() {
		return a
	}
	return b
}

// Higher returns whichever of two levels grants more.
//
// Exists because a super admin's access is a FLOOR rather than a replacement:
// they get at least edit on everything, and keep admin on anything a grant
// already gave them. Taking one and discarding the other made being promoted
// remove somebody's ability to delete their own work.
func Higher(a, b AccessLevel) AccessLevel {
	if a.rank() >= b.rank() {
		return a
	}
	return b
}

// Valid reports whether a is a level the database will accept.
func (a AccessLevel) Valid() bool { return a.rank() > 0 }

// SubjectKind is the kind of thing a grant can be made to.
type SubjectKind string

const (
	SubjectPrincipal SubjectKind = "principal"
	SubjectChannel   SubjectKind = "channel"
	SubjectTeam      SubjectKind = "team"
	SubjectOrg       SubjectKind = "org"

	// SubjectSuper marks a caller who may reach everything.
	//
	// It is NOT a grant and is never written to grants.subject_key. ParseSubject
	// refuses it for exactly that reason: a row in the grants table claiming
	// this would be a permanent skeleton key that nothing revokes.
	//
	// It exists because "super admin" was a capability and document access is a
	// grant, so a super admin could see an entry in the approvals queue and get
	// a 404 opening the document behind it. The queue said yes and the document
	// said no, and both were working as written.
	//
	// Resolved once, in SubjectsFor, so the bypass costs one query per request
	// rather than one per permission check.
	SubjectSuper SubjectKind = "super"
)

// Subject is one entry in a caller's subject set: something they are, or
// something they belong to, that a grant might have been made to.
type Subject struct {
	Kind SubjectKind
	ID   string
}

// Key renders the subject in the form stored in grants.subject_key.
func (s Subject) Key() string { return string(s.Kind) + ":" + s.ID }

// ParseSubject reads a stored subject_key back into a Subject.
func ParseSubject(key string) (Subject, error) {
	kind, id, ok := strings.Cut(key, ":")
	if !ok || id == "" {
		return Subject{}, fmt.Errorf("core: malformed subject key %q", key)
	}
	switch SubjectKind(kind) {
	case SubjectPrincipal, SubjectChannel, SubjectTeam, SubjectOrg:
		return Subject{Kind: SubjectKind(kind), ID: id}, nil
	default:
		return Subject{}, fmt.Errorf("core: unknown subject kind %q", kind)
	}
}

// Subjects is everything a caller is, for permission purposes: themselves, every
// channel they are in, every team they belong to, and their org.
//
// It is computed once per request and bounded by the caller's own memberships,
// which is tens of entries. That bound is what lets every list query filter on
// it directly instead of maintaining a materialised access table.
type Subjects []Subject

// Keys renders the set for use as a SQL array parameter.
//
// SubjectSuper is left out. It is not a grant and nothing in grants.subject_key
// will ever match it, so including it would be a value shipped to the database
// on every query to be compared against nothing.
func (s Subjects) Keys() []string {
	keys := make([]string, 0, len(s))
	for _, sub := range s {
		if sub.Kind == SubjectSuper {
			continue
		}
		keys = append(keys, sub.Key())
	}
	return keys
}

// GrantsExists renders the permission filter every list query carries: an
// EXISTS against grants, or TRUE for a super admin.
//
// entityID is the column holding the row's id, and arg is whatever the caller
// uses to register a query parameter and get back its placeholder. Written here
// rather than inlined for a twenty-fifth time because the super-admin case has
// to be the same answer everywhere: a filter that lets somebody see a document
// on one page and not another is worse than either answer alone.
//
// For a super admin the keys are never referenced, so arg is not called and no
// parameter is registered. That matters: registering one and not referencing it
// leaves a bind with more parameters than the statement uses.
func (s Subjects) GrantsExists(entityType EntityType, entityID string, arg func(any) string) string {
	if s.Super() {
		return "TRUE"
	}
	return `EXISTS (
	      SELECT 1 FROM grants g
	       WHERE g.entity_type = ` + arg(string(entityType)) + `
	         AND g.entity_id = ` + entityID + `
	         AND g.subject_key = ANY(` + arg(s.Keys()) + `)
	         AND (g.expires_at IS NULL OR g.expires_at > now()))`
}

// Super reports whether this caller may reach everything.
//
// Checked by the two places that decide access, LevelFor and VisibleFilter, so
// a super admin sees every document rather than only the ones somebody
// remembered to share with them.
func (s Subjects) Super() bool {
	for _, sub := range s {
		if sub.Kind == SubjectSuper {
			return true
		}
	}
	return false
}

// Principal returns the acting principal's id, or "" if the set has none.
func (s Subjects) Principal() string {
	for _, sub := range s {
		if sub.Kind == SubjectPrincipal {
			return sub.ID
		}
	}
	return ""
}

// MemberSpaces is a WITH RECURSIVE term named member_spaces: the spaces a
// principal reaches through department membership -- each department they are
// in and everything beneath it. principalParam is the placeholder holding the
// principal's id ("$4"); an empty id reaches nothing.
//
// Membership is never a grant row, and that is the point. Grants on a space
// are copied onto every document filed there, so a department grant shows
// every colleague's work. A member gets the department -- open it, file work
// into it -- and sees only the documents granted to them, their own. One
// definition, used by authz for a single space and by plant for the tree, so
// "what a member can see" cannot mean two things.
func MemberSpaces(principalParam string) string {
	return `member_spaces AS (
	    SELECT pd.space_id AS id, 0 AS depth
	      FROM principal_departments pd
	     WHERE pd.principal_id = NULLIF(` + principalParam + `::text, '')::uuid
	  UNION ALL
	    -- A space marked open_to_all is a department for everybody in its
	    -- organisation, with no row per person. This is how every account can
	    -- file a daily report from day one (migration 00044).
	    -- Not for a contractor account: it reaches what it was given and
	    -- nothing else, and this is everybody's.
	    SELECT s.id, 0 FROM spaces s
	     WHERE s.open_to_all
	       AND s.org_id = (SELECT p.org_id FROM principals p
	                        WHERE p.id = NULLIF(` + principalParam + `::text, '')::uuid
	                          AND NOT p.contractor)
	  UNION ALL
	    SELECT c.id, m.depth + 1 FROM spaces c
	      JOIN member_spaces m ON c.parent_id = m.id
	     WHERE m.depth < 10
	)`
}
