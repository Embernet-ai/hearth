package crm

// What a company is, and the tags beside it.
//
// A company can be several things at once (a client that is also a vendor, a
// partner that was a prospect), so this is a list of types and not one word. The
// list belongs to the organisation and an admin edits it. Free tags sit beside it
// for everything that is not worth a type, and filter the same way.
//
// Why not the tags Hearth already has: those are the organisation's taxonomy for
// sorting a logged hour, tied to time entries, and a company is not an hour. Why
// not property definitions: they are built for documents and have no company
// page. So this is two small tables next to the company.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// CompanyType is one entry in the organisation's list.
type CompanyType struct {
	ID    string
	Key   string
	Label string
	// ERPRole is how it lines up with EmberOps: customer, supplier, or empty.
	ERPRole  string
	Position int
	Archived bool
}

// Labels is what a company has been called: its types and its tags.
type Labels struct {
	Types []CompanyType
	Tags  []string
}

// Keys are the type keys, in list order.
func (l Labels) Keys() []string {
	out := make([]string, 0, len(l.Types))
	for _, t := range l.Types {
		out = append(out, t.Key)
	}
	return out
}

var typeKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// KeyFor makes a type key from a label: lower case, letters and digits, an
// underscore between words.
func KeyFor(label string) string {
	var b strings.Builder
	under := false
	for _, r := range strings.ToLower(strings.TrimSpace(label)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			under = false
		default:
			if b.Len() > 0 && !under {
				b.WriteByte('_')
				under = true
			}
		}
	}
	k := strings.Trim(b.String(), "_")
	if k != "" && k[0] >= '0' && k[0] <= '9' {
		k = "t_" + k
	}
	if len(k) > 32 {
		k = k[:32]
	}
	return k
}

// seedTypes is the list a new organisation starts with. A migration seeds the
// organisations that exist; this covers the ones made afterwards, on first use.
var seedTypes = []CompanyType{
	{Key: "client", Label: "Client", ERPRole: "customer"},
	{Key: "prospect", Label: "Prospect"},
	{Key: "vendor", Label: "Vendor", ERPRole: "supplier"},
	{Key: "partner", Label: "Partner"},
	{Key: "contractor", Label: "Contractor", ERPRole: "supplier"},
	{Key: "manufacturer", Label: "Manufacturer", ERPRole: "supplier"},
}

