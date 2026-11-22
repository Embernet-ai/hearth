package connect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/files"
)

// Service holds the settings and runs the syncs.
type Service struct {
	db       *pgxpool.Pool
	authz    *authz.Service
	crm      crm.Store
	docs     *docs.Service
	files    *files.Service
	events   EventStore
	off      map[Kind]string
	log      *slog.Logger
	backends map[Provider]Backend
	now      func() time.Time
}

// Deps are what the service is built from.
type Deps struct {
	DB    *pgxpool.Pool
	Authz *authz.Service
	CRM   crm.Store
	Docs  *docs.Service
	Files *files.Service
	Log   *slog.Logger
	// Events is where the calendar sync keeps Hearth's events. Empty means Hearth's own calendar_events table.
	Events EventStore
	// Unavailable names the connectors this installation cannot run and why, for example Files when the documents live in
	// EmberOPS. They show on the page with the reason, cannot be switched on, and are never synced.
	Unavailable map[Kind]string
}

func New(d Deps, backends ...Backend) *Service {
	s := &Service{db: d.DB, authz: d.Authz, crm: d.CRM, docs: d.Docs, files: d.Files, log: d.Log, events: d.Events, off: d.Unavailable,
		backends: map[Provider]Backend{}, now: time.Now}
	if s.events == nil {
		s.events = NewPGEvents(d.DB)
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	for _, b := range backends {
		s.backends[b.Provider()] = b
	}
	return s
}

// Backend returns a provider's backend, or nil when the deployment has not configured it.
func (s *Service) Backend(p Provider) Backend {
	b := s.backends[p]
	if b == nil || !b.Configured() {
		return nil
	}
	return b
}

// -----------------------------------------------------------------------------
// What an administrator has set
// -----------------------------------------------------------------------------

// Setting is one connector's switch.
type Setting struct {
	Provider  Provider
	Kind      Kind
	Enabled   bool
	MeetLinks bool
	ChangedAt time.Time
}

// Tenant is the tenant or domain an administrator connected for a provider.
type Tenant struct {
	Provider    Provider
	Value       string
	ConnectedAt time.Time
}

// TenantFor returns the connected tenant or domain, or "" when none is.
func (s *Service) TenantFor(ctx context.Context, orgID string, p Provider) (string, error) {
	var v string
	err := s.db.QueryRow(ctx,
		`SELECT tenant FROM connector_tenants WHERE org_id = $1 AND provider = $2`, orgID, string(p)).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("connect: read tenant: %w", err)
	}
	return v, nil
}

// NormalizeTenant checks what an administrator typed. A Microsoft tenant is its directory id, a GUID, because that
// is what a sign-in token carries and what is compared. A Google domain is a bare domain name.
func NormalizeTenant(p Provider, v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch p {
	case Microsoft:
		if !isGUID(v) {
			return "", errors.New("the Microsoft 365 tenant is its directory ID, a value like 8f1c2b7e-0000-4a1d-9c3e-5d2f7a6b1c90, found in the Entra admin center under Overview")
		}
	case Google:
		if v == "" || strings.ContainsAny(v, "@/: ") || !strings.Contains(v, ".") {
			return "", errors.New("the Google Workspace domain is the primary domain, like example.com")
		}
	default:
		return "", errors.New("unknown provider")
	}
	return v, nil
}

func isGUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f'):
			return false
		}
	}
	return true
}

// ConnectTenant records the tenant or domain. Changing it ends nothing by itself, but a person's connection that is
// not in the new tenant is refused the next time it is used.
func (s *Service) ConnectTenant(ctx context.Context, orgID, actorID string, p Provider, value string) error {
	v, err := NormalizeTenant(p, value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO connector_tenants (org_id, provider, tenant, connected_by)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (org_id, provider) DO UPDATE
		   SET tenant = EXCLUDED.tenant, connected_by = EXCLUDED.connected_by, connected_at = now()`,
		orgID, string(p), v, actorID)
	if err != nil {
		return fmt.Errorf("connect: save tenant: %w", err)
	}
	return nil
}

// Settings lists every connector's switch for an organisation, off where nothing was ever set.
func (s *Service) Settings(ctx context.Context, orgID string) (map[Provider]map[Kind]Setting, error) {
	out := map[Provider]map[Kind]Setting{}
	for _, p := range Providers {
		out[p] = map[Kind]Setting{}
		for _, k := range Kinds {
			out[p][k] = Setting{Provider: p, Kind: k}
		}
	}
	rows, err := s.db.Query(ctx, `
		SELECT provider, connector, enabled, meet_links, changed_at
		  FROM connector_settings WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("connect: read settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st Setting
		var p, k string
		if err := rows.Scan(&p, &k, &st.Enabled, &st.MeetLinks, &st.ChangedAt); err != nil {
			return nil, err
		}
		st.Provider, st.Kind = Provider(p), Kind(k)
		if m, ok := out[st.Provider]; ok {
			m[st.Kind] = st
		}
	}
	return out, rows.Err()
}

// Enabled reports whether a connector is on.
func (s *Service) Enabled(ctx context.Context, orgID string, p Provider, k Kind) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `
		SELECT enabled FROM connector_settings WHERE org_id=$1 AND provider=$2 AND connector=$3`,
		orgID, string(p), string(k)).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// SetEnabled switches a connector. It cannot be turned on until a tenant or domain is connected and the deployment
