package connect

// Contacts: an address book imported into the CRM, and kept current.
//
// Imported as the person who connected, so the contacts are theirs: the CRM's own access rules decide who else
// sees them, and a person's private address book does not become the whole company's by being connected. The sync
// only ever adds and updates. A contact deleted at the provider is left in Hearth, because a CRM record can carry
// deals and history that nothing in the provider knows about.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
)

func contactHash(c crm.Contact) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", c.Name, strings.ToLower(c.Email), c.Phone, c.Title)
	return hex.EncodeToString(h.Sum(nil))
}

// SyncContacts imports and updates one person's contacts and returns how many Hearth records it touched.
func (s *Service) SyncContacts(ctx context.Context, orgID, principalID string, p Provider) (int, error) {
	sess, err := s.session(ctx, orgID, principalID, p, Contacts)
	if err != nil {
		return 0, err
	}
	remote, err := sess.Contacts().Contacts(ctx)
	if err != nil {
		return 0, err
	}
	actor, err := s.actor(ctx, orgID, principalID)
	if err != nil {
		return 0, err
	}
	links := map[string]calLink{}
	rows, err := s.db.Query(ctx, `
		SELECT external_id, entity_id::text, remote_version, local_hash FROM connector_links
		 WHERE principal_id=$1 AND provider=$2 AND connector='contacts'`, principalID, string(p))
	if err != nil {
		return 0, fmt.Errorf("connect: read links: %w", err)
	}
	for rows.Next() {
		var ext string
		var l calLink
		if err := rows.Scan(&ext, &l.entityID, &l.version, &l.hash); err != nil {
			rows.Close()
			return 0, err
		}
		links[ext] = l
	}
	rows.Close()

	touched := 0
	seen := map[string]bool{}
	for _, r := range remote {
		seen[r.ID] = true
		name := strings.TrimSpace(r.Name)
		email := strings.ToLower(strings.TrimSpace(r.Email))
		if name == "" {
			name = email
		}
		if name == "" {
			continue // nothing to name it by
		}
		want := crm.Contact{Name: name, Email: email, Phone: strings.TrimSpace(r.Phone), Title: strings.TrimSpace(r.Title)}
		l, linked := links[r.ID]

		if !linked {
			id, created, err := s.adoptOrCreate(ctx, orgID, principalID, p, r, want)
			if err != nil {
				return touched, err
			}
			if id == "" {
				continue
			}
			if created {
				touched++
			}
			continue
		}
		if l.version == r.Version {
			continue
		}
		cur, err := s.crm.GetContact(ctx, orgID, actor, l.entityID)
		if err != nil {
			if errors.Is(err, crm.ErrNotFound) {
				// Deleted in Hearth on purpose: it stays deleted, and the link stays so it is not made again.
				_, _ = s.db.Exec(ctx, `UPDATE connector_links SET remote_version=$4 WHERE principal_id=$1 AND provider=$2
					AND connector='contacts' AND external_id=$3`, principalID, string(p), r.ID, r.Version)
				continue
			}
			return touched, err
		}
		if contactHash(*cur) == l.hash {
			// Nobody changed it in Hearth since the last run, so the provider's version is the newer one.
			upd := *cur
			upd.Name, upd.Phone, upd.Title = want.Name, want.Phone, want.Title
			if want.Email != "" {
				upd.Email = want.Email
			}
			if err := s.crm.UpdateContact(ctx, orgID, actor, upd); err != nil {
				if errors.Is(err, crm.ErrDuplicate) {
					upd.Email = cur.Email
					err = s.crm.UpdateContact(ctx, orgID, actor, upd)
				}
				if err != nil {
					return touched, err
				}
			}
			after, err := s.crm.GetContact(ctx, orgID, actor, l.entityID)
			if err != nil {
				return touched, err
			}
			if err := s.setContactLink(ctx, orgID, principalID, p, r.ID, l.entityID, r.Version, contactHash(*after)); err != nil {
				return touched, err
			}
			touched++
			continue
		}
		// Changed in Hearth since: Hearth's edit is kept, and only the provider's marker moves on.
		if err := s.setContactLink(ctx, orgID, principalID, p, r.ID, l.entityID, r.Version, l.hash); err != nil {
			return touched, err
		}
	}
	// Gone at the provider: forget the link, keep the record.
	for ext := range links {
		if !seen[ext] {
			_, _ = s.db.Exec(ctx, `DELETE FROM connector_links WHERE principal_id=$1 AND provider=$2
				AND connector='contacts' AND external_id=$3`, principalID, string(p), ext)
		}
	}
	return touched, nil
}

// adoptOrCreate links a remote contact to the Hearth person with the same address when there is one, and makes a
// new record when there is not. Adopting changes nothing in Hearth: the existing record is the better source.
func (s *Service) adoptOrCreate(ctx context.Context, orgID, principalID string, p Provider, r Contact, want crm.Contact) (string, bool, error) {
	actor, err := s.actor(ctx, orgID, principalID)
	if err != nil {
		return "", false, err
	}
	if want.Email != "" {
		if ex, err := s.crm.ByEmail(ctx, orgID, want.Email); err == nil {
			full, gerr := s.crm.GetContact(ctx, orgID, actor, ex.ID)
			if gerr == nil {
				return ex.ID, false, s.setContactLink(ctx, orgID, principalID, p, r.ID, ex.ID, r.Version, contactHash(*full))
			}
			if !errors.Is(gerr, crm.ErrNotFound) {
				return "", false, gerr
			}
			// Somebody else's contact this person cannot see: do not link to it or leak that it exists.
			return "", false, nil
		} else if !errors.Is(err, crm.ErrNotFound) {
			return "", false, err
		}
	}
	if r.Company != "" {
		if id := s.companyNamed(ctx, orgID, actor, r.Company); id != "" {
			want.CompanyID = &id
		}
	}
	created, err := s.crm.CreateContact(ctx, orgID, actor, want)
	if err != nil {
		if errors.Is(err, crm.ErrDuplicate) || errors.Is(err, crm.ErrInvalid) {
			return "", false, nil
		}
		return "", false, err
	}
	full, err := s.crm.GetContact(ctx, orgID, actor, created.ID)
	if err != nil {
		return "", false, err
	}
	return created.ID, true, s.setContactLink(ctx, orgID, principalID, p, r.ID, created.ID, r.Version, contactHash(*full))
}

// companyNamed finds a company the person can see whose name is exactly the one the provider gave. A company is
// never made from an address book entry: the name is free text there and a CRM full of near-duplicates is worse
// than a contact with no company.
func (s *Service) companyNamed(ctx context.Context, orgID string, actor core.Subjects, name string) string {
	cos, err := s.crm.Companies(ctx, orgID, actor, "", name)
	if err != nil {
		return ""
	}
	for _, c := range cos {
		if strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(name)) {
			return c.ID
		}
	}
	return ""
}

func (s *Service) setContactLink(ctx context.Context, orgID, principalID string, p Provider, ext, entityID, version, hash string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO connector_links (org_id, principal_id, provider, connector, external_id, entity_type, entity_id,
		                             remote_version, local_hash)
		VALUES ($1,$2,$3,'contacts',$4,'contact',$5,$6,$7)
		ON CONFLICT (principal_id, provider, connector, external_id) DO UPDATE
		   SET entity_id=EXCLUDED.entity_id, remote_version=EXCLUDED.remote_version,
		       local_hash=EXCLUDED.local_hash, synced_at=now()`,
		orgID, principalID, string(p), ext, entityID, version, hash)
	if err != nil {
		return fmt.Errorf("connect: save contact link: %w", err)
	}
	return nil
}