// Types lists the organisation's company types, archived ones last. An
// organisation with none yet gets the starting list.
func (s *Service) Types(ctx context.Context, orgID string, includeArchived bool) ([]CompanyType, error) {
	list, err := s.types(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		for i, t := range seedTypes {
			if _, err := s.db.Exec(ctx, `INSERT INTO company_types (id, org_id, key, label, erp_role, position)
				VALUES (gen_random_uuid(), $1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
				orgID, t.Key, t.Label, t.ERPRole, i+1); err != nil {
				return nil, fmt.Errorf("crm: seed types: %w", err)
			}
		}
		if list, err = s.types(ctx, orgID); err != nil {
			return nil, err
		}
	}
	if includeArchived {
		return list, nil
	}
	out := list[:0:0]
	for _, t := range list {
		if !t.Archived {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *Service) types(ctx context.Context, orgID string) ([]CompanyType, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, key, label, erp_role, position, archived
		FROM company_types WHERE org_id=$1 ORDER BY archived, position, label`, orgID)
	if err != nil {
		return nil, fmt.Errorf("crm: list types: %w", err)
	}
	defer rows.Close()
	var out []CompanyType
	for rows.Next() {
		var t CompanyType
		if err := rows.Scan(&t.ID, &t.Key, &t.Label, &t.ERPRole, &t.Position, &t.Archived); err != nil {
			return nil, fmt.Errorf("crm: scan type: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveType adds a type, or changes the label, accounting counterpart, order, or
// archived state of one. The key never changes once made: it is what the API and
// the sync name the type by. Who may do this is the caller's decision (the page
// asks for an admin); this package only keeps the list well formed.
func (s *Service) SaveType(ctx context.Context, orgID string, t CompanyType) (*CompanyType, error) {
	t.Label = strings.TrimSpace(t.Label)
	if t.Label == "" {
		return nil, fmt.Errorf("%w: a type needs a name", ErrInvalid)
	}
	if t.ERPRole != "" && t.ERPRole != "customer" && t.ERPRole != "supplier" {
		return nil, fmt.Errorf("%w: %q is not an accounting role", ErrInvalid, t.ERPRole)
	}
	if t.ID == "" {
		if t.Key == "" {
			t.Key = KeyFor(t.Label)
		}
		if !typeKeyRe.MatchString(t.Key) {
			return nil, fmt.Errorf("%w: %q cannot make a type key", ErrInvalid, t.Label)
		}
		err := s.db.QueryRow(ctx, `INSERT INTO company_types (id, org_id, key, label, erp_role, position)
			VALUES (gen_random_uuid(), $1,$2,$3,$4,
			        coalesce((SELECT max(position)+1 FROM company_types WHERE org_id=$1), 1))
			RETURNING id::text, position`, orgID, t.Key, t.Label, t.ERPRole).Scan(&t.ID, &t.Position)
		if err != nil {
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("%w: there is already a type %q", ErrDuplicate, t.Key)
			}
			return nil, fmt.Errorf("crm: add type: %w", err)
		}
		return &t, nil
	}
	ct, err := s.db.Exec(ctx, `UPDATE company_types SET label=$3, erp_role=$4, position=$5, archived=$6
		WHERE id=$1 AND org_id=$2`, t.ID, orgID, t.Label, t.ERPRole, t.Position, t.Archived)
	if err != nil {
		return nil, fmt.Errorf("crm: change type: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return &t, nil
}

// normaliseTags trims, drops blanks and repeats (case-insensitively), and sorts.
func normaliseTags(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" {
			continue
		}
		if len(t) > 48 {
			return nil, fmt.Errorf("%w: a tag is at most 48 characters", ErrInvalid)
		}
		if k := strings.ToLower(t); !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out, nil
}

// SetCompanyLabels replaces a company's types (by key) and tags. Either may be
// nil to leave that half alone. A key that is not on the list is refused, an
// archived type may be kept on a company that has it but not added.
func (s *Service) SetCompanyLabels(ctx context.Context, orgID string, actor core.Subjects,
	companyID string, typeKeys *[]string, tags *[]string) error {

	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityCompany, ID: companyID},
		core.AccessEdit); err != nil {
		return err
	}
	all, err := s.Types(ctx, orgID, true)
	if err != nil {
		return err
	}
	byKey := map[string]CompanyType{}
	for _, t := range all {
		byKey[t.Key] = t
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM companies WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL)`,
		companyID, orgID).Scan(&exists); err != nil || !exists {
		return ErrNotFound
	}

	if typeKeys != nil {
		have := map[string]bool{}
		rows, err := tx.Query(ctx, `SELECT t.key FROM company_type_links l JOIN company_types t ON t.id=l.type_id
			WHERE l.company_id=$1`, companyID)
		if err != nil {
			return fmt.Errorf("crm: read types: %w", err)
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return err
			}
			have[k] = true
		}
		rows.Close()
		want := map[string]bool{}
		for _, k := range *typeKeys {
			k = strings.TrimSpace(k)
			t, ok := byKey[k]
			switch {
			case !ok:
				return fmt.Errorf("%w: %q is not a company type here", ErrInvalid, k)
			case t.Archived && !have[k]:
				return fmt.Errorf("%w: the %q type is archived", ErrInvalid, t.Label)
			}
			want[k] = true
		}
		if _, err := tx.Exec(ctx, `DELETE FROM company_type_links WHERE company_id=$1`, companyID); err != nil {
			return fmt.Errorf("crm: clear types: %w", err)
		}
		for k := range want {
			if _, err := tx.Exec(ctx, `INSERT INTO company_type_links (company_id, type_id) VALUES ($1,$2)`,
				companyID, byKey[k].ID); err != nil {
				return fmt.Errorf("crm: set type: %w", err)
			}
		}
	}
	if tags != nil {
		clean, err := normaliseTags(*tags)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM company_tags WHERE company_id=$1`, companyID); err != nil {
			return fmt.Errorf("crm: clear tags: %w", err)
		}
		for _, t := range clean {
			if _, err := tx.Exec(ctx, `INSERT INTO company_tags (company_id, tag) VALUES ($1,$2)`, companyID, t); err != nil {
				return fmt.Errorf("crm: set tag: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE companies SET updated_at=now() WHERE id=$1`, companyID); err != nil {
		return fmt.Errorf("crm: touch company: %w", err)
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityCompany), EntityID: companyID,
		Event: jobs.EventUpdated, ActorID: actor.Principal(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CompanyLabels reads one company's types and tags.
func (s *Service) CompanyLabels(ctx context.Context, orgID string, actor core.Subjects, companyID string) (*Labels, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityCompany, ID: companyID},
		core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	l := &Labels{}
	rows, err := s.db.Query(ctx, `SELECT t.id::text, t.key, t.label, t.erp_role, t.position, t.archived
		FROM company_type_links k JOIN company_types t ON t.id=k.type_id
		WHERE k.company_id=$1 AND t.org_id=$2 ORDER BY t.position, t.label`, companyID, orgID)
	if err != nil {
		return nil, fmt.Errorf("crm: company types: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t CompanyType
		if err := rows.Scan(&t.ID, &t.Key, &t.Label, &t.ERPRole, &t.Position, &t.Archived); err != nil {
			return nil, err
		}
		l.Types = append(l.Types, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	trows, err := s.db.Query(ctx, `SELECT tag FROM company_tags WHERE company_id=$1 ORDER BY lower(tag)`, companyID)
	if err != nil {
		return nil, fmt.Errorf("crm: company tags: %w", err)
	}
	defer trows.Close()
	for trows.Next() {
		var tag string
		if err := trows.Scan(&tag); err != nil {
			return nil, err
		}
		l.Tags = append(l.Tags, tag)
	}
	return l, trows.Err()
}

// TagsInUse is the free tags on companies the caller can see, most used first,
// for a picker and a filter.
func (s *Service) TagsInUse(ctx context.Context, orgID string, actor core.Subjects) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT min(g.tag), count(*) FROM company_tags g
		JOIN companies c ON c.id=g.company_id
		WHERE c.org_id=$1 AND c.deleted_at IS NULL AND `+visible("company", "c", 2)+`
		GROUP BY lower(g.tag) ORDER BY 2 DESC, 1 LIMIT 200`, orgID, actor.Keys())
	if err != nil {
		return nil, fmt.Errorf("crm: tags in use: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
