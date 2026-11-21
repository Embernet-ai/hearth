package stores

// A person who signs in to Hearth for the first time, or whose approver changes, has to exist in EmberOPS before
// anything Hearth does as them can work: the User (the bearer token is refused with a 401 for an email EmberOPS has
// no User for), the Employee that names them (the time off, time and approval records belong to an Employee, and
// ERPNext strips the Employee role from a User with no Employee record), and the hearth_id on both. emberops-migrate
// made them once for everybody who existed on 2026-11-20; nothing made them for the next person, so a new staff
// account had to be set up by hand in EmberOPS before it could file time off or have its time off decided.
//
// This is that step, run from the sign-in. Nothing here grants a role: the Employee role comes from Vord at the
// person's first token (the site's realm_role_grants, emberops.tf in embernet-iac), and it now sticks because the
// Employee exists by then. It is safe to run again for anybody, and it never removes anything.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// PersonSource reads a person as the User and Employee records are made from.
type PersonSource interface {
	Person(ctx context.Context, orgID, principalID string) (mapping.Rec, error)
}

// People makes sure a person exists in EmberOPS.
type People struct {
	Base
	Source PersonSource
	Log    *slog.Logger
}

func (p *People) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Ensure makes the User and the Employee, with their hearth ids, and the Employee's approver link. The approver is
// made first when they are missing, so the link can name them; their own approver is left to their own sign-in.
func (p *People) Ensure(ctx context.Context, orgID, principalID string) error {
	// Hearth's own account does this, not the person: the person has no User to act as yet.
	ctx = emberops.WithUser(ctx, nil)
	rec, err := p.Source.Person(ctx, orgID, principalID)
	if err != nil {
		return err
	}
	if !rec.Has("email") {
		return nil // an agent or a bot: no sign-in, no EmberOPS account
	}
	approver := rec.S("approver_id")
	if approver != "" && approver != principalID {
		if arec, err := p.Source.Person(ctx, orgID, approver); err == nil && arec.Has("email") {
			arec["approver_id"] = nil
			if err := p.put(ctx, arec); err != nil {
				p.log().Warn("emberops: could not make the approver's account, so the link is left off", "person", principalID, "error", err)
				rec["approver_id"] = nil
			}
		} else {
			rec["approver_id"] = nil
		}
	}
	return p.put(ctx, rec)
}

func (p *People) put(ctx context.Context, rec mapping.Rec) error {
	id := rec.S("id")
	user := mapping.UserDoc(rec)
	delete(user, "doctype")
	delete(user, "hearth_id")
	if _, err := p.C.Upsert(ctx, "User", id, user); err != nil {
		return fmt.Errorf("emberops: make the User for %s: %w", id, err)
	}
	emp := mapping.EmployeeDoc(rec)
	delete(emp, "doctype")
	delete(emp, "hearth_id")
	if _, err := p.C.Upsert(ctx, "Employee", id, emp); err != nil {
		return fmt.Errorf("emberops: make the Employee for %s: %w", id, err)
	}
	return nil
}

// PGPerson reads people from Hearth's own database.
type PGPerson struct{ DB *pgxpool.Pool }

// Person is the record the migration's users and employees steps read, plus the approver: the person's named approver,
// else the one manager of their department when there is exactly one (two managers name nobody, because the link would
// shut the other one out of deciding).
func (s PGPerson) Person(ctx context.Context, orgID, principalID string) (mapping.Rec, error) {
	var id, name, kind, status string
	var handle, email, approver *string
	var minutes *int
	err := s.DB.QueryRow(ctx, `
		SELECT p.id::text, p.display_name, p.kind, p.status, p.handle::text,
		       COALESCE((SELECT lc.email::text FROM local_credentials lc WHERE lc.principal_id = p.id),
		                (SELECT ui.email::text FROM user_identities ui WHERE ui.principal_id = p.id AND ui.email <> '' ORDER BY ui.updated_at DESC LIMIT 1)),
		       COALESCE(p.approver_id::text, (
		           SELECT CASE WHEN count(DISTINCT m.principal_id) = 1 THEN min(m.principal_id::text) END
		             FROM principal_departments pd JOIN space_managers m ON m.space_id = pd.space_id
		            WHERE pd.principal_id = p.id AND pd.org_id = p.org_id AND m.principal_id <> p.id)),
		       (SELECT c.minutes_per_week FROM capacity c WHERE c.principal_id = p.id)
		  FROM principals p WHERE p.id = $2 AND p.org_id = $1`, orgID, principalID).
		Scan(&id, &name, &kind, &status, &handle, &email, &approver, &minutes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("emberops: no such person %s", principalID)
	}
	if err != nil {
		return nil, fmt.Errorf("emberops: read person: %w", err)
	}
	rec := mapping.Rec{"id": id, "display_name": name, "kind": kind, "status": status}
	if handle != nil {
		rec["handle"] = *handle
	}
	if email != nil {
		rec["email"] = *email
	}
	if approver != nil {
		rec["approver_id"] = *approver
	}
	if minutes != nil {
		rec["minutes_per_week"] = float64(*minutes)
	}
	return rec, nil
}
