package authz

// Capabilities: which parts of the application somebody may reach.
//
// This is a different question from the rest of this package and it is worth
// being clear about the line. Everything else here answers "may this person
// open this document", per entity, by grant and by level, and it answers it by
// walking the subjects a person carries. A capability is not about a row. It is
// about a section: whether somebody sees money, whether they see anybody's
// hours but their own, whether the audit log is on their rail at all.
//
// They live in the same package because they are the same concern from the
// caller's side. A handler asking "may they" should not have to know which of
// two access systems the answer comes from.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Capability is one named thing a person may be granted.
//
// The strings are the values in the capability_grants CHECK constraint, and a
// new one is a migration. That is deliberate friction: a typo in a capability
// name is a permission that silently never matches, which somebody reads as
// "the page is broken" rather than as "the grant is wrong".
type Capability string

const (
	// SuperAdmin sees everything and hands out the rest. It is a capability
	// rather than a flag beside them so there is one table and one question.
	SuperAdmin Capability = "super_admin"

	// Finance is rates, costs, and anything else with money in it.
	Finance Capability = "finance"

	// TimeAll is everybody's hours, load and capacity. Without it a person
	// sees their own and nobody else's.
	TimeAll Capability = "time_all"

	// Audit is the log of who did what, and from where.
	Audit Capability = "audit"

	// People is colleagues, teams and provisioning.
	People Capability = "people"

	// Agents is machine access: agents, API tokens, webhooks, MCP.
	Agents Capability = "agents"
)

// Grantable is every capability a super admin can hand out, in the order a
// page should list them. SuperAdmin is deliberately absent: making somebody a
// super admin is not the same kind of act as letting them see the audit log,
// and putting it in the same checklist invites it to be done by accident.
var Grantable = []Capability{Finance, TimeAll, Audit, People, Agents}

// Label is what to call this on a page.
func (c Capability) Label() string {
	switch c {
	case SuperAdmin:
		return "Super admin"
	case Finance:
		return "Financial"
	case TimeAll:
		return "Everyone's time"
	case Audit:
		return "Audit log"
	case People:
		return "People and teams"
	case Agents:
		return "Agents and API"
	}
	return string(c)
}

// Describes is the sentence next to the checkbox. Written as what the person
// gains rather than what the flag is called, because the reader is deciding
// about a colleague and not about a schema.
func (c Capability) Describes() string {
	switch c {
	case SuperAdmin:
		return "Everything, including granting these to other people."
	case Finance:
		return "Hourly rates and anything costed from them."
	case TimeAll:
		return "Everybody's hours, load and capacity, not only their own."
	case Audit:
		return "Who did what, and from where."
	case People:
		return "Adding colleagues, managing teams, directory provisioning."
	case Agents:
		return "Agents, API tokens, webhooks."
	}
	return ""
}

// ParseCapability turns a form value into a capability, refusing anything that
// is not one. Refused rather than ignored: a POST naming a capability that does
// not exist is a bug or an attempt, and silently granting nothing makes both
// look like success.
func ParseCapability(s string) (Capability, error) {
	c := Capability(strings.TrimSpace(s))
	if c == SuperAdmin || slices.Contains(Grantable, c) {
		return c, nil
	}
	return "", fmt.Errorf("authz: %q is not a capability", s)
}

// Held is the set one person holds.
type Held map[Capability]bool

// Has reports whether this person may reach something.
//
// Super admin implies everything, resolved here rather than by writing a row
// per capability when somebody is promoted. Six rows would go stale the day a
// seventh capability is added, and whoever added it would have no reason to
// suspect that every existing super admin was now missing it.
func (h Held) Has(c Capability) bool {
	if h == nil {
		return false
	}
	return h[SuperAdmin] || h[c]
}

// IsSuperAdmin reports the capability itself rather than what it implies, for
// the places that have to distinguish. Granting capabilities is one: a person
// with `people` administers colleagues and still must not be able to promote
// themselves.
func (h Held) IsSuperAdmin() bool { return h != nil && h[SuperAdmin] }

