package crm

import (
	"context"
	"fmt"

	"github.com/Embernet-ai/hearth/internal/core"
)

// EmberOpsLink is the page a record has in EmberOps: its Customer, Supplier, Lead, Contact or Opportunity.
type EmberOpsLink struct {
	Label string
	URL   string
}

// EmberOpsLinks lists the EmberOps pages the CRM sync recorded for a company, contact or deal. The caller has
// already been allowed to see the record, so this only reads what the sync wrote.
func (s *Service) EmberOpsLinks(ctx context.Context, orgID string, ref core.Ref) ([]EmberOpsLink, error) {
	rows, err := s.db.Query(ctx, `SELECT split_part(external_id, '/', 1), coalesce(external_url, '')
		FROM external_refs WHERE org_id=$1 AND source='emberops-record' AND entity_id=$2::uuid
		ORDER BY external_id`, orgID, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("crm: read EmberOps links: %w", err)
	}
	defer rows.Close()
	var out []EmberOpsLink
	for rows.Next() {
		var l EmberOpsLink
		if err := rows.Scan(&l.Label, &l.URL); err != nil {
			return nil, err
		}
		if l.URL != "" {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}
