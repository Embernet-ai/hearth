package auth

// Roles from the sign-in provider.
//
// A group or role in the person's token can give them a capability, and losing the group takes it back at their
// next sign-in. Two rules keep that safe.
//
// What the provider gave is recorded, so what it takes back is exactly that and never a capability somebody was
// handed by hand in Hearth. And the owner, the account that claimed the installation, is left alone entirely: a
// provider with a mistaken group list must not be able to strip the one person who can fix it.
//
// Super admin is not mappable. Making somebody a super admin is a different kind of act from letting them see the
// audit log, and a group in somebody else's directory is not where it should be decided.

import (
	"context"
	"fmt"
	"slices"

	"github.com/Embernet-ai/hearth/internal/authz"
)

// MappableCapabilities are the capabilities a role map may name.
var MappableCapabilities = []string{
	string(authz.Finance), string(authz.TimeAll), string(authz.Audit), string(authz.People), string(authz.Agents),
}

// ApplyIdPRoles makes a person's provider-given capabilities exactly want.
func (s *Service) ApplyIdPRoles(ctx context.Context, orgID, principalID string, want []string) error {
	if owner, err := s.OwnerID(ctx, orgID); err == nil && owner == principalID {
		return nil
	}
	for _, w := range want {
		if !slices.Contains(MappableCapabilities, w) {
			return fmt.Errorf("auth: %q cannot be given by a sign-in provider", w)
		}
	}
	owner, err := s.OwnerID(ctx, orgID)
	if err != nil {
		return fmt.Errorf("auth: find the owner: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	given := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT capability FROM idp_capability_grants WHERE org_id=$1 AND principal_id=$2`, orgID, principalID)
	if err != nil {
		return fmt.Errorf("auth: read provider grants: %w", err)
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return err
		}
		given[c] = true
	}
	rows.Close()

	for _, c := range want {
		if given[c] {
			continue
		}
		// granted_by is the owner: the grant has to name a person, and the provider is not one.
		ct, err := tx.Exec(ctx, `
			INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
			VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, orgID, principalID, c, owner)
		if err != nil {
			return fmt.Errorf("auth: grant %s: %w", c, err)
		}
		// Only record it when this row is the one that made it. A capability already held by hand is not the
		// provider's to take back.
		if ct.RowsAffected() > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO idp_capability_grants (org_id, principal_id, capability) VALUES ($1,$2,$3)
				ON CONFLICT DO NOTHING`, orgID, principalID, c); err != nil {
				return err
			}
		}
	}
	for c := range given {
		if slices.Contains(want, c) {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM capability_grants WHERE org_id=$1 AND principal_id=$2 AND capability=$3`,
			orgID, principalID, c); err != nil {
			return fmt.Errorf("auth: take back %s: %w", c, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM idp_capability_grants WHERE org_id=$1 AND principal_id=$2 AND capability=$3`,
			orgID, principalID, c); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
