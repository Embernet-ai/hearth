package connect

// Contacts: an address book and the CRM, kept the same in both directions.
//
// Imported and exported as the person who connected, so what comes in is theirs in the CRM and the CRM's own
// sharing decides who else sees it. Each linked contact has two facts recorded at the last run, the provider's change
// marker and a hash of the Hearth record, and the next run reads who changed what since:
//
//	new at the provider                      a new CRM contact (or a link to the one with that address)
//	new in Hearth, made by this person       a new contact at the provider
//	changed at the provider only             Hearth takes the change
//	changed in Hearth only                   the provider takes the change
//	changed on both sides                    the provider wins, and the log says so
//	deleted at the provider                  deleted in Hearth (a soft delete, recoverable in the CRM)
//	deleted in Hearth                        deleted at the provider

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

// SyncContacts runs one person's address book both ways and returns how many records it touched on either side.
func (s *Service) SyncContacts(ctx context.Context, orgID, principalID string, p Provider) (int, error) {
	sess, err := s.session(ctx, orgID, principalID, p, Contacts)
	if err != nil {
		return 0, err
	}
	api := sess.Contacts()
	remote, err := api.Contacts(ctx)
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
			if id != "" && created {
				touched++
			}
			continue
		}
		cur, err := s.crm.GetContact(ctx, orgID, actor, l.entityID)
		if err != nil {
			if errors.Is(err, crm.ErrNotFound) {
				// Deleted in Hearth: take it off the provider too.
				if err := api.Delete(ctx, r.ID); err != nil && !errors.Is(err, ErrNotFound) {
					return touched, err
				}
				s.dropContactLink(ctx, principalID, p, r.ID)
				touched++
				continue
			}
			return touched, err
		}
		remoteMoved := r.Version != l.version
		localMoved := contactHash(*cur) != l.hash
		switch {
		case remoteMoved:
			if localMoved {
				s.log.Info("connect: contact clash, the provider's version wins", "provider", p, "contact", l.entityID)
			}
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
		case localMoved:
			out, err := api.Update(ctx, toRemoteContact(*cur, r.ID, r.Version))
			if err != nil {
				return touched, err
			}
			if err := s.setContactLink(ctx, orgID, principalID, p, r.ID, l.entityID, out.Version, contactHash(*cur)); err != nil {
				return touched, err
			}
			touched++
		}
	}

	// Linked here, gone there: deleted at the provider, so deleted here.
	for ext, l := range links {
		if seen[ext] {
			continue
		}
		if err := s.crm.DeleteContact(ctx, orgID, actor, l.entityID); err != nil && !errors.Is(err, crm.ErrNotFound) {
			return touched, err
		}
		s.dropContactLink(ctx, principalID, p, ext)
		touched++
	}

	// New in Hearth, made by this person: send it across.
	mine, err := s.unlinkedContacts(ctx, orgID, principalID)
	if err != nil {
		return touched, err
	}
	for _, id := range mine {
		c, err := s.crm.GetContact(ctx, orgID, actor, id)
		if err != nil {
			continue
		}
		out, err := api.Create(ctx, toRemoteContact(*c, "", ""))
		if err != nil {
			return touched, err
		}
		if err := s.setContactLink(ctx, orgID, principalID, p, out.ID, id, out.Version, contactHash(*c)); err != nil {
			return touched, err
		}
		touched++
	}
	return touched, nil
}

func toRemoteContact(c crm.Contact, id, version string) Contact {
	return Contact{ID: id, Version: version, Name: c.Name, Email: c.Email, Phone: c.Phone, Title: c.Title, Company: c.CompanyName}
}

// unlinkedContacts finds contacts this person made that no address book has yet.
func (s *Service) unlinkedContacts(ctx context.Context, orgID, principalID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.id::text FROM contacts c
		 WHERE c.org_id=$1 AND c.created_by=$2 AND c.deleted_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM connector_links l WHERE l.entity_type='contact' AND l.entity_id=c.id)
		 ORDER BY c.created_at LIMIT 500`, orgID, principalID)
	if err != nil {
		return nil, fmt.Errorf("connect: find new contacts: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) dropContactLink(ctx context.Context, principalID string, p Provider, ext string) {
	_, _ = s.db.Exec(ctx, `DELETE FROM connector_links WHERE principal_id=$1 AND provider=$2 AND connector='contacts' AND external_id=$3`,
		principalID, string(p), ext)
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
