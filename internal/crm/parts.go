package crm

// Parts, and the movements that make a work order's cost real rather than
// estimated.
//
// Labour alone is half the number. A repair that took two hours and a
// four-hundred-pound bearing costs four hundred pounds more than the timesheet
// says, and the asset roll-up that a replacement decision is made from is
// wrong by exactly that much until the parts are in it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Movement reasons. The sign and the reason have to agree, which the database
// enforces: a receipt entered as a consumption quietly doubles the shelf
// instead of emptying it.
const (
	ReasonConsumed = "consumed"
	ReasonReceived = "received"
	ReasonReturned = "returned"
	ReasonAdjusted = "adjusted"
)

// Part is something on a shelf.
type Part struct {
	ID            string
	OrgID         string
	SKU           string
	Name          string
	UnitCostCents int
	SupplierID    *string
	SupplierName  string
	ReorderAt     int

	// OnHand is summed from the movements rather than stored. A quantity
	// column can only be adjusted, and "why does this say four when there are
	// none on the shelf" then has no answer at all.
	OnHand float64
	// Used is how many have been consumed, ever, which is the number that says
	// whether a part is worth stocking.
	Used float64
}

// Low reports whether stock has fallen to the reorder point. A reorder point
// of zero means nobody has set one, which reads as unknown rather than as
// "never reorder".
func (p Part) Low() bool { return p.ReorderAt > 0 && p.OnHand <= float64(p.ReorderAt) }

// Movement is one time stock moved.
type Movement struct {
	ID            string
	PartID        string
	PartName      string
	SKU           string
	DocumentID    *string
	DocumentTitle string
	Quantity      float64
	UnitCostCents int
	Reason        string
	Note          string
	MovedAt       time.Time
	MovedBy       string
	ByName        string
}

// CostCents is what this movement was worth, always positive: two of a
// fifty-pound part is a hundred pounds whether it went on or came off.
func (m Movement) CostCents() int {
	q := m.Quantity
	if q < 0 {
		q = -q
	}
	return int(q * float64(m.UnitCostCents))
}

// -----------------------------------------------------------------------------
// The catalogue
// -----------------------------------------------------------------------------

