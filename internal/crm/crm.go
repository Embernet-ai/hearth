// Package crm is contacts and companies, and it exists for a
// maintenance-shaped reason rather than a sales one.
//
// The useful question is not which stage a deal is at. It is who fixed the
// compressor last time, what their number is, and whether they are the same
// firm that supplied the seal. That answer belongs on the compressor, which is
// why a contact links to assets and work orders through the same entity_links
// every other relationship uses.
//
// # Why these are ordinary entities
//
// core.EntityContact and EntityCompany have been declared since the beginning
// with nothing behind them. Putting real tables there rather than inventing a
// parallel world means grants, properties, links, and search all work on the
// day they land, because every one of those is keyed by (entity_type,
// entity_id) and neither cares what a contact is.
//
// There is no pipeline and no deals table. The build order says to add one only
// if asked for, and it is right: a pipeline nobody updates is worse than no
// pipeline, because it looks like a forecast.
package crm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("that is not something this can store")
	// ErrDuplicate is separate because it is the one a person can act on: the
	// row they are trying to create already exists and they probably want it.
	ErrDuplicate = errors.New("that already exists here")
)

// Company kinds. One row per firm with a kind on it, rather than three tables:
// a company that supplies parts and also sends an engineer is both, and two
// rows for it means two phone numbers that drift apart.
const (
	KindVendor       = "vendor"
	KindContractor   = "contractor"
	KindCustomer     = "customer"
	KindManufacturer = "manufacturer"
	KindOther        = "other"
	// KindProspect is somebody who has not bought anything yet, so a sales
	// list can say so without calling them a customer.
	KindProspect = "prospect"
)

// Kinds in the order a picker offers them, commonest first.
var Kinds = []string{KindVendor, KindContractor, KindManufacturer, KindCustomer, KindProspect, KindOther}

var validKinds = map[string]bool{
	KindVendor: true, KindContractor: true, KindCustomer: true,
	KindManufacturer: true, KindOther: true, KindProspect: true,
}

// Company is a firm.
type Company struct {
	ID     string
	OrgID  string
	Name   string
	Domain string
	Kind   string
	Notes  string
	// Contacts is how many people are on file there, for the list page.
	Contacts  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (c *Company) Ref() core.Ref { return core.Ref{Type: core.EntityCompany, ID: c.ID} }

// Contact is a person.
type Contact struct {
	ID          string
	OrgID       string
	CompanyID   *string
	CompanyName string
	Name        string
	Email       string
	Phone       string
	Title       string
	Notes       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (c *Contact) Ref() core.Ref { return core.Ref{Type: core.EntityContact, ID: c.ID} }

// Authorizer is the slice of access control this package needs.
type Authorizer interface {
	Require(ctx context.Context, subjects core.Subjects, ref core.Ref, required core.AccessLevel) error
}

type Service struct {
	db    *pgxpool.Pool
	authz Authorizer
}

func New(db *pgxpool.Pool, az Authorizer) *Service { return &Service{db: db, authz: az} }

// -----------------------------------------------------------------------------
// Companies
// -----------------------------------------------------------------------------

// CreateCompany writes the row, its grant, and its creation event in one
// transaction.
//
// The grant is not a separate step, for the same reason it is not one for a
// document: a row that exists with no grant is invisible to everybody including
// the person who just made it, and it can only be found by an admin going into
// the database.
func (s *Service) CreateCompany(ctx context.Context, orgID string, actor core.Subjects,
	c Company) (*Company, error) {

	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("crm: no acting principal")
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return nil, fmt.Errorf("%w: a company needs a name", ErrInvalid)
	}
	c.Domain = normaliseDomain(c.Domain)
	if c.Kind == "" {
		c.Kind = KindVendor
	}
	if !validKinds[c.Kind] {
		return nil, fmt.Errorf("%w: %q is not a kind of company", ErrInvalid, c.Kind)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	c.ID, c.OrgID = uuid.NewString(), orgID
	err = tx.QueryRow(ctx, `
		INSERT INTO companies (id, org_id, name, domain, kind, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING created_at, updated_at`,
		c.ID, orgID, c.Name, c.Domain, c.Kind, strings.TrimSpace(c.Notes), principal).
		Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: another company already has the domain %s",
				ErrDuplicate, c.Domain)
		}
		return nil, fmt.Errorf("crm: create company: %w", err)
	}
	if err := grantAdmin(ctx, tx, orgID, core.EntityCompany, c.ID, principal); err != nil {
		return nil, err
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityCompany), EntityID: c.ID,
		Event: jobs.EventCreated, ActorID: principal,
		Payload: map[string]any{"name": c.Name, "kind": c.Kind},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("crm: commit: %w", err)
	}
	return &c, nil
}

