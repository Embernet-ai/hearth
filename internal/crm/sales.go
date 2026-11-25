package crm

// The sales side: owners, a pipeline of deals, and a timeline of notes and
// calls.
//
// Everything here sits beside the contact and company code rather than inside
// it. Store, the interface the rest of Hearth holds, is untouched, so a
// deployment that keeps its data in EmberOps keeps working exactly as before
// and simply has no sales pages. The sales pages need Postgres, because a deal
// is a row that points at a company row.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// Deal stages, in the order a deal moves through them. The labels are the
// vocabulary the sales team already uses.
const (
	StageNew        = "new"
	StageScreening  = "screening"
	StageMeeting    = "meeting"
	StageProposal   = "proposal"
	StageCustomer   = "customer"
	StageWon        = "won"
	StageLost       = "lost"
	StageNoResponse = "no_response"
)

// Stages in board order.
var Stages = []string{StageNew, StageScreening, StageMeeting, StageProposal,
	StageCustomer, StageWon, StageLost, StageNoResponse}

var stageLabels = map[string]string{
	StageNew: "New", StageScreening: "Screening", StageMeeting: "Meeting",
	StageProposal: "Proposal", StageCustomer: "Customer", StageWon: "Won",
	StageLost: "Lost", StageNoResponse: "No response",
}

// StageLabel is the stage as a person reads it.
func StageLabel(stage string) string {
	if l, ok := stageLabels[stage]; ok {
		return l
	}
	return stage
}

// ValidStage reports whether s is one of the stages.
func ValidStage(s string) bool { _, ok := stageLabels[s]; return ok }

// Activity kinds and call directions.
const (
	ActNote    = "note"
	ActCall    = "call"
	ActMeeting = "meeting"
	ActEmail   = "email"
	Inbound    = "inbound"
	Outbound   = "outbound"
)

// Deal is one opportunity.
type Deal struct {
	ID          string
	OrgID       string
	Name        string
	Stage       string
	AmountCents int64
	Currency    string
	CloseDate   *time.Time
	CompanyID   *string
	CompanyName string
	ContactID   *string
	ContactName string
	OwnerID     *string
	OwnerName   string
	Position    float64
	Notes       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// LostReason is why a deal was lost, in the seller's words. StageChangedAt is when it entered its stage, set by
	// the database for every writer, and ClosedAt when it was won, lost or given up on.
	LostReason     string
	StageChangedAt time.Time
	ClosedAt       *time.Time
}

func (d *Deal) Ref() core.Ref { return core.Ref{Type: core.EntityDeal, ID: d.ID} }

// Amount is the figure as a person writes it, in whole currency units.
func (d *Deal) Amount() string { return Money(d.AmountCents, d.Currency) }

// Money formats cents.
func Money(cents int64, currency string) string {
	if currency == "" || currency == "USD" {
		currency = "$"
	} else {
		currency += " "
	}
	whole := cents / 100
	s := fmt.Sprintf("%d", whole)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if cents%100 != 0 {
		return fmt.Sprintf("%s%s.%02d", currency, s, cents%100)
	}
	return currency + s
}

// Activity is a note or a call on the timeline.
type Activity struct {
	ID          string
	Kind        string
	Title       string
	Body        string
	OccurredAt  time.Time
	Direction   string
	Disposition string
	DurationMs  *int64
	CreatedBy   string
	ByName      string
	CreatedAt   time.Time
	Targets     []core.Ref
}

// Details is what a company or contact carries for the sales side.
type Details struct {
	OwnerID      string
	OwnerName    string
	Website      string
	LinkedIn     string
	TaxID        string
	Street       string
	City         string
	State        string
	Postcode     string
	Country      string
	RevenueCents *int64
	Currency     string
	ExtraEmails  []string
	ExtraPhones  []string
	// MarketingOptOutAt is when this person stopped being emailed (a contact only). Once set it is never cleared here: an
	// unsubscribe is the person's, and Hearth does not undo it. OptOut asks for it to be set on a save.
	MarketingOptOutAt     *time.Time
	MarketingOptOutReason string
	OptOut                bool
}

// ListQuery filters, sorts and pages a list.
type ListQuery struct {
	Search string
	// PhoneE164 keeps the contacts holding this number, already normalized with PhoneE164 (a contacts list only).
	PhoneE164 string
	Kind      string
	Stage     string
	Owner     string // a principal id, or "none" for unowned
	CompanyID string
	ContactID string
	// Type is a company type key, or "none" for companies with no type. Tag
	// is a free tag.
	Type string
	Tag  string
	// Open keeps the deals still in the pipeline, Overdue the open ones past
	// their expected close date, and ClosedFrom and ClosedTo (both days
	// included) the ones that were won or lost inside that window. The
	// dashboard's numbers link here with these, so the list is the count.
	Open       bool
	Overdue    bool
	ClosedFrom *time.Time
	ClosedTo   *time.Time
	// PropKey and PropValue keep the records whose custom field PropKey holds PropValue: a match anywhere in a text field, an exact
	// one (case-blind) in any other, and "*" for any value at all.
	PropKey   string
	PropValue string
	// CloseFrom and CloseTo (both days included) keep the deals expected to close in that window, and NoCloseDate the ones with no
	// expected date at all.
	CloseFrom   *time.Time
	CloseTo     *time.Time
	NoCloseDate bool
	Sort        string
	Desc        bool
	// Group puts the rows of a list under headings: one of GroupOptions for that list.
	Group  string
	Limit  int
	Offset int
}

func (q *ListQuery) page() (int, int) {
	limit := q.Limit
	if limit <= 0 || limit > 5000 {
		limit = 50
	}
	off := q.Offset
	if off < 0 {
		off = 0
	}
	return limit, off
}

// CompanyRow is a company as the list shows it.
type CompanyRow struct {
	Company
	OwnerID      string
	OwnerName    string
	Website      string
	City         string
	State        string
	RevenueCents *int64
	Currency     string
	Deals        int
	TypeKeys     []string
	TypeLabels   []string
	Tags         []string
}

// Revenue is the annual revenue as a person reads it, or nothing when unknown.
func (c *CompanyRow) Revenue() string {
	if c.RevenueCents == nil {
		return ""
	}
	return Money(*c.RevenueCents, c.Currency)
}

// ContactRow is a contact as the list shows it.
type ContactRow struct {
	Contact
	OwnerID   string
	OwnerName string
	LinkedIn  string
}