// has the provider's client set up, because a connector with nothing to talk to is a switch that does nothing and
// reads as if it worked.
func (s *Service) SetEnabled(ctx context.Context, orgID, actorID string, p Provider, k Kind, on, meetLinks bool) error {
	if on {
		if why := s.UnavailableReason(k); why != "" {
			return fmt.Errorf("%s is not available here: %s", k.Label(), why)
		}
		if s.Backend(p) == nil {
			return fmt.Errorf("%s is not set up on this installation", p.Label())
		}
		t, err := s.TenantFor(ctx, orgID, p)
		if err != nil {
			return err
		}
		if t == "" {
			return errors.New("connect the tenant or domain first")
		}
	}
	if k != Calendar {
		meetLinks = false
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO connector_settings (org_id, provider, connector, enabled, meet_links, changed_by)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (org_id, provider, connector) DO UPDATE
		   SET enabled = EXCLUDED.enabled, meet_links = EXCLUDED.meet_links,
		       changed_by = EXCLUDED.changed_by, changed_at = now()`,
		orgID, string(p), string(k), on, meetLinks, actorID)
	if err != nil {
		return fmt.Errorf("connect: save setting: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Status
// -----------------------------------------------------------------------------

// Status is what one connector last did for one person.
type Status struct {
	PrincipalID string
	Provider    Provider
	Kind        Kind
	LastRunAt   *time.Time
	LastOKAt    *time.Time
	LastError   string
	Items       int
}

// Statuses lists every status in an organisation.
func (s *Service) Statuses(ctx context.Context, orgID string) ([]Status, error) {
	rows, err := s.db.Query(ctx, `
		SELECT principal_id::text, provider, connector, last_run_at, last_ok_at, last_error, items
		  FROM connector_status WHERE org_id = $1 ORDER BY provider, connector, principal_id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("connect: read status: %w", err)
	}
	defer rows.Close()
	var out []Status
	for rows.Next() {
		var st Status
		var p, k string
		if err := rows.Scan(&st.PrincipalID, &p, &k, &st.LastRunAt, &st.LastOKAt, &st.LastError, &st.Items); err != nil {
			return nil, err
		}
		st.Provider, st.Kind = Provider(p), Kind(k)
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Service) record(ctx context.Context, orgID, principalID string, p Provider, k Kind, items int, runErr error) {
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO connector_status (principal_id, org_id, provider, connector, last_run_at, last_ok_at, last_error, items)
		VALUES ($1,$2,$3,$4, now(), CASE WHEN $5 = '' THEN now() END, $5, $6)
		ON CONFLICT (principal_id, provider, connector) DO UPDATE SET
		    last_run_at = now(),
		    last_ok_at  = CASE WHEN $5 = '' THEN now() ELSE connector_status.last_ok_at END,
		    last_error  = $5,
		    items       = CASE WHEN $5 = '' THEN $6 ELSE connector_status.items END`,
		principalID, orgID, string(p), string(k), msg, items)
	if err != nil {
		s.log.Warn("connect: record status", "error", err)
	}
}

// Disconnect ends one person's connection and forgets what it brought in that is theirs alone: the mail previews
// and the links. The events and contacts it created stay, because they are records the person now owns in Hearth.
func (s *Service) Disconnect(ctx context.Context, p Provider, principalID string) error {
	b := s.backends[p]
	if b == nil {
		return errors.New("connect: unknown provider")
	}
	if err := b.Disconnect(ctx, principalID); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM connector_mail WHERE principal_id = $1 AND provider = $2`,
		`DELETE FROM connector_status WHERE principal_id = $1 AND provider = $2`,
		`DELETE FROM connector_links WHERE principal_id = $1 AND provider = $2`,
		`DELETE FROM connector_calendars WHERE principal_id = $1 AND provider = $2`,
		`DELETE FROM connector_chat_hooks WHERE principal_id = $1 AND provider = $2`,
	} {
		if _, err := s.db.Exec(ctx, q, principalID, string(p)); err != nil {
			return fmt.Errorf("connect: clear: %w", err)
		}
	}
	return nil
}

// actor returns the person's access as a set of subjects, so everything the sync writes is written as them and
// is visible to exactly what they could see.
func (s *Service) actor(ctx context.Context, orgID, principalID string) (core.Subjects, error) {
	return s.authz.SubjectsFor(ctx, orgID, principalID)
}

// session opens a person's connection after checking the connector is on and the grant covers it.
func (s *Service) session(ctx context.Context, orgID, principalID string, p Provider, k Kind) (Session, error) {
	b := s.Backend(p)
	if b == nil {
		return nil, ErrDisabled
	}
	on, err := s.Enabled(ctx, orgID, p, k)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, ErrDisabled
	}
	granted, err := b.Granted(ctx, principalID)
	if err != nil {
		return nil, err
	}
	if !HasScopes(granted, b.Scopes(k)) {
		return nil, ErrScope
	}
	return b.Session(ctx, principalID)
}

// Users lists the people with a live connection to a provider, for the scheduled run.
func (s *Service) connected(ctx context.Context, p Provider) ([]Conn, error) {
	b := s.Backend(p)
	if b == nil {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id::text FROM orgs WHERE playground_of IS NULL`)
	if err != nil {
		return nil, err
	}
	var orgs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		orgs = append(orgs, id)
	}
	rows.Close()
	var all []Conn
	for _, o := range orgs {
		cs, err := b.Connections(ctx, o)
		if err != nil {
			return nil, err
		}
		all = append(all, cs...)
	}
	return all, nil
}

// UnavailableReason says why a connector cannot run on this installation, or "" when it can.
func (s *Service) UnavailableReason(k Kind) string { return s.off[k] }