// UpdateCompany changes the row and publishes the event that reindexes it.
func (s *Service) UpdateCompany(ctx context.Context, orgID string, actor core.Subjects,
	c Company) error {

	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityCompany, ID: c.ID},
		core.AccessEdit); err != nil {
		return err
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return fmt.Errorf("%w: a company needs a name", ErrInvalid)
	}
	c.Domain = normaliseDomain(c.Domain)
	if c.Kind != "" && !validKinds[c.Kind] {
		return fmt.Errorf("%w: %q is not a kind of company", ErrInvalid, c.Kind)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx, `
		UPDATE companies SET name=$3, domain=$4, kind=COALESCE(NULLIF($5,''), kind),
		                     notes=$6, updated_at=now()
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`,
		c.ID, orgID, c.Name, c.Domain, c.Kind, strings.TrimSpace(c.Notes))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: another company already has the domain %s",
				ErrDuplicate, c.Domain)
		}
		return fmt.Errorf("crm: update company: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityCompany), EntityID: c.ID,
		Event: jobs.EventUpdated, ActorID: actor.Principal(),
		Payload: map[string]any{"name": c.Name},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteCompany soft deletes a firm. Its contacts stay, with no company, for
// the same reason a work order outlives the schedule that raised it: they are
// a record of somebody who was on site.
func (s *Service) DeleteCompany(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityCompany, ID: id},
		core.AccessAdmin); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx, `
		UPDATE companies SET deleted_at=now(), updated_at=now()
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID)
	if err != nil {
		return fmt.Errorf("crm: delete company: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	// The unique index on the domain excludes deleted rows, so the domain is
	// free again immediately. A firm that was removed by mistake and re-added
	// must not be refused its own address.
	if _, err := tx.Exec(ctx,
		`UPDATE contacts SET company_id=NULL, updated_at=now() WHERE company_id=$1`, id); err != nil {
		return fmt.Errorf("crm: detach contacts: %w", err)
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityCompany), EntityID: id,
		Event: jobs.EventDeleted, ActorID: actor.Principal(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetCompany returns one firm if the caller may see it.
func (s *Service) GetCompany(ctx context.Context, orgID string, actor core.Subjects, id string) (*Company, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityCompany, ID: id},
		core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	c := &Company{ID: id, OrgID: orgID}
	err := s.db.QueryRow(ctx, `
		SELECT name, domain, kind, notes, created_at, updated_at,
		       (SELECT count(*) FROM contacts ct
		         WHERE ct.company_id = $1 AND ct.deleted_at IS NULL)::int
		FROM companies WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID).
		Scan(&c.Name, &c.Domain, &c.Kind, &c.Notes, &c.CreatedAt, &c.UpdatedAt, &c.Contacts)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("crm: get company: %w", err)
	}
	return c, nil
}