// Capabilities reads what somebody holds.
//
// One query, and the caller is expected to hold the result for the length of a
// request rather than asking again per page section. A rail that renders eight
// gated items should not be eight round trips.
func (s *Service) Capabilities(ctx context.Context, orgID, principalID string) (Held, error) {
	rows, err := s.db.Query(ctx,
		`SELECT capability FROM capability_grants
		  WHERE org_id = $1 AND principal_id = $2`, orgID, principalID)
	if err != nil {
		return nil, fmt.Errorf("authz: read capabilities: %w", err)
	}
	defer rows.Close()

	held := Held{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		held[Capability(c)] = true
	}
	return held, rows.Err()
}

// HasCapability answers the same question as Held.Has, for callers that hold a
// principal id rather than a set.
//
// It exists so a feature package can depend on a one-method interface instead
// of importing this one. timelog needs to know whether somebody may see
// everybody's hours, and that is the whole of its interest in access control;
// making it import this package for one bool would drag the entire grant model
// into a package about clocks.
func (s *Service) HasCapability(ctx context.Context, orgID, principalID, capability string) (bool, error) {
	held, err := s.Capabilities(ctx, orgID, principalID)
	if err != nil {
		return false, err
	}
	return held.Has(Capability(capability)), nil
}

// RequireCapability is the guard a handler calls. Returns ErrDenied so it lands
// in the same error handling as every other refusal in this package.
func (s *Service) RequireCapability(ctx context.Context, orgID, principalID string, c Capability) error {
	held, err := s.Capabilities(ctx, orgID, principalID)
	if err != nil {
		return err
	}
	if !held.Has(c) {
		return ErrDenied
	}
	return nil
}

// ErrNotSuperAdmin is returned when somebody who is not a super admin tries to
// change what anybody may reach.
var ErrNotSuperAdmin = errors.New("authz: only a super admin can change what somebody may reach")

// GrantCapability gives somebody a capability.
//
// The actor must be a super admin, checked here rather than trusted from the
// handler. A permission system whose only guard is at the edge is one where the
// second caller, added later by somebody reading a different file, is the hole.
func (s *Service) GrantCapability(ctx context.Context, orgID, actorID, principalID string, c Capability) error {
	actor, err := s.Capabilities(ctx, orgID, actorID)
	if err != nil {
		return err
	}
	if !actor.IsSuperAdmin() {
		return ErrNotSuperAdmin
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (org_id, principal_id, capability) DO NOTHING`,
		orgID, principalID, string(c), actorID)
	if err != nil {
		return fmt.Errorf("authz: grant %s: %w", c, err)
	}
	return nil
}

// RevokeCapability takes one away.
//
// A super admin can stand down as one, and the last one cannot. An installation
// with no super admin has nobody who can grant the capability back, which is a
// state that needs a database client to leave.
func (s *Service) RevokeCapability(ctx context.Context, orgID, actorID, principalID string, c Capability) error {
	actor, err := s.Capabilities(ctx, orgID, actorID)
	if err != nil {
		return err
	}
	if !actor.IsSuperAdmin() {
		return ErrNotSuperAdmin
	}

	if c == SuperAdmin {
		var others int
		if err := s.db.QueryRow(ctx, `
			SELECT count(*) FROM capability_grants
			 WHERE org_id = $1 AND capability = 'super_admin' AND principal_id <> $2`,
			orgID, principalID).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return errors.New("authz: this is the last super admin, and an " +
				"installation with none has nobody who can appoint one")
		}
	}

	_, err = s.db.Exec(ctx, `
		DELETE FROM capability_grants
		 WHERE org_id = $1 AND principal_id = $2 AND capability = $3`,
		orgID, principalID, string(c))
	if err != nil {
		return fmt.Errorf("authz: revoke %s: %w", c, err)
	}
	return nil
}

// HeldBy reads capabilities for several people at once.
//
// The people page lists everybody with what each one holds, and asking per
// person is the same page rendered as twenty-three queries.
func (s *Service) HeldBy(ctx context.Context, orgID string) (map[string]Held, error) {
	rows, err := s.db.Query(ctx,
		`SELECT principal_id::text, capability FROM capability_grants WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("authz: read capabilities: %w", err)
	}
	defer rows.Close()

	all := map[string]Held{}
	for rows.Next() {
		var id, c string
		if err := rows.Scan(&id, &c); err != nil {
			return nil, err
		}
		if all[id] == nil {
			all[id] = Held{}
		}
		all[id][Capability(c)] = true
	}
	return all, rows.Err()
}
