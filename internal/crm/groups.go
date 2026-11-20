package crm

import (
	"context"
	"fmt"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Group is one way to put a list's rows under headings.
type Group struct {
	Key   string
	Label string
	// order sorts the rows so each heading's rows sit together, and name is the heading each row falls under.
	order, name string
}

var companyGroups = []Group{
	{Key: "kind", Label: "Kind", order: "c.kind", name: "coalesce(nullif(c.kind,''),'No kind')"},
	{Key: "owner", Label: "Owner", order: "lower(coalesce(op.display_name,''))", name: "coalesce(nullif(op.display_name,''),'Nobody')"},
}

var contactGroups = []Group{
	{Key: "company", Label: "Company", order: "lower(coalesce(co.name,''))", name: "coalesce(nullif(co.name,''),'No company')"},
	{Key: "owner", Label: "Owner", order: "lower(coalesce(op.display_name,''))", name: "coalesce(nullif(op.display_name,''),'Nobody')"},
}

var dealGroups = []Group{
	{Key: "stage", Label: "Stage", order: "array_position(ARRAY['new','screening','meeting','proposal','customer','won','lost','no_response'], d.stage)", name: "d.stage"},
	{Key: "owner", Label: "Owner", order: "lower(coalesce(op.display_name,''))", name: "coalesce(nullif(op.display_name,''),'Nobody')"},
	{Key: "company", Label: "Company", order: "lower(coalesce(co.name,''))", name: "coalesce(nullif(co.name,''),'No company')"},
}

func groupsFor(list string) []Group {
	switch list {
	case "companies":
		return companyGroups
	case "contacts":
		return contactGroups
	case "deals":
		return dealGroups
	}
	return nil
}

// GroupOptions are the headings a list can be grouped under.
func GroupOptions(list string) []Group { return groupsFor(list) }

// groupOrder is the leading ORDER BY term for a chosen grouping, or "" when there is none or it is not one of the list's.
func groupOrder(groups []Group, key string) string {
	for _, g := range groups {
		if g.Key == key {
			return g.order
		}
	}
	return ""
}

// CompanyGroup is the heading a company falls under.
func CompanyGroup(r *CompanyRow, key string) string {
	switch key {
	case "kind":
		return orElse(r.Kind, "No kind")
	case "owner":
		return orElse(r.OwnerName, "Nobody")
	}
	return ""
}

// ContactGroup is the heading a contact falls under.
func ContactGroup(r *ContactRow, key string) string {
	switch key {
	case "company":
		return orElse(r.CompanyName, "No company")
	case "owner":
		return orElse(r.OwnerName, "Nobody")
	}
	return ""
}

// DealGroup is the heading a deal falls under.
func DealGroup(d *Deal, key string) string {
	switch key {
	case "stage":
		return StageLabel(d.Stage)
	case "owner":
		return orElse(d.OwnerName, "Nobody")
	case "company":
		return orElse(d.CompanyName, "No company")
	}
	return ""
}

func orElse(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// GroupCounts is how many rows each heading has across the whole filtered list, not just the page on screen. It asks the
// same question as the list (the same filter), so a heading's number is the number of rows that heading holds.
func (s *Service) GroupCounts(ctx context.Context, orgID string, actor core.Subjects, list string, q ListQuery) (map[string]int, error) {
	var name string
	for _, g := range groupsFor(list) {
		if g.Key == q.Group {
			name = g.name
		}
	}
	if name == "" {
		return nil, nil
	}
	var from, where string
	var args []any
	switch list {
	case "companies":
		where, args = companyFilter(orgID, actor, q)
		from = `FROM companies c LEFT JOIN principals op ON op.id = c.owner_id
			WHERE c.org_id = $1 AND c.deleted_at IS NULL`
	case "contacts":
		where, args = contactFilter(orgID, actor, q)
		from = `FROM contacts ct
			LEFT JOIN companies co ON co.id = ct.company_id AND co.deleted_at IS NULL
			LEFT JOIN principals op ON op.id = ct.owner_id
			WHERE ct.org_id = $1 AND ct.deleted_at IS NULL`
	default:
		where, args = dealFilter(orgID, actor, q)
		from = `FROM deals d
			LEFT JOIN companies co ON co.id = d.company_id AND co.deleted_at IS NULL
			LEFT JOIN contacts ct ON ct.id = d.contact_id AND ct.deleted_at IS NULL
			LEFT JOIN principals op ON op.id = d.owner_id
			WHERE d.org_id = $1 AND d.deleted_at IS NULL`
	}
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT %s, count(*)::int `+from+where+` GROUP BY 1`, name), args...)
	if err != nil {
		return nil, fmt.Errorf("crm: group %s: %w", list, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var label string
		var n int
		if err := rows.Scan(&label, &n); err != nil {
			return nil, fmt.Errorf("crm: scan group: %w", err)
		}
		if list == "deals" && q.Group == "stage" {
			label = StageLabel(label)
		}
		out[label] = n
	}
	return out, rows.Err()
}