// Sales is the Postgres-only surface of this package. *Service implements it.
type Sales interface {
	CompaniesPage(ctx context.Context, orgID string, actor core.Subjects, q ListQuery) ([]*CompanyRow, int, error)
	ContactsPage(ctx context.Context, orgID string, actor core.Subjects, q ListQuery) ([]*ContactRow, int, error)
	Types(ctx context.Context, orgID string, includeArchived bool) ([]CompanyType, error)
	SaveType(ctx context.Context, orgID string, t CompanyType) (*CompanyType, error)
	SetCompanyLabels(ctx context.Context, orgID string, actor core.Subjects, companyID string, typeKeys *[]string, tags *[]string) error
	CompanyLabels(ctx context.Context, orgID string, actor core.Subjects, companyID string) (*Labels, error)
	TagsInUse(ctx context.Context, orgID string, actor core.Subjects) ([]string, error)
	CompanyDetails(ctx context.Context, orgID string, actor core.Subjects, id string) (*Details, error)
	SetCompanyDetails(ctx context.Context, orgID string, actor core.Subjects, id string, d Details) error
	ContactDetails(ctx context.Context, orgID string, actor core.Subjects, id string) (*Details, error)
	SetContactDetails(ctx context.Context, orgID string, actor core.Subjects, id string, d Details) error

	CreateDeal(ctx context.Context, orgID string, actor core.Subjects, d Deal) (*Deal, error)
	UpdateDeal(ctx context.Context, orgID string, actor core.Subjects, d Deal) error
	MoveDeal(ctx context.Context, orgID string, actor core.Subjects, id, stage string, before *string) error
	DeleteDeal(ctx context.Context, orgID string, actor core.Subjects, id string) error
	GetDeal(ctx context.Context, orgID string, actor core.Subjects, id string) (*Deal, error)
	Deals(ctx context.Context, orgID string, actor core.Subjects, q ListQuery) ([]*Deal, int, error)
	GroupCounts(ctx context.Context, orgID string, actor core.Subjects, list string, q ListQuery) (map[string]int, error)
	SetFavorite(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref, on bool) error
	IsFavorite(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref) (bool, error)
	Favorites(ctx context.Context, orgID string, actor core.Subjects) ([]Favorite, error)
	DealsFor(ctx context.Context, orgID string, actor core.Subjects, t core.Ref) ([]*Deal, error)

	Stats(ctx context.Context, orgID string, actor core.Subjects) (*Stats, error)
	// HasSalesAccess reports whether the actor works sales: a member of the Sales team, or a super admin.
	HasSalesAccess(ctx context.Context, orgID string, actor core.Subjects) (bool, error)
	PersonStats(ctx context.Context, orgID string, actor core.Subjects, owner string, today, from, to time.Time) (*PersonStats, error)
	Unassigned(ctx context.Context, orgID string, actor core.Subjects, t core.EntityType, q ListQuery) ([]*Unowned, int, error)
	UnassignedCounts(ctx context.Context, orgID string, actor core.Subjects) (map[core.EntityType]int, error)
	Share(ctx context.Context, orgID string, actor core.Subjects, t core.Ref) error
	SetOwner(ctx context.Context, orgID string, actor core.Subjects, t core.Ref, owner string) error

	AddActivity(ctx context.Context, orgID string, actor core.Subjects, a Activity) (*Activity, error)
	DeleteActivity(ctx context.Context, orgID string, actor core.Subjects, id string) error
	ActivitiesFor(ctx context.Context, orgID string, actor core.Subjects, t core.Ref) ([]*Activity, error)
}

var _ Sales = (*Service)(nil)

// superKey is not a subject anybody holds. keysOf appends it for a super admin and visible lets it through, so the one
// filter every list carries lets a super admin see every record, as the rest of Hearth does, and nobody else.
const superKey = core.SuperKey

// keysOf is the subject keys a list filters on: the actor's own, and for a super admin the key that sees everything.
func keysOf(actor core.Subjects) []string { return actor.GrantKeys() }

// visible is the permission filter every list uses: a row is the caller's to
// see when some grant on it names a subject they hold, or when they are a super
// admin.
func visible(entity, alias string, keysArg int) string {
	return fmt.Sprintf(`($%d::text[] @> ARRAY['%s'] OR EXISTS (SELECT 1 FROM grants g
	       WHERE g.entity_type = '%s' AND g.entity_id = %s.id
	         AND g.subject_key = ANY($%d)
	         AND (g.expires_at IS NULL OR g.expires_at > now())))`, keysArg, superKey, entity, alias, keysArg)
}

// sortCol maps a requested sort onto a column. Whitelisted, because the value
// comes from the address bar and goes into an ORDER BY.
func sortCol(m map[string]string, want, fallback string) string {
	if c, ok := m[want]; ok {
		return c
	}
	return m[fallback]
}

func dir(desc bool) string {
	if desc {
		return "DESC"
	}
	return "ASC"
}

func ownerFilter(col string, owner string, args *[]any) string {
	switch owner {
	case "":
		return ""
	case "none":
		return " AND " + col + " IS NULL"
	}
	if _, err := uuid.Parse(owner); err != nil {
		return ""
	}
	*args = append(*args, owner)
	return fmt.Sprintf(" AND %s = $%d", col, len(*args))
}

// -----------------------------------------------------------------------------
// Company and contact lists with owner, sort, and paging
// -----------------------------------------------------------------------------

var companySorts = map[string]string{
	"name": "lower(c.name)", "kind": "c.kind", "domain": "c.domain",
	"owner": "lower(coalesce(op.display_name,''))", "created": "c.created_at",
	"updated": "c.updated_at", "revenue": "c.annual_revenue_cents",
	"contacts": "contacts_n", "deals": "deals_n",
}