// Companies lists the firms the caller can see.
func (s *Service) Companies(ctx context.Context, orgID string, actor core.Subjects,
	kind, search string) ([]*Company, error) {

	args := []any{orgID, keysOf(actor)}
	where := ""
	if kind != "" && validKinds[kind] {
		args = append(args, kind)
		where += fmt.Sprintf(" AND c.kind = $%d", len(args))
	}
	if q := strings.TrimSpace(search); q != "" {
		args = append(args, "%"+q+"%")
		where += fmt.Sprintf(" AND (c.name ILIKE $%d OR c.domain ILIKE $%d)", len(args), len(args))
	}

	rows, err := s.db.Query(ctx, `
		SELECT c.id::text, c.name, c.domain, c.kind, c.notes, c.created_at, c.updated_at,
		       (SELECT count(*) FROM contacts ct
		         WHERE ct.company_id = c.id AND ct.deleted_at IS NULL)::int
		FROM companies c
		WHERE c.org_id = $1 AND c.deleted_at IS NULL`+where+`
		  AND ($2::text[] @> ARRAY['*super'] OR EXISTS (
		      SELECT 1 FROM grants g
		       WHERE g.entity_type = 'company' AND g.entity_id = c.id
		         AND g.subject_key = ANY($2)
		         AND (g.expires_at IS NULL OR g.expires_at > now())))
		ORDER BY c.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("crm: list companies: %w", err)
	}
	defer rows.Close()

	var out []*Company
	for rows.Next() {
		c := &Company{OrgID: orgID}
		if err := rows.Scan(&c.ID, &c.Name, &c.Domain, &c.Kind, &c.Notes,
			&c.CreatedAt, &c.UpdatedAt, &c.Contacts); err != nil {
			return nil, fmt.Errorf("crm: scan company: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------------
// Contacts
// -----------------------------------------------------------------------------

// CreateContact writes a person, their grant, and their creation event.
func (s *Service) CreateContact(ctx context.Context, orgID string, actor core.Subjects,
	c Contact) (*Contact, error) {

	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("crm: no acting principal")
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return nil, fmt.Errorf("%w: a contact needs a name", ErrInvalid)
	}
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	if c.Email != "" && !strings.Contains(c.Email, "@") {
		return nil, fmt.Errorf("%w: %q is not an email address", ErrInvalid, c.Email)
	}
	if c.CompanyID != nil && *c.CompanyID == "" {
		c.CompanyID = nil
	}
	if c.CompanyID != nil {
		// Edit on the company, because adding somebody to a firm's page is a
		// change to that firm.
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityCompany, ID: *c.CompanyID}, core.AccessEdit); err != nil {
			return nil, err
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	c.ID, c.OrgID = uuid.NewString(), orgID
	err = tx.QueryRow(ctx, `
		INSERT INTO contacts (id, org_id, company_id, name, email, phone, title, notes, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING created_at, updated_at`,
		c.ID, orgID, c.CompanyID, c.Name, c.Email, strings.TrimSpace(c.Phone),
		strings.TrimSpace(c.Title), strings.TrimSpace(c.Notes), principal).
		Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: somebody here already has the address %s",
				ErrDuplicate, c.Email)
		}
		return nil, fmt.Errorf("crm: create contact: %w", err)
	}
	if err := grantAdmin(ctx, tx, orgID, core.EntityContact, c.ID, principal); err != nil {
		return nil, err
	}
	// A contact inherits the company's grants, so adding a person to a firm
	// everybody can see does not produce a row only the author can find.
	if c.CompanyID != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
			SELECT gen_random_uuid(), $1, 'contact', $2, g.subject_key, g.level, $4
			FROM grants g
			WHERE g.entity_type = 'company' AND g.entity_id = $3
			ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
			orgID, c.ID, *c.CompanyID, principal); err != nil {
			return nil, fmt.Errorf("crm: inherit company grants: %w", err)
		}
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityContact), EntityID: c.ID,
		Event: jobs.EventCreated, ActorID: principal,
		Payload: map[string]any{"name": c.Name},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("crm: commit: %w", err)
	}
	return &c, nil
}

// UpdateContact changes a person.
func (s *Service) UpdateContact(ctx context.Context, orgID string, actor core.Subjects,
	c Contact) error {

	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityContact, ID: c.ID},
		core.AccessEdit); err != nil {
		return err
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return fmt.Errorf("%w: a contact needs a name", ErrInvalid)
	}
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	if c.CompanyID != nil && *c.CompanyID == "" {
		c.CompanyID = nil
	}
	if c.CompanyID != nil {
		if err := s.authz.Require(ctx, actor,
			core.Ref{Type: core.EntityCompany, ID: *c.CompanyID}, core.AccessEdit); err != nil {
			return err
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx, `
		UPDATE contacts SET company_id=$3, name=$4, email=$5, phone=$6, title=$7,
		                    notes=$8, updated_at=now()
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`,
		c.ID, orgID, c.CompanyID, c.Name, c.Email, strings.TrimSpace(c.Phone),
		strings.TrimSpace(c.Title), strings.TrimSpace(c.Notes))
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: somebody here already has the address %s",
				ErrDuplicate, c.Email)
		}
		return fmt.Errorf("crm: update contact: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityContact), EntityID: c.ID,
		Event: jobs.EventUpdated, ActorID: actor.Principal(),
		Payload: map[string]any{"name": c.Name},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteContact soft deletes a person.
func (s *Service) DeleteContact(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityContact, ID: id},
		core.AccessAdmin); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx, `
		UPDATE contacts SET deleted_at=now(), updated_at=now()
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID)
	if err != nil {
		return fmt.Errorf("crm: delete contact: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityContact), EntityID: id,
		Event: jobs.EventDeleted, ActorID: actor.Principal(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetContact returns one person if the caller may see them.
func (s *Service) GetContact(ctx context.Context, orgID string, actor core.Subjects, id string) (*Contact, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityContact, ID: id},
		core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	c := &Contact{ID: id, OrgID: orgID}
	err := s.db.QueryRow(ctx, `
		SELECT ct.company_id::text, COALESCE(co.name, ''), ct.name, ct.email, ct.phone,
		       ct.title, ct.notes, ct.created_at, ct.updated_at
		FROM contacts ct
		LEFT JOIN companies co ON co.id = ct.company_id AND co.deleted_at IS NULL
		WHERE ct.id=$1 AND ct.org_id=$2 AND ct.deleted_at IS NULL`, id, orgID).
		Scan(&c.CompanyID, &c.CompanyName, &c.Name, &c.Email, &c.Phone,
			&c.Title, &c.Notes, &c.CreatedAt, &c.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("crm: get contact: %w", err)
	}
	return c, nil
}

// Contacts lists the people the caller can see, optionally at one company.
func (s *Service) Contacts(ctx context.Context, orgID string, actor core.Subjects,
	companyID, search string) ([]*Contact, error) {

	args := []any{orgID, keysOf(actor)}
	where := ""
	if companyID != "" {
		args = append(args, companyID)
		where += fmt.Sprintf(" AND ct.company_id = $%d", len(args))
	}
	if q := strings.TrimSpace(search); q != "" {
		args = append(args, "%"+q+"%")
		where += fmt.Sprintf(" AND (ct.name ILIKE $%d OR ct.email ILIKE $%d OR ct.phone ILIKE $%d)",
			len(args), len(args), len(args))
	}

	rows, err := s.db.Query(ctx, `
		SELECT ct.id::text, ct.company_id::text, COALESCE(co.name, ''), ct.name,
		       ct.email, ct.phone, ct.title, ct.notes, ct.created_at, ct.updated_at
		FROM contacts ct
		LEFT JOIN companies co ON co.id = ct.company_id AND co.deleted_at IS NULL
		WHERE ct.org_id = $1 AND ct.deleted_at IS NULL`+where+`
		  AND ($2::text[] @> ARRAY['*super'] OR EXISTS (
		      SELECT 1 FROM grants g
		       WHERE g.entity_type = 'contact' AND g.entity_id = ct.id
		         AND g.subject_key = ANY($2)
		         AND (g.expires_at IS NULL OR g.expires_at > now())))
		ORDER BY ct.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("crm: list contacts: %w", err)
	}
	defer rows.Close()

	var out []*Contact
	for rows.Next() {
		c := &Contact{OrgID: orgID}
		if err := rows.Scan(&c.ID, &c.CompanyID, &c.CompanyName, &c.Name, &c.Email,
			&c.Phone, &c.Title, &c.Notes, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("crm: scan contact: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ByEmail finds a contact from an address, for the inbound mail path that
// arrives in the next phase. Unfiltered by grant on purpose: a message arriving
// from an address has no caller to check, and the answer is used to attribute
// the message rather than to show anybody a row.
func (s *Service) ByEmail(ctx context.Context, orgID, email string) (*Contact, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, ErrNotFound
	}
	c := &Contact{OrgID: orgID, Email: email}
	err := s.db.QueryRow(ctx, `
		SELECT ct.id::text, ct.company_id::text, COALESCE(co.name, ''), ct.name,
		       ct.phone, ct.title
		FROM contacts ct
		LEFT JOIN companies co ON co.id = ct.company_id AND co.deleted_at IS NULL
		WHERE ct.org_id=$1 AND lower(ct.email)=$2 AND ct.deleted_at IS NULL`, orgID, email).
		Scan(&c.ID, &c.CompanyID, &c.CompanyName, &c.Name, &c.Phone, &c.Title)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("crm: contact by email: %w", err)
	}
	return c, nil
}

// Party is a contact or a company as it appears attached to something else.
//
// One type for both, because a document page showing "who is involved" does
// not care which it is, and two lists side by side would be a distinction the
// reader has to make rather than one the page has made for them.
type Party struct {
	Type    core.EntityType
	ID      string
	Name    string
	Subtext string
}

// Href is where this party lives.
func (p Party) Href() string {
	if p.Type == core.EntityCompany {
		return "/companies/" + p.ID
	}
	return "/contacts/" + p.ID
}

// PartiesFor lists the contacts and companies attached to a document.
//
// The link is one row in entity_links, and until this existed it could only be
// read from one end: the vendor page showed the compressor and the compressor
// showed nothing. A relationship that is only visible from one side is a
// relationship people stop making.
func (s *Service) PartiesFor(ctx context.Context, orgID string, actor core.Subjects,
	docID string) ([]Party, error) {

	if err := s.authz.Require(ctx, actor,
		core.Ref{Type: core.EntityDocument, ID: docID}, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		WITH related AS (
		    SELECT to_type   AS t, to_id   AS id FROM entity_links
		     WHERE org_id = $1 AND from_type = 'document' AND from_id = $2
		       AND to_type IN ('contact','company')
		    UNION
		    SELECT from_type AS t, from_id AS id FROM entity_links
		     WHERE org_id = $1 AND to_type = 'document' AND to_id = $2
		       AND from_type IN ('contact','company')
		)
		SELECT 'contact'::text, ct.id::text, ct.name,
		       concat_ws(' · ', NULLIF(ct.title,''), NULLIF(co.name,''), NULLIF(ct.phone,''))
		FROM related r
		JOIN contacts ct ON ct.id = r.id AND r.t = 'contact' AND ct.deleted_at IS NULL
		LEFT JOIN companies co ON co.id = ct.company_id AND co.deleted_at IS NULL
		WHERE ct.org_id = $1
		  AND EXISTS (SELECT 1 FROM grants g
		               WHERE g.entity_type = 'contact' AND g.entity_id = ct.id
		                 AND g.subject_key = ANY($3)
		                 AND (g.expires_at IS NULL OR g.expires_at > now()))
		UNION ALL
		SELECT 'company'::text, c.id::text, c.name,
		       concat_ws(' · ', NULLIF(c.kind,''), NULLIF(c.domain,''))
		FROM related r
		JOIN companies c ON c.id = r.id AND r.t = 'company' AND c.deleted_at IS NULL
		WHERE c.org_id = $1
		  AND EXISTS (SELECT 1 FROM grants g
		               WHERE g.entity_type = 'company' AND g.entity_id = c.id
		                 AND g.subject_key = ANY($3)
		                 AND (g.expires_at IS NULL OR g.expires_at > now()))
		ORDER BY 1, 3`, orgID, docID, actor.Keys())
	if err != nil {
		return nil, fmt.Errorf("crm: parties for document: %w", err)
	}
	defer rows.Close()

	var out []Party
	for rows.Next() {
		var p Party
		var t string
		if err := rows.Scan(&t, &p.ID, &p.Name, &p.Subtext); err != nil {
			return nil, fmt.Errorf("crm: scan party: %w", err)
		}
		p.Type = core.EntityType(t)
		out = append(out, p)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------------
// Shared
// -----------------------------------------------------------------------------

// grantAdmin gives the creator admin in the same transaction as the row.
func grantAdmin(ctx context.Context, tx pgx.Tx, orgID string, t core.EntityType,
	id, principal string) error {

	_, err := tx.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'admin', $5)
		ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
		orgID, string(t), id,
		core.Subject{Kind: core.SubjectPrincipal, ID: principal}.Key(), principal)
	if err != nil {
		return fmt.Errorf("crm: grant %s: %w", t, err)
	}
	return nil
}

// normaliseDomain reduces whatever somebody pasted to a bare hostname.
//
// A person filling this in will paste a URL, an address with an at sign, or
// type it with a trailing slash. Storing all four shapes means the inbound mail
// matcher in the next phase finds none of them.
func normaliseDomain(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://")
	if _, after, found := strings.Cut(v, "@"); found {
		v = after
	}
	v = strings.TrimPrefix(v, "www.")
	if slash := strings.IndexByte(v, '/'); slash >= 0 {
		v = v[:slash]
	}
	return strings.Trim(v, ". ")
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