// SavePart creates or updates one.
//
// Parts are org-wide rather than granted individually. A store room is a store
// room: hiding a bearing from a fitter who can see the job it goes on produces
// a work order nobody can complete, and there is nothing on a part worth
// keeping from a colleague.
func (s *Service) SavePart(ctx context.Context, orgID string, actor core.Subjects, p Part) (*Part, error) {
	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("crm: no acting principal")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, fmt.Errorf("%w: a part needs a name", ErrInvalid)
	}
	p.SKU = strings.ToUpper(strings.TrimSpace(p.SKU))
	if p.UnitCostCents < 0 {
		return nil, fmt.Errorf("%w: a part cannot cost less than nothing", ErrInvalid)
	}
	if p.ReorderAt < 0 {
		p.ReorderAt = 0
	}
	if p.SupplierID != nil && *p.SupplierID == "" {
		p.SupplierID = nil
	}

	if p.ID == "" {
		p.ID, p.OrgID = uuid.NewString(), orgID
		_, err := s.db.Exec(ctx, `
			INSERT INTO parts (id, org_id, sku, name, unit_cost_cents, supplier_id,
			                   reorder_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			p.ID, orgID, p.SKU, p.Name, p.UnitCostCents, p.SupplierID, p.ReorderAt, principal)
		if err != nil {
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("%w: another part already has the number %s",
					ErrDuplicate, p.SKU)
			}
			return nil, fmt.Errorf("crm: create part: %w", err)
		}
		return &p, nil
	}

	ct, err := s.db.Exec(ctx, `
		UPDATE parts SET sku=$3, name=$4, unit_cost_cents=$5, supplier_id=$6,
		                 reorder_at=$7, updated_at=now()
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`,
		p.ID, orgID, p.SKU, p.Name, p.UnitCostCents, p.SupplierID, p.ReorderAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: another part already has the number %s",
				ErrDuplicate, p.SKU)
		}
		return nil, fmt.Errorf("crm: update part: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return &p, nil
}

// DeletePart soft deletes one. Its movements stay, because they are what a work
// order's recorded cost is made of and a repair must not get cheaper because
// somebody tidied the catalogue.
func (s *Service) DeletePart(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if actor.Principal() == "" {
		return errors.New("crm: no acting principal")
	}
	ct, err := s.db.Exec(ctx, `
		UPDATE parts SET deleted_at=now(), updated_at=now()
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID)
	if err != nil {
		return fmt.Errorf("crm: delete part: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const partSelect = `
	SELECT p.id::text, p.sku, p.name, p.unit_cost_cents, p.supplier_id::text,
	       COALESCE(co.name, ''), p.reorder_at,
	       COALESCE(m.on_hand, 0)::float8, COALESCE(m.used, 0)::float8
	FROM parts p
	LEFT JOIN companies co ON co.id = p.supplier_id AND co.deleted_at IS NULL
	LEFT JOIN LATERAL (
	    SELECT SUM(quantity) AS on_hand,
	           -SUM(quantity) FILTER (WHERE reason = 'consumed') AS used
	    FROM part_movements WHERE part_id = p.id
	) m ON true`

// Parts lists the catalogue with stock summed from the movements.
func (s *Service) Parts(ctx context.Context, orgID, search string) ([]*Part, error) {
	args := []any{orgID}
	where := ""
	if q := strings.TrimSpace(search); q != "" {
		args = append(args, "%"+q+"%")
		where = fmt.Sprintf(" AND (p.name ILIKE $%d OR p.sku ILIKE $%d)", len(args), len(args))
	}
	rows, err := s.db.Query(ctx,
		partSelect+` WHERE p.org_id = $1 AND p.deleted_at IS NULL`+where+` ORDER BY p.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("crm: list parts: %w", err)
	}
	defer rows.Close()
	return scanParts(rows, orgID)
}

// GetPart returns one.
func (s *Service) GetPart(ctx context.Context, orgID, id string) (*Part, error) {
	rows, err := s.db.Query(ctx,
		partSelect+` WHERE p.id = $2 AND p.org_id = $1 AND p.deleted_at IS NULL`, orgID, id)
	if err != nil {
		return nil, fmt.Errorf("crm: get part: %w", err)
	}
	list, err := scanParts(rows, orgID)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

func scanParts(rows pgx.Rows, orgID string) ([]*Part, error) {
	var out []*Part
	for rows.Next() {
		p := &Part{OrgID: orgID}
		if err := rows.Scan(&p.ID, &p.SKU, &p.Name, &p.UnitCostCents, &p.SupplierID,
			&p.SupplierName, &p.ReorderAt, &p.OnHand, &p.Used); err != nil {
			return nil, fmt.Errorf("crm: scan part: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------------
// Movements
// -----------------------------------------------------------------------------

// Consume fits a part to a job.
//
// Comment access on the document, the same line time entries and checklists
// draw: recording that a bearing went on is doing the work, not deciding what
// the work is.
//
// The unit cost is copied from the catalogue at this moment and stored on the
// movement. A price rise next spring must not change what last autumn's repair
// is recorded as having cost, and a maintenance history whose numbers move
// under you is not evidence of anything.
func (s *Service) Consume(ctx context.Context, orgID string, actor core.Subjects,
	docID, partID string, quantity float64, note string) (*Movement, error) {

	if quantity <= 0 {
		return nil, fmt.Errorf("%w: fitting nothing is not a movement", ErrInvalid)
	}
	return s.move(ctx, orgID, actor, docID, partID, -quantity, ReasonConsumed, note)
}

// Receive books stock in against a purchase order.
func (s *Service) Receive(ctx context.Context, orgID string, actor core.Subjects,
	docID, partID string, quantity float64, unitCostCents int, note string) (*Movement, error) {

	if quantity <= 0 {
		return nil, fmt.Errorf("%w: receiving nothing is not a movement", ErrInvalid)
	}
	m, err := s.move(ctx, orgID, actor, docID, partID, quantity, ReasonReceived, note)
	if err != nil || unitCostCents <= 0 {
		return m, err
	}
	// A delivery is when the real price is known, so it updates the catalogue
	// as well as the movement. The movements already written keep the price
	// they were written with.
	if _, err := s.db.Exec(ctx, `
		UPDATE parts SET unit_cost_cents=$3, updated_at=now() WHERE id=$1 AND org_id=$2`,
		partID, orgID, unitCostCents); err != nil {
		return nil, fmt.Errorf("crm: update part cost: %w", err)
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE part_movements SET unit_cost_cents=$2 WHERE id=$1`, m.ID, unitCostCents); err != nil {
		return nil, fmt.Errorf("crm: record delivered cost: %w", err)
	}
	m.UnitCostCents = unitCostCents
	return m, nil
}

// Return puts an unused part back on the shelf.
func (s *Service) Return(ctx context.Context, orgID string, actor core.Subjects,
	docID, partID string, quantity float64, note string) (*Movement, error) {

	if quantity <= 0 {
		return nil, fmt.Errorf("%w: returning nothing is not a movement", ErrInvalid)
	}
	return s.move(ctx, orgID, actor, docID, partID, quantity, ReasonReturned, note)
}

// Adjust corrects the shelf after a stocktake, in either direction.
//
// This is why on_hand is derived rather than stored: a correction is a row that
// says who counted, when, and by how much, instead of a number that changed
// with no explanation attached.
func (s *Service) Adjust(ctx context.Context, orgID string, actor core.Subjects,
	partID string, delta float64, note string) (*Movement, error) {

	if delta == 0 {
		return nil, fmt.Errorf("%w: an adjustment of zero says nothing", ErrInvalid)
	}
	if strings.TrimSpace(note) == "" {
		// An unexplained correction is the thing this table exists to prevent.
		return nil, fmt.Errorf("%w: say why the count changed", ErrInvalid)
	}
	return s.move(ctx, orgID, actor, "", partID, delta, ReasonAdjusted, note)
}

func (s *Service) move(ctx context.Context, orgID string, actor core.Subjects,
	docID, partID string, quantity float64, reason, note string) (*Movement, error) {

	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("crm: no acting principal")
	}
	var doc *string
	if docID != "" {
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessComment); err != nil {
			return nil, err
		}
		doc = &docID
	}

	var cost int
	err := s.db.QueryRow(ctx,
		`SELECT unit_cost_cents FROM parts WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`,
		partID, orgID).Scan(&cost)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("crm: read part: %w", err)
	}

	m := &Movement{
		ID: uuid.NewString(), PartID: partID, DocumentID: doc, Quantity: quantity,
		UnitCostCents: cost, Reason: reason, Note: strings.TrimSpace(note),
		MovedBy: principal,
	}
	err = s.db.QueryRow(ctx, `
		INSERT INTO part_movements (id, org_id, part_id, document_id, quantity,
		                            unit_cost_cents, reason, note, moved_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING moved_at`,
		m.ID, orgID, partID, doc, quantity, cost, reason, m.Note, principal).Scan(&m.MovedAt)
	if err != nil {
		return nil, fmt.Errorf("crm: record movement: %w", err)
	}
	return m, nil
}

// RemoveMovement deletes one, for the fat-fingered quantity.
//
// Edit on the document rather than comment. Recording a part is reporting what
// happened; deleting the record changes what the repair is on file as having
// cost, and that number has to be harder to erase than it was to write.
func (s *Service) RemoveMovement(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	var docID *string
	err := s.db.QueryRow(ctx,
		`SELECT document_id::text FROM part_movements WHERE id=$1 AND org_id=$2`, id, orgID).
		Scan(&docID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("crm: find movement: %w", err)
	}
	if docID != nil {
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityDocument, ID: *docID}, core.AccessEdit); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM part_movements WHERE id=$1 AND org_id=$2`,
		id, orgID); err != nil {
		return fmt.Errorf("crm: remove movement: %w", err)
	}
	return nil
}

// MovementsFor lists the parts on one document.
func (s *Service) MovementsFor(ctx context.Context, orgID string, actor core.Subjects,
	docID string) ([]Movement, error) {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		SELECT m.id::text, m.part_id::text, p.name, p.sku, m.quantity,
		       m.unit_cost_cents, m.reason, m.note, m.moved_at, m.moved_by::text,
		       COALESCE(NULLIF(pr.display_name,''), '')
		FROM part_movements m
		JOIN parts p ON p.id = m.part_id
		JOIN principals pr ON pr.id = m.moved_by
		WHERE m.document_id = $1 AND m.org_id = $2
		ORDER BY m.moved_at DESC`, docID, orgID)
	if err != nil {
		return nil, fmt.Errorf("crm: movements for document: %w", err)
	}
	defer rows.Close()
	return scanMovements(rows)
}

// HistoryFor lists one part's movements, which is the answer to "where did they
// all go".
func (s *Service) HistoryFor(ctx context.Context, orgID, partID string, limit int) ([]Movement, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
		SELECT m.id::text, m.part_id::text, p.name, p.sku, m.quantity,
		       m.unit_cost_cents, m.reason, m.note, m.moved_at, m.moved_by::text,
		       COALESCE(NULLIF(pr.display_name,''), '')
		FROM part_movements m
		JOIN parts p ON p.id = m.part_id
		JOIN principals pr ON pr.id = m.moved_by
		WHERE m.part_id = $1 AND m.org_id = $2
		ORDER BY m.moved_at DESC LIMIT $3`, partID, orgID, limit)
	if err != nil {
		return nil, fmt.Errorf("crm: part history: %w", err)
	}
	defer rows.Close()
	return scanMovements(rows)
}

func scanMovements(rows pgx.Rows) ([]Movement, error) {
	var out []Movement
	for rows.Next() {
		var m Movement
		if err := rows.Scan(&m.ID, &m.PartID, &m.PartName, &m.SKU, &m.Quantity,
			&m.UnitCostCents, &m.Reason, &m.Note, &m.MovedAt, &m.MovedBy, &m.ByName); err != nil {
			return nil, fmt.Errorf("crm: scan movement: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PartsCost is what the parts on one document came to.
//
// Consumption counts and returns come off it, so a fitter who took three
// bearings and put one back is charged for two. Nothing else is included: a
// receipt against a purchase order is stock arriving, not a repair costing
// anything.
func (s *Service) PartsCost(ctx context.Context, orgID, docID string) (int, error) {
	var cents int
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(-quantity * unit_cost_cents), 0)::int
		FROM part_movements
		WHERE document_id = $1 AND org_id = $2 AND reason IN ('consumed','returned')`,
		docID, orgID).Scan(&cents)
	if err != nil {
		return 0, fmt.Errorf("crm: parts cost: %w", err)
	}
	if cents < 0 {
		// More returned than taken, which is a correction rather than a
		// negative repair. Reporting a job that earned money is worse than
		// reporting one that cost nothing.
		return 0, nil
	}
	return cents, nil
}

// PartsCostForAsset rolls the parts up through the same links the time roll-up
// uses, so an asset's total cost is labour and materials rather than labour
// alone.
func (s *Service) PartsCostForAsset(ctx context.Context, orgID, assetID string) (int, error) {
	var cents int
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(-m.quantity * m.unit_cost_cents), 0)::int
		FROM part_movements m
		JOIN entity_links l
		  ON l.from_type = 'document' AND l.from_id = m.document_id
		 AND l.to_type = 'document' AND l.to_id = $2 AND l.kind = 'concerns'
		JOIN documents d ON d.id = m.document_id AND d.deleted_at IS NULL
		WHERE m.org_id = $1 AND m.reason IN ('consumed','returned')`, orgID, assetID).Scan(&cents)
	if err != nil {
		return 0, fmt.Errorf("crm: parts cost for asset: %w", err)
	}
	if cents < 0 {
		return 0, nil
	}
	return cents, nil
}