// CompaniesPage lists firms with the sales columns, filtered, sorted and paged.
// The int is the total across all pages.
func (s *Service) CompaniesPage(ctx context.Context, orgID string, actor core.Subjects,
	q ListQuery) ([]*CompanyRow, int, error) {

	where, args := companyFilter(orgID, actor, q)

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM companies c
		WHERE c.org_id = $1 AND c.deleted_at IS NULL`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("crm: count companies: %w", err)
	}
	limit, off := q.page()
	order := sortCol(companySorts, q.Sort, "name")
	if g := groupOrder(companyGroups, q.Group); g != "" {
		order = g + ", " + order
	}
	args = append(args, limit, off)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT c.id::text, c.name, c.domain, c.kind, c.notes, c.created_at, c.updated_at,
		       coalesce(c.owner_id::text,''), coalesce(op.display_name,''), c.website,
		       c.address_city, c.address_state, c.annual_revenue_cents, c.revenue_currency,
		       (SELECT count(*) FROM contacts ct
		         WHERE ct.company_id = c.id AND ct.deleted_at IS NULL)::int AS contacts_n,
		       (SELECT count(*) FROM deals d
		         WHERE d.company_id = c.id AND d.deleted_at IS NULL)::int AS deals_n,
		       coalesce((SELECT array_agg(t.key ORDER BY t.position, t.label) FROM company_type_links l
		                  JOIN company_types t ON t.id = l.type_id WHERE l.company_id = c.id), '{}'),
		       coalesce((SELECT array_agg(t.label ORDER BY t.position, t.label) FROM company_type_links l
		                  JOIN company_types t ON t.id = l.type_id WHERE l.company_id = c.id), '{}'),
		       coalesce((SELECT array_agg(g.tag ORDER BY lower(g.tag)) FROM company_tags g WHERE g.company_id = c.id), '{}')
		FROM companies c
		LEFT JOIN principals op ON op.id = c.owner_id
		WHERE c.org_id = $1 AND c.deleted_at IS NULL%s
		ORDER BY %s %s NULLS LAST, c.id
		LIMIT $%d OFFSET $%d`, where, order, dir(q.Desc), len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("crm: list companies: %w", err)
	}
	defer rows.Close()
	var out []*CompanyRow
	for rows.Next() {
		r := &CompanyRow{}
		r.OrgID = orgID
		if err := rows.Scan(&r.ID, &r.Name, &r.Domain, &r.Kind, &r.Notes, &r.CreatedAt,
			&r.UpdatedAt, &r.OwnerID, &r.OwnerName, &r.Website, &r.City, &r.State,
			&r.RevenueCents, &r.Currency, &r.Contacts, &r.Deals, &r.TypeKeys, &r.TypeLabels, &r.Tags); err != nil {
			return nil, 0, fmt.Errorf("crm: scan company: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// companyFilter is the WHERE the companies list asks, shared by the page and the group totals so the two cannot disagree.
func companyFilter(orgID string, actor core.Subjects, q ListQuery) (string, []any) {
	args := []any{orgID, keysOf(actor)}
	where := " AND " + visible("company", "c", 2)
	if q.Kind != "" && validKinds[q.Kind] {
		args = append(args, q.Kind)
		where += fmt.Sprintf(" AND c.kind = $%d", len(args))
	}
	if t := strings.TrimSpace(q.Search); t != "" {
		args = append(args, "%"+t+"%")
		where += fmt.Sprintf(" AND (c.name ILIKE $%d OR c.domain ILIKE $%d OR c.website ILIKE $%d)",
			len(args), len(args), len(args))
	}
	where += ownerFilter("c.owner_id", q.Owner, &args)
	where += propFilter(core.EntityCompany, "c", q, &args)
	switch q.Type {
	case "":
	case "none":
		where += " AND NOT EXISTS (SELECT 1 FROM company_type_links l WHERE l.company_id = c.id)"
	default:
		args = append(args, q.Type)
		where += fmt.Sprintf(` AND EXISTS (SELECT 1 FROM company_type_links l JOIN company_types t ON t.id = l.type_id
			WHERE l.company_id = c.id AND t.key = $%d)`, len(args))
	}
	if tg := strings.TrimSpace(q.Tag); tg != "" {
		args = append(args, tg)
		where += fmt.Sprintf(" AND EXISTS (SELECT 1 FROM company_tags g WHERE g.company_id = c.id AND lower(g.tag) = lower($%d))", len(args))
	}
	return where, args
}

var contactSorts = map[string]string{
	"name": "lower(ct.name)", "company": "lower(coalesce(co.name,''))",
	"title": "lower(ct.title)", "email": "lower(ct.email)", "phone": "ct.phone",
	"owner": "lower(coalesce(op.display_name,''))", "created": "ct.created_at",
	"updated": "ct.updated_at",
}

// contactFilter is the WHERE the contacts list asks.
func contactFilter(orgID string, actor core.Subjects, q ListQuery) (string, []any) {
	args := []any{orgID, keysOf(actor)}
	where := " AND " + visible("contact", "ct", 2)
	if q.CompanyID != "" {
		args = append(args, q.CompanyID)
		where += fmt.Sprintf(" AND ct.company_id = $%d", len(args))
	}
	if t := strings.TrimSpace(q.Search); t != "" {
		args = append(args, "%"+t+"%")
		where += fmt.Sprintf(" AND (ct.name ILIKE $%d OR ct.email ILIKE $%d OR ct.phone ILIKE $%d OR co.name ILIKE $%d)",
			len(args), len(args), len(args), len(args))
	}
	if q.PhoneE164 != "" {
		args = append(args, q.PhoneE164)
		where += fmt.Sprintf(" AND phone_e164_all(ct.phone, ct.extra_phones) @> ARRAY[$%d]::text[]", len(args))
	}
	where += ownerFilter("ct.owner_id", q.Owner, &args)
	where += propFilter(core.EntityContact, "ct", q, &args)
	return where, args
}

// ContactsPage lists people with the sales columns, filtered, sorted and paged.
func (s *Service) ContactsPage(ctx context.Context, orgID string, actor core.Subjects,
	q ListQuery) ([]*ContactRow, int, error) {

	where, args := contactFilter(orgID, actor, q)

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM contacts ct
		LEFT JOIN companies co ON co.id = ct.company_id AND co.deleted_at IS NULL
		WHERE ct.org_id = $1 AND ct.deleted_at IS NULL`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("crm: count contacts: %w", err)
	}
	limit, off := q.page()
	order := sortCol(contactSorts, q.Sort, "name")
	if g := groupOrder(contactGroups, q.Group); g != "" {
		order = g + ", " + order
	}
	args = append(args, limit, off)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		SELECT ct.id::text, ct.company_id::text, COALESCE(co.name, ''), ct.name,
		       ct.email, ct.phone, ct.title, ct.notes, ct.created_at, ct.updated_at,
		       coalesce(ct.owner_id::text,''), coalesce(op.display_name,''), ct.linkedin_url
		FROM contacts ct
		LEFT JOIN companies co ON co.id = ct.company_id AND co.deleted_at IS NULL
		LEFT JOIN principals op ON op.id = ct.owner_id
		WHERE ct.org_id = $1 AND ct.deleted_at IS NULL%s
		ORDER BY %s %s NULLS LAST, ct.id
		LIMIT $%d OFFSET $%d`, where, order, dir(q.Desc), len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("crm: list contacts: %w", err)
	}
	defer rows.Close()
	var out []*ContactRow
	for rows.Next() {
		r := &ContactRow{}
		r.OrgID = orgID
		if err := rows.Scan(&r.ID, &r.CompanyID, &r.CompanyName, &r.Name, &r.Email,
			&r.Phone, &r.Title, &r.Notes, &r.CreatedAt, &r.UpdatedAt,
			&r.OwnerID, &r.OwnerName, &r.LinkedIn); err != nil {
			return nil, 0, fmt.Errorf("crm: scan contact: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// -----------------------------------------------------------------------------
// Details and owners
// -----------------------------------------------------------------------------

func (s *Service) checkOwner(ctx context.Context, orgID, owner string) error {
	if owner == "" {
		return nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM principals
		WHERE id = $1 AND org_id = $2 AND kind = 'user')`, owner, orgID).Scan(&ok)
	if err != nil {
		return fmt.Errorf("crm: check owner: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: that owner is not a person here", ErrInvalid)
	}
	return nil
}

// CompanyDetails reads the sales columns of one firm.
func (s *Service) CompanyDetails(ctx context.Context, orgID string, actor core.Subjects,
	id string) (*Details, error) {

	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityCompany, ID: id},
		core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	d := &Details{}
	err := s.db.QueryRow(ctx, `
		SELECT coalesce(c.owner_id::text,''), coalesce(op.display_name,''), c.website,
		       c.linkedin_url, c.address_street, c.address_city, c.address_state,
		       c.address_postcode, c.address_country, c.annual_revenue_cents, c.revenue_currency, c.tax_id
		FROM companies c LEFT JOIN principals op ON op.id = c.owner_id
		WHERE c.id = $1 AND c.org_id = $2 AND c.deleted_at IS NULL`, id, orgID).
		Scan(&d.OwnerID, &d.OwnerName, &d.Website, &d.LinkedIn, &d.Street, &d.City,
			&d.State, &d.Postcode, &d.Country, &d.RevenueCents, &d.Currency, &d.TaxID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("crm: company details: %w", err)
	}
	return d, nil
}

// SetCompanyDetails writes the sales columns of one firm.
func (s *Service) SetCompanyDetails(ctx context.Context, orgID string, actor core.Subjects,
	id string, d Details) error {

	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityCompany, ID: id},
		core.AccessEdit); err != nil {
		return err
	}
	if err := s.checkOwner(ctx, orgID, d.OwnerID); err != nil {
		return err
	}
	cur := d.Currency
	if cur == "" {
		cur = "USD"
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ct, err := tx.Exec(ctx, `
		UPDATE companies SET owner_id = NULLIF($3,'')::uuid, website=$4, linkedin_url=$5,
		       address_street=$6, address_city=$7, address_state=$8, address_postcode=$9,
		       address_country=$10, annual_revenue_cents=$11, revenue_currency=$12,
		       tax_id=$13, updated_at = now()
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`,
		id, orgID, d.OwnerID, strings.TrimSpace(d.Website), strings.TrimSpace(d.LinkedIn),
		strings.TrimSpace(d.Street), strings.TrimSpace(d.City), strings.TrimSpace(d.State),
		strings.TrimSpace(d.Postcode), strings.TrimSpace(d.Country), d.RevenueCents, cur, strings.TrimSpace(d.TaxID))
	if err != nil {
		return fmt.Errorf("crm: set company details: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if d.OwnerID != "" {
		if err := grantOwner(ctx, tx, orgID, core.EntityCompany, id, d.OwnerID, actor.Principal()); err != nil {
			return err
		}
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityCompany), EntityID: id,
		Event: jobs.EventUpdated, ActorID: actor.Principal(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ContactDetails reads the sales columns of one person.
func (s *Service) ContactDetails(ctx context.Context, orgID string, actor core.Subjects,
	id string) (*Details, error) {

	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityContact, ID: id},
		core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	d := &Details{}
	err := s.db.QueryRow(ctx, `
		SELECT coalesce(c.owner_id::text,''), coalesce(op.display_name,''), c.linkedin_url,
		       c.extra_emails, c.extra_phones, c.marketing_opt_out_at, c.marketing_opt_out_reason
		FROM contacts c LEFT JOIN principals op ON op.id = c.owner_id
		WHERE c.id = $1 AND c.org_id = $2 AND c.deleted_at IS NULL`, id, orgID).
		Scan(&d.OwnerID, &d.OwnerName, &d.LinkedIn, &d.ExtraEmails, &d.ExtraPhones, &d.MarketingOptOutAt, &d.MarketingOptOutReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("crm: contact details: %w", err)
	}
	return d, nil
}

// SetContactOptOut opts one person out of marketing email for a stated reason: "unsubscribed", "bounced" or "manual". It
// never clears an opt-out and never changes the reason of one already set, and it says whether it set one.
func (s *Service) SetContactOptOut(ctx context.Context, orgID string, actor core.Subjects, id, reason string) (bool, error) {
	switch reason {
	case "unsubscribed", "bounced", "manual":
	default:
		return false, fmt.Errorf("crm: opt-out reason %q is not unsubscribed, bounced or manual", reason)
	}
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityContact, ID: id}, core.AccessEdit); err != nil {
		return false, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ct, err := tx.Exec(ctx, `UPDATE contacts SET marketing_opt_out_at = now(), marketing_opt_out_reason = $3, updated_at = now()
		WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL AND marketing_opt_out_at IS NULL`, id, orgID, reason)
	if err != nil {
		return false, fmt.Errorf("crm: opt out contact: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return false, nil
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityContact), EntityID: id,
		Event: jobs.EventUpdated, ActorID: actor.Principal(),
	}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// SetContactDetails writes the sales columns of one person.
func (s *Service) SetContactDetails(ctx context.Context, orgID string, actor core.Subjects,
	id string, d Details) error {

	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityContact, ID: id},
		core.AccessEdit); err != nil {
		return err
	}
	if err := s.checkOwner(ctx, orgID, d.OwnerID); err != nil {
		return err
	}
	if d.ExtraEmails == nil {
		d.ExtraEmails = []string{}
	}
	if d.ExtraPhones == nil {
		d.ExtraPhones = []string{}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ct, err := tx.Exec(ctx, `
		UPDATE contacts SET owner_id = NULLIF($3,'')::uuid, linkedin_url=$4,
		       extra_emails=$5, extra_phones=$6, updated_at = now(),
		       -- Opting out is one way: a save can set it and never clears it.
		       marketing_opt_out_at = CASE WHEN marketing_opt_out_at IS NULL AND $7 THEN now() ELSE marketing_opt_out_at END,
		       marketing_opt_out_reason = CASE WHEN marketing_opt_out_at IS NULL AND $7 THEN 'manual' ELSE marketing_opt_out_reason END
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`,
		id, orgID, d.OwnerID, strings.TrimSpace(d.LinkedIn), d.ExtraEmails, d.ExtraPhones, d.OptOut)
	if err != nil {
		return fmt.Errorf("crm: set contact details: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if d.OwnerID != "" {
		if err := grantOwner(ctx, tx, orgID, core.EntityContact, id, d.OwnerID, actor.Principal()); err != nil {
			return err
		}
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityContact), EntityID: id,
		Event: jobs.EventUpdated, ActorID: actor.Principal(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// grantOwner makes sure the owner can edit the record they own, at least.
// Never lowers an existing grant: the insert is skipped when they have one.
func grantOwner(ctx context.Context, tx pgx.Tx, orgID string, t core.EntityType,
	id, owner, by string) error {

	_, err := tx.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'admin', $5)
		ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
		orgID, string(t), id,
		core.Subject{Kind: core.SubjectPrincipal, ID: owner}.Key(), by)
	if err != nil {
		return fmt.Errorf("crm: grant owner: %w", err)
	}
	return nil
}

// SalesTeamName is the team whose members work the CRM. Hearth's roles are
// teams and grants, so "sales staff" is membership of this team, and admins see
// everything through the super bypass they already have.
const SalesTeamName = "Sales"

type rower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ShareKey is the grant subject that makes a CRM record visible to the people
// who work the CRM: the Sales team. An organisation with no such team gets an
// empty key, and records stay as private as Hearth makes any record, visible to
// their creator and to admins. Sharing with the whole organisation instead would
// have quietly widened who can see contacts.
func ShareKey(ctx context.Context, q rower, orgID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id::text FROM teams
		WHERE org_id = $1 AND lower(btrim(name)) = lower($2) ORDER BY created_at LIMIT 1`,
		orgID, SalesTeamName).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("crm: find the sales team: %w", err)
	}
	return core.Subject{Kind: core.SubjectTeam, ID: id}.Key(), nil
}

// ShareWithSales gives the sales team edit on a record. Ownership never gates
// this: an owner is a label and a filter, and everybody who works the CRM sees
// every record. A narrower grant already in place is left as it is.
func ShareWithSales(ctx context.Context, tx pgx.Tx, orgID string, t core.EntityType,
	id, by string) error {

	key, err := ShareKey(ctx, tx, orgID)
	if err != nil || key == "" {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'edit', $5)
		ON CONFLICT (entity_type, entity_id, subject_key) DO NOTHING`,
		orgID, string(t), id, key, by)
	if err != nil {
		return fmt.Errorf("crm: share %s: %w", t, err)
	}
	return nil
}

// Share applies ShareWithSales to a record that already exists, for the ones
// made through the older forms. The caller must be able to administer it.
func (s *Service) Share(ctx context.Context, orgID string, actor core.Subjects, t core.Ref) error {
	if err := s.authz.Require(ctx, actor, t, core.AccessAdmin); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ShareWithSales(ctx, tx, orgID, t.Type, t.ID, actor.Principal()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// -----------------------------------------------------------------------------
// Deals
// -----------------------------------------------------------------------------

func cleanDeal(d *Deal) error {
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return fmt.Errorf("%w: a deal needs a name", ErrInvalid)
	}
	if d.Stage == "" {
		d.Stage = StageNew
	}
	if !ValidStage(d.Stage) {
		return fmt.Errorf("%w: %q is not a stage", ErrInvalid, d.Stage)
	}
	if d.AmountCents < 0 {
		return fmt.Errorf("%w: an amount cannot be negative", ErrInvalid)
	}
	if d.Currency == "" {
		d.Currency = "USD"
	}
	if d.CompanyID != nil && *d.CompanyID == "" {
		d.CompanyID = nil
	}
	if d.ContactID != nil && *d.ContactID == "" {
		d.ContactID = nil
	}
	if d.OwnerID != nil && *d.OwnerID == "" {
		d.OwnerID = nil
	}
	return nil
}

// CreateDeal writes a deal, its grants, and its creation event in one
// transaction. It is shared with the sales team and put at the bottom of its
// column, so a new card does not push the ones people are working on.
func (s *Service) CreateDeal(ctx context.Context, orgID string, actor core.Subjects,
	d Deal) (*Deal, error) {

	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("crm: no acting principal")
	}
	if err := cleanDeal(&d); err != nil {
		return nil, err
	}
	if d.OwnerID != nil {
		if err := s.checkOwner(ctx, orgID, *d.OwnerID); err != nil {
			return nil, err
		}
	}
	if d.CompanyID != nil {
		if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityCompany, ID: *d.CompanyID},
			core.AccessView); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	d.ID, d.OrgID = uuid.NewString(), orgID
	err = tx.QueryRow(ctx, `
		INSERT INTO deals (id, org_id, name, stage, amount_cents, currency, close_date,
		                   company_id, contact_id, owner_id, notes, created_by, position)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
		        coalesce((SELECT max(position) + 1 FROM deals
		                   WHERE org_id = $2 AND stage = $4 AND deleted_at IS NULL), 0))
		RETURNING position, created_at, updated_at`,
		d.ID, orgID, d.Name, d.Stage, d.AmountCents, d.Currency, d.CloseDate, d.CompanyID,
		d.ContactID, d.OwnerID, strings.TrimSpace(d.Notes), principal).
		Scan(&d.Position, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("crm: create deal: %w", err)
	}
	if err := grantAdmin(ctx, tx, orgID, core.EntityDeal, d.ID, principal); err != nil {
		return nil, err
	}
	if err := ShareWithSales(ctx, tx, orgID, core.EntityDeal, d.ID, principal); err != nil {
		return nil, err
	}
	if d.OwnerID != nil {
		if err := grantOwner(ctx, tx, orgID, core.EntityDeal, d.ID, *d.OwnerID, principal); err != nil {
			return nil, err
		}
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDeal), EntityID: d.ID,
		Event: jobs.EventCreated, ActorID: principal,
		Payload: map[string]any{"name": d.Name, "stage": d.Stage},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("crm: commit: %w", err)
	}
	return &d, nil
}

// UpdateDeal changes a deal's fields. Stage and position go through MoveDeal.
func (s *Service) UpdateDeal(ctx context.Context, orgID string, actor core.Subjects, d Deal) error {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDeal, ID: d.ID},
		core.AccessEdit); err != nil {
		return err
	}
	if err := cleanDeal(&d); err != nil {
		return err
	}
	if d.OwnerID != nil {
		if err := s.checkOwner(ctx, orgID, *d.OwnerID); err != nil {
			return err
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ct, err := tx.Exec(ctx, `
		UPDATE deals SET name=$3, amount_cents=$4, currency=$5, close_date=$6, company_id=$7,
		       contact_id=$8, owner_id=$9, notes=$10, updated_at=now(),
		       stage = $11,
		       position = CASE WHEN stage = $11 THEN position ELSE
		           coalesce((SELECT max(position) + 1 FROM deals
		                      WHERE org_id = $2 AND stage = $11 AND deleted_at IS NULL), 0) END
		 WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`,
		d.ID, orgID, d.Name, d.AmountCents, d.Currency, d.CloseDate, d.CompanyID,
		d.ContactID, d.OwnerID, strings.TrimSpace(d.Notes), d.Stage)
	if err != nil {
		return fmt.Errorf("crm: update deal: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if d.OwnerID != nil {
		if err := grantOwner(ctx, tx, orgID, core.EntityDeal, d.ID, *d.OwnerID, actor.Principal()); err != nil {
			return err
		}
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDeal), EntityID: d.ID,
		Event: jobs.EventUpdated, ActorID: actor.Principal(),
		Payload: map[string]any{"name": d.Name, "stage": d.Stage},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MoveDeal puts a deal in a stage, above the card named in before, or at the
// bottom when before is nil. This is the write behind dragging a card.
func (s *Service) MoveDeal(ctx context.Context, orgID string, actor core.Subjects,
	id, stage string, before *string) error {

	if !ValidStage(stage) {
		return fmt.Errorf("%w: %q is not a stage", ErrInvalid, stage)
	}
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDeal, ID: id},
		core.AccessEdit); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A position between the card above the drop point and the card below it.
	// Halving the gap is enough here: a column is a few dozen cards, and the
	// renumber below covers the day the gaps run out.
	var pos float64
	if before != nil && *before != "" {
		var below float64
		err := tx.QueryRow(ctx, `SELECT position FROM deals
			WHERE id=$1 AND org_id=$2 AND stage=$3 AND deleted_at IS NULL`,
			*before, orgID, stage).Scan(&below)
		if err != nil {
			return fmt.Errorf("%w: that card is not in that column", ErrInvalid)
		}
		var above *float64
		if err := tx.QueryRow(ctx, `SELECT max(position) FROM deals
			WHERE org_id=$1 AND stage=$2 AND deleted_at IS NULL AND position < $3 AND id <> $4`,
			orgID, stage, below, id).Scan(&above); err != nil {
			return fmt.Errorf("crm: find neighbour: %w", err)
		}
		if above == nil {
			pos = below - 1
		} else {
			pos = (*above + below) / 2
		}
	} else if err := tx.QueryRow(ctx, `SELECT coalesce(max(position) + 1, 0) FROM deals
			WHERE org_id=$1 AND stage=$2 AND deleted_at IS NULL AND id <> $3`,
		orgID, stage, id).Scan(&pos); err != nil {
		return fmt.Errorf("crm: find bottom: %w", err)
	}
	ct, err := tx.Exec(ctx, `UPDATE deals SET stage=$3, position=$4, updated_at=now()
		WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID, stage, pos)
	if err != nil {
		return fmt.Errorf("crm: move deal: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDeal), EntityID: id,
		Event: jobs.EventUpdated, ActorID: actor.Principal(),
		Payload: map[string]any{"stage": stage},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteDeal soft deletes a deal.
func (s *Service) DeleteDeal(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDeal, ID: id},
		core.AccessAdmin); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ct, err := tx.Exec(ctx, `UPDATE deals SET deleted_at=now(), updated_at=now()
		WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID)
	if err != nil {
		return fmt.Errorf("crm: delete deal: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(core.EntityDeal), EntityID: id,
		Event: jobs.EventDeleted, ActorID: actor.Principal(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const dealSelect = `
	SELECT d.id::text, d.name, d.stage, d.amount_cents, d.currency, d.close_date,
	       d.company_id::text, coalesce(co.name,''), d.contact_id::text, coalesce(ct.name,''),
	       d.owner_id::text, coalesce(op.display_name,''), d.position, d.notes,
	       d.created_at, d.updated_at, d.lost_reason, d.stage_changed_at, d.closed_at
	FROM deals d
	LEFT JOIN companies co ON co.id = d.company_id AND co.deleted_at IS NULL
	LEFT JOIN contacts  ct ON ct.id = d.contact_id AND ct.deleted_at IS NULL
	LEFT JOIN principals op ON op.id = d.owner_id`

func scanDeal(row pgx.Row, orgID string) (*Deal, error) {
	d := &Deal{OrgID: orgID}
	err := row.Scan(&d.ID, &d.Name, &d.Stage, &d.AmountCents, &d.Currency, &d.CloseDate,
		&d.CompanyID, &d.CompanyName, &d.ContactID, &d.ContactName, &d.OwnerID,
		&d.OwnerName, &d.Position, &d.Notes, &d.CreatedAt, &d.UpdatedAt, &d.LostReason, &d.StageChangedAt, &d.ClosedAt)
	return d, err
}

// GetDeal returns one deal if the caller may see it.
func (s *Service) GetDeal(ctx context.Context, orgID string, actor core.Subjects, id string) (*Deal, error) {
	if err := s.authz.Require(ctx, actor, core.Ref{Type: core.EntityDeal, ID: id},
		core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	d, err := scanDeal(s.db.QueryRow(ctx, dealSelect+`
		WHERE d.id=$1 AND d.org_id=$2 AND d.deleted_at IS NULL`, id, orgID), orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("crm: get deal: %w", err)
	}
	return d, nil
}

var dealSorts = map[string]string{
	"name": "lower(d.name)", "stage": "array_position(ARRAY['new','screening','meeting','proposal','customer','won','lost','no_response'], d.stage)",
	"amount": "d.amount_cents", "close": "d.close_date", "company": "lower(coalesce(co.name,''))",
	"owner": "lower(coalesce(op.display_name,''))", "created": "d.created_at",
	"updated": "d.updated_at", "position": "d.position", "closed": "d.closed_at",
}

// dealFilter is the WHERE the deals list asks.
func dealFilter(orgID string, actor core.Subjects, q ListQuery) (string, []any) {
	args := []any{orgID, keysOf(actor)}
	where := " AND " + visible("deal", "d", 2)
	if q.Stage != "" && ValidStage(q.Stage) {
		args = append(args, q.Stage)
		where += fmt.Sprintf(" AND d.stage = $%d", len(args))
	}
	if q.CompanyID != "" {
		args = append(args, q.CompanyID)
		where += fmt.Sprintf(" AND d.company_id = $%d", len(args))
	}
	if q.ContactID != "" {
		args = append(args, q.ContactID)
		where += fmt.Sprintf(" AND d.contact_id = $%d", len(args))
	}
	if t := strings.TrimSpace(q.Search); t != "" {
		args = append(args, "%"+t+"%")
		where += fmt.Sprintf(" AND (d.name ILIKE $%d OR co.name ILIKE $%d OR ct.name ILIKE $%d)",
			len(args), len(args), len(args))
	}
	where += ownerFilter("d.owner_id", q.Owner, &args)
	where += propFilter(core.EntityDeal, "d", q, &args)
	if q.Open || q.Overdue {
		where += " AND d.stage NOT IN ('won', 'lost', 'no_response')"
	}
	if q.Overdue {
		args = append(args, time.Now().Format("2006-01-02"))
		where += fmt.Sprintf(" AND d.close_date IS NOT NULL AND d.close_date < $%d::date", len(args))
	}
	if q.NoCloseDate {
		where += " AND d.close_date IS NULL"
	}
	if q.CloseFrom != nil {
		args = append(args, q.CloseFrom.Format("2006-01-02"))
		where += fmt.Sprintf(" AND d.close_date >= $%d::date", len(args))
	}
	if q.CloseTo != nil {
		args = append(args, q.CloseTo.Format("2006-01-02"))
		where += fmt.Sprintf(" AND d.close_date <= $%d::date", len(args))
	}
	if q.ClosedFrom != nil {
		args = append(args, *q.ClosedFrom)
		where += fmt.Sprintf(" AND d.closed_at >= $%d", len(args))
	}
	if q.ClosedTo != nil {
		args = append(args, q.ClosedTo.AddDate(0, 0, 1))
		where += fmt.Sprintf(" AND d.closed_at < $%d", len(args))
	}
	return where, args
}

// Deals lists deals, filtered, sorted and paged, with the total across pages.
func (s *Service) Deals(ctx context.Context, orgID string, actor core.Subjects,
	q ListQuery) ([]*Deal, int, error) {

	where, args := dealFilter(orgID, actor, q)

	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM deals d
		LEFT JOIN companies co ON co.id = d.company_id AND co.deleted_at IS NULL
		LEFT JOIN contacts  ct ON ct.id = d.contact_id AND ct.deleted_at IS NULL
		WHERE d.org_id = $1 AND d.deleted_at IS NULL`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("crm: count deals: %w", err)
	}
	limit, off := q.page()
	args = append(args, limit, off)
	orderBy := "d.updated_at DESC, d.id"
	if q.Sort != "" {
		orderBy = fmt.Sprintf("%s %s NULLS LAST, d.id", sortCol(dealSorts, q.Sort, "updated"), dir(q.Desc))
	}
	if g := groupOrder(dealGroups, q.Group); g != "" {
		orderBy = g + ", " + orderBy
	}
	rows, err := s.db.Query(ctx, fmt.Sprintf(dealSelect+`
		WHERE d.org_id = $1 AND d.deleted_at IS NULL%s
		ORDER BY %s LIMIT $%d OFFSET $%d`, where, orderBy, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("crm: list deals: %w", err)
	}
	defer rows.Close()
	var out []*Deal
	for rows.Next() {
		d, err := scanDeal(rows, orgID)
		if err != nil {
			return nil, 0, fmt.Errorf("crm: scan deal: %w", err)
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// DealsFor lists the deals on one company or contact.
func (s *Service) DealsFor(ctx context.Context, orgID string, actor core.Subjects,
	t core.Ref) ([]*Deal, error) {

	col := "d.company_id"
	if t.Type == core.EntityContact {
		col = "d.contact_id"
	} else if t.Type != core.EntityCompany {
		return nil, fmt.Errorf("%w: deals hang off a company or a contact", ErrInvalid)
	}
	rows, err := s.db.Query(ctx, dealSelect+` WHERE d.org_id = $1 AND d.deleted_at IS NULL
		AND `+col+` = $3 AND `+visible("deal", "d", 2)+` ORDER BY d.updated_at DESC`,
		orgID, keysOf(actor), t.ID)
	if err != nil {
		return nil, fmt.Errorf("crm: deals for: %w", err)
	}
	defer rows.Close()
	var out []*Deal
	for rows.Next() {
		d, err := scanDeal(rows, orgID)
		if err != nil {
			return nil, fmt.Errorf("crm: scan deal: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------------
// Activities
// -----------------------------------------------------------------------------

// AddActivity records a note or a call about one or more records. The caller
// must be able to edit every record it is about.
func (s *Service) AddActivity(ctx context.Context, orgID string, actor core.Subjects,
	a Activity) (*Activity, error) {

	principal := actor.Principal()
	if principal == "" {
		return nil, errors.New("crm: no acting principal")
	}
	if a.Kind != ActNote && a.Kind != ActCall && a.Kind != ActMeeting && a.Kind != ActEmail {
		return nil, fmt.Errorf("%w: %q is not a kind of activity", ErrInvalid, a.Kind)
	}
	a.Title = strings.TrimSpace(a.Title)
	a.Body = strings.TrimSpace(a.Body)
	if a.Title == "" && a.Body == "" {
		return nil, fmt.Errorf("%w: write something first", ErrInvalid)
	}
	if len(a.Targets) == 0 {
		return nil, fmt.Errorf("%w: an activity has to be about something", ErrInvalid)
	}
	for _, t := range a.Targets {
		if t.Type != core.EntityCompany && t.Type != core.EntityContact && t.Type != core.EntityDeal {
			return nil, fmt.Errorf("%w: cannot attach to a %s", ErrInvalid, t.Type)
		}
		if err := s.authz.Require(ctx, actor, t, core.AccessEdit); err != nil {
			return nil, err
		}
	}
	switch a.Kind {
	case ActCall, ActEmail:
		if a.Direction != "" && a.Direction != Inbound && a.Direction != Outbound {
			return nil, fmt.Errorf("%w: a %s goes in or out", ErrInvalid, a.Kind)
		}
		if a.Kind == ActEmail {
			a.DurationMs = nil
		}
	case ActMeeting:
		a.Direction = ""
	default:
		a.Direction, a.Disposition, a.DurationMs = "", "", nil
	}
	if a.OccurredAt.IsZero() {
		a.OccurredAt = time.Now()
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a.ID = uuid.NewString()
	a.CreatedBy = principal
	err = tx.QueryRow(ctx, `
		INSERT INTO crm_activities (id, org_id, kind, title, body, occurred_at, direction,
		                            disposition, duration_ms, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10)
		RETURNING created_at`,
		a.ID, orgID, a.Kind, a.Title, a.Body, a.OccurredAt, a.Direction,
		strings.TrimSpace(a.Disposition), a.DurationMs, principal).Scan(&a.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("crm: add activity: %w", err)
	}
	if err := InsertTargets(ctx, tx, a.ID, a.Targets); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("crm: commit: %w", err)
	}
	return &a, nil
}

// InsertTargets records what an activity is about.
func InsertTargets(ctx context.Context, tx pgx.Tx, activityID string, targets []core.Ref) error {
	for _, t := range targets {
		if _, err := tx.Exec(ctx, `INSERT INTO crm_activity_targets (activity_id, target_type, target_id)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, activityID, string(t.Type), t.ID); err != nil {
			return fmt.Errorf("crm: add activity target: %w", err)
		}
	}
	return nil
}

// DeleteActivity removes an activity from every timeline it is on. The author
// may, and so may anybody with edit on all the records it is about.
func (s *Service) DeleteActivity(ctx context.Context, orgID string, actor core.Subjects, id string) error {
	var author string
	if err := s.db.QueryRow(ctx, `SELECT created_by::text FROM crm_activities
		WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, id, orgID).Scan(&author); err != nil {
		return ErrNotFound
	}
	if author != actor.Principal() {
		rows, err := s.db.Query(ctx, `SELECT target_type, target_id::text FROM crm_activity_targets
			WHERE activity_id=$1`, id)
		if err != nil {
			return fmt.Errorf("crm: activity targets: %w", err)
		}
		var refs []core.Ref
		for rows.Next() {
			var t, tid string
			if err := rows.Scan(&t, &tid); err != nil {
				rows.Close()
				return err
			}
			refs = append(refs, core.Ref{Type: core.EntityType(t), ID: tid})
		}
		rows.Close()
		for _, r := range refs {
			if err := s.authz.Require(ctx, actor, r, core.AccessEdit); err != nil {
				return err
			}
		}
	}
	_, err := s.db.Exec(ctx, `UPDATE crm_activities SET deleted_at=now(), updated_at=now()
		WHERE id=$1 AND org_id=$2`, id, orgID)
	if err != nil {
		return fmt.Errorf("crm: delete activity: %w", err)
	}
	return nil
}

// ActivitiesFor is the timeline of one record, newest first. It is what was
// written about this record and nothing else: a call about a deal shows on the
// deal, and on the company and contact it also names.
func (s *Service) ActivitiesFor(ctx context.Context, orgID string, actor core.Subjects,
	t core.Ref) ([]*Activity, error) {

	if err := s.authz.Require(ctx, actor, t, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `
		SELECT a.id::text, a.kind, a.title, a.body, a.occurred_at, coalesce(a.direction,''),
		       a.disposition, a.duration_ms, a.created_by::text, coalesce(p.display_name,''),
		       a.created_at
		FROM crm_activities a
		JOIN crm_activity_targets tg ON tg.activity_id = a.id
		LEFT JOIN principals p ON p.id = a.created_by
		WHERE a.org_id = $1 AND a.deleted_at IS NULL
		  AND tg.target_type = $2 AND tg.target_id = $3
		ORDER BY a.occurred_at DESC, a.id
		LIMIT 500`, orgID, string(t.Type), t.ID)
	if err != nil {
		return nil, fmt.Errorf("crm: activities: %w", err)
	}
	defer rows.Close()
	var out []*Activity
	for rows.Next() {
		a := &Activity{}
		if err := rows.Scan(&a.ID, &a.Kind, &a.Title, &a.Body, &a.OccurredAt, &a.Direction,
			&a.Disposition, &a.DurationMs, &a.CreatedBy, &a.ByName, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("crm: scan activity: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Unowned is a record nobody owns yet, as the queue lists it.
type Unowned struct {
	Ref       core.Ref
	Name      string
	Subtext   string
	CreatedAt time.Time
}

// Href is where the record lives.
func (u *Unowned) Href() string {
	switch u.Ref.Type {
	case core.EntityCompany:
		return "/companies/" + u.Ref.ID
	case core.EntityContact:
		return "/contacts/" + u.Ref.ID
	}
	return "/deals/" + u.Ref.ID
}

var unownedSQL = map[core.EntityType]struct{ from, name, sub string }{
	core.EntityCompany: {"companies r", "r.name", "concat_ws(' · ', NULLIF(r.kind,''), NULLIF(r.domain,''))"},
	core.EntityContact: {"contacts r LEFT JOIN companies co ON co.id = r.company_id AND co.deleted_at IS NULL",
		"r.name", "concat_ws(' · ', NULLIF(r.title,''), NULLIF(co.name,''), NULLIF(r.email,''))"},
	core.EntityDeal: {"deals r LEFT JOIN companies co ON co.id = r.company_id AND co.deleted_at IS NULL",
		"r.name", "concat_ws(' · ', initcap(replace(r.stage,'_',' ')), NULLIF(co.name,''))"},
}

// Unassigned lists records of one type with no owner, the ones the caller can
// see, oldest first so the queue drains from the front.
func (s *Service) Unassigned(ctx context.Context, orgID string, actor core.Subjects,
	t core.EntityType, q ListQuery) ([]*Unowned, int, error) {

	u, ok := unownedSQL[t]
	if !ok {
		return nil, 0, fmt.Errorf("%w: %q has no owner", ErrInvalid, t)
	}
	args := []any{orgID, keysOf(actor)}
	where := " AND r.owner_id IS NULL AND " + visible(string(t), "r", 2)
	if tx := strings.TrimSpace(q.Search); tx != "" {
		args = append(args, "%"+tx+"%")
		where += fmt.Sprintf(" AND r.name ILIKE $%d", len(args))
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM `+u.from+`
		WHERE r.org_id = $1 AND r.deleted_at IS NULL`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("crm: count unassigned: %w", err)
	}
	limit, off := q.page()
	args = append(args, limit, off)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT r.id::text, %s, %s, r.created_at FROM %s
		WHERE r.org_id = $1 AND r.deleted_at IS NULL%s
		ORDER BY r.created_at, r.id LIMIT $%d OFFSET $%d`, u.name, u.sub, u.from, where,
		len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("crm: list unassigned: %w", err)
	}
	defer rows.Close()
	var out []*Unowned
	for rows.Next() {
		r := &Unowned{Ref: core.Ref{Type: t}}
		if err := rows.Scan(&r.Ref.ID, &r.Name, &r.Subtext, &r.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("crm: scan unassigned: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// UnassignedCounts is how many of each type nobody owns.
func (s *Service) UnassignedCounts(ctx context.Context, orgID string, actor core.Subjects) (map[core.EntityType]int, error) {
	out := map[core.EntityType]int{}
	for t := range unownedSQL {
		_, n, err := s.Unassigned(ctx, orgID, actor, t, ListQuery{Limit: 1})
		if err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}

// SetOwner claims a record or hands it to somebody, or clears the owner when
// owner is empty. It needs edit, which everybody has on a shared sales record.
func (s *Service) SetOwner(ctx context.Context, orgID string, actor core.Subjects, t core.Ref, owner string) error {
	table := map[core.EntityType]string{core.EntityCompany: "companies", core.EntityContact: "contacts", core.EntityDeal: "deals"}[t.Type]
	if table == "" {
		return fmt.Errorf("%w: %q has no owner", ErrInvalid, t.Type)
	}
	if err := s.authz.Require(ctx, actor, t, core.AccessEdit); err != nil {
		return err
	}
	if err := s.checkOwner(ctx, orgID, owner); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ct, err := tx.Exec(ctx, `UPDATE `+table+` SET owner_id = NULLIF($3,'')::uuid, updated_at = now()
		WHERE id=$1 AND org_id=$2 AND deleted_at IS NULL`, t.ID, orgID, owner)
	if err != nil {
		return fmt.Errorf("crm: set owner: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if owner != "" {
		if err := grantOwner(ctx, tx, orgID, t.Type, t.ID, owner, actor.Principal()); err != nil {
			return err
		}
	}
	if err := jobs.Publish(ctx, tx, jobs.EventSpec{
		OrgID: orgID, EntityType: string(t.Type), EntityID: t.ID,
		Event: jobs.EventUpdated, ActorID: actor.Principal(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Stats is the counts behind a dashboard tile, over what the caller can see.
type Stats struct {
	Companies int
	Contacts  int
	Deals     int
	// Open deals are the ones not yet Won, Lost, or No response.
	OpenDeals      int
	OpenValueCents int64
	// ByStage is the count and the value of the deals in each stage.
	ByStage map[string]StageStat
}

// StageStat is one stage's count and value.
type StageStat struct {
	Count      int
	ValueCents int64
}

// Stats counts what the caller can see, with the same permission filter every
// list uses, so a tile never says more than the page behind it would show.
func (s *Service) Stats(ctx context.Context, orgID string, actor core.Subjects) (*Stats, error) {
	out := &Stats{ByStage: map[string]StageStat{}}
	keys := keysOf(actor)
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM companies c WHERE c.org_id=$1 AND c.deleted_at IS NULL AND `+
		visible("company", "c", 2), orgID, keys).Scan(&out.Companies); err != nil {
		return nil, fmt.Errorf("crm: stats companies: %w", err)
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM contacts c WHERE c.org_id=$1 AND c.deleted_at IS NULL AND `+
		visible("contact", "c", 2), orgID, keys).Scan(&out.Contacts); err != nil {
		return nil, fmt.Errorf("crm: stats contacts: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT d.stage, count(*), coalesce(sum(d.amount_cents),0)::bigint FROM deals d
		WHERE d.org_id=$1 AND d.deleted_at IS NULL AND `+visible("deal", "d", 2)+` GROUP BY d.stage`, orgID, keys)
	if err != nil {
		return nil, fmt.Errorf("crm: stats deals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		var v int64
		if err := rows.Scan(&st, &n, &v); err != nil {
			return nil, fmt.Errorf("crm: scan stats: %w", err)
		}
		out.ByStage[st] = StageStat{Count: n, ValueCents: v}
		out.Deals += n
		if st != StageWon && st != StageLost && st != StageNoResponse {
			out.OpenDeals += n
			out.OpenValueCents += v
		}
	}
	return out, rows.Err()
}

// HasSalesAccess reports whether the actor works sales: a super admin, or a member of the Sales team. It is the one
// answer every sales part of the dashboard asks, so a person outside sales gets none of them, and it is the same
// membership that makes a CRM record visible (ShareKey), so it cannot say yes where the records say no.
func (s *Service) HasSalesAccess(ctx context.Context, orgID string, actor core.Subjects) (bool, error) {
	if actor.Super() {
		return true, nil
	}
	key, err := ShareKey(ctx, s.db, orgID)
	if err != nil || key == "" {
		return false, err
	}
	for _, k := range actor.Keys() {
		if k == key {
			return true, nil
		}
	}
	return false, nil
}
