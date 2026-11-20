-- The owner becomes a super admin, so upgrading does not lock them out.
--
-- 00026 introduced capabilities and moved the gates onto them. The owner is a
-- different thing: orgs.owner_id, one person, the one who claimed the
-- installation. Nothing granted them a capability, so the moment those gates
-- went in, the owner lost the workload page, and on a single-admin
-- installation that is everybody losing it with nobody able to grant it back.
--
-- Caught by loading the page as the owner and getting a 403, which is the
-- reason this migration exists rather than a note in a runbook.
--
-- Deliberately generic. No names, no email addresses, nothing about this
-- particular company: it grants to whoever owns each organisation, so it does
-- the right thing on an installation nobody here has seen. The other three
-- super admins are granted through the People page, by a super admin, which is
-- how every grant after this one is meant to happen.

-- +goose Up

-- There is no orgs.owner_id. The owner is defined in auth.OwnerID as the
-- earliest user principal in the organisation, ordered by created_at then id,
-- and this has to use the same definition or it grants to somebody else. The
-- tie-break on id matters: two principals inserted in the same transaction can
-- share a created_at, and without it the "owner" would differ between this
-- migration and the running application.
INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
SELECT owner.org_id, owner.id, 'super_admin', owner.id
  FROM (
      SELECT DISTINCT ON (p.org_id) p.org_id, p.id
        FROM principals p
       WHERE p.kind = 'user'
       ORDER BY p.org_id, p.created_at, p.id
  ) AS owner
ON CONFLICT (org_id, principal_id, capability) DO NOTHING;

-- +goose Down

-- Removing it would lock the owner out of the pages this granted, which is the
-- exact failure the migration exists to prevent, and rolling back 00026 drops
-- the table this row lives in anyway.
SELECT 1;
