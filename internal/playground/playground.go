// Package playground is the personal tutorial: Settings -> Tutorial.
//
// Each person gets an organisation of their own ("Ember Demo Co"), full of
// made-up departments, people and work, plus lesson tasks whose checklists
// walk them through Hearth by doing it. It is theirs until they reset it.
//
// # Why an organisation
//
// The wall between organisations is already on every row and every query, so
// nothing done in a playground can show up in real search, reports, billing,
// time or notifications. A flagged corner of the real organisation would need
// a new exclusion in each of those, and the one that gets forgotten is the
// one that puts made-up hours on a real invoice.
//
// # The rules that keep it a playground
//
//   - The playground self is a separate principal with NO password and NO
//     sign-in identity (local_credentials, user_identities). No login path can
//     ever land in a playground; the only way in is the switch, from a real
//     session (auth.Service.Switch, sessions.impersonated_by).
//   - It owns its playground (it is the first user principal, which is what
//     auth.OwnerID means) and holds every grantable capability, but NEVER
//     super_admin: a super admin's entity checks do not look at the
//     organisation (authz.LevelFor), so one in a playground could write to a
//     real document by its id.
//   - Anything that reaches outside -- mail, webhooks, tokens, Google, new
//     real accounts -- is refused in a playground (web.playgroundGuard), and
//     the boot-time recurring work skips playgrounds (orgs.playground_of).
//   - Purge refuses any organisation that is not a playground.
//
// The content lives in seed.go, built through the same service calls a person's
// requests go through, so it follows the schema rather than fighting it.
package playground

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/timelog"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

// Name is what a playground organisation is called.
const Name = "Ember Demo Co"

// ErrNotPlayground is what Purge says about a real organisation.
var ErrNotPlayground = errors.New("playground: that organisation is not a playground")

// Service builds, finds and resets playgrounds.
type Service struct {
	db       *pgxpool.Pool
	authz    *authz.Service
	plant    plant.Store
	docs     docs.Store
	channels *channels.Service
	time     timelog.Store
	timeoff  timeoff.Store

	// is caches IsPlayground per organisation. An organisation never changes
	// between being a playground and not, so nothing ever invalidates it.
	is sync.Map

	// building serialises Enter per person, so a double click cannot build
	// two playgrounds (the unique index would refuse the second anyway).
	building sync.Map
}

func New(db *pgxpool.Pool, az *authz.Service, pl plant.Store, d docs.Store,
	ch *channels.Service, tl timelog.Store, to timeoff.Store) *Service {
	return &Service{db: db, authz: az, plant: pl, docs: d, channels: ch, time: tl, timeoff: to}
}

// IsPlayground reports whether an organisation is somebody's playground.
// Errors read as "yes": the guard that asks this refuses things, and refusing
// by mistake is the safe way to be wrong.
func (s *Service) IsPlayground(ctx context.Context, orgID string) bool {
	if v, ok := s.is.Load(orgID); ok {
		return v.(bool)
	}
	var is bool
	if err := s.db.QueryRow(ctx,
		`SELECT playground_of IS NOT NULL FROM orgs WHERE id = $1`, orgID).Scan(&is); err != nil {
		return true
	}
	s.is.Store(orgID, is)
	return is
}

// Enter returns the person's playground -- built the first time -- and the
// playground self to switch to. realName is what the playground self is
// called, so the person sees their own name on what they do in there.
func (s *Service) Enter(ctx context.Context, realPrincipalID, realName string) (orgID, selfID string, err error) {
	lock, _ := s.building.LoadOrStore(realPrincipalID, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	orgID, selfID, err = s.find(ctx, realPrincipalID)
	if errors.Is(err, pgx.ErrNoRows) {
		orgID, selfID, err = s.build(ctx, realPrincipalID, realName)
	}
	if err != nil {
		return "", "", err
	}
	if _, err := s.db.Exec(ctx, `UPDATE orgs SET entered_at = now() WHERE id = $1`, orgID); err != nil {
		return "", "", fmt.Errorf("playground: touch: %w", err)
	}
	return orgID, selfID, nil
}

// Reset throws the person's playground away; the next Enter builds a fresh one.
func (s *Service) Reset(ctx context.Context, realPrincipalID string) error {
	orgID, _, err := s.find(ctx, realPrincipalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.Purge(ctx, orgID)
}

// find is the playground self: the first user principal of the person's
// playground, the same "first is the owner" rule auth.OwnerID applies.
func (s *Service) find(ctx context.Context, realPrincipalID string) (orgID, selfID string, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT o.id::text, p.id::text
		FROM orgs o
		JOIN principals p ON p.org_id = o.id AND p.kind = 'user'
		WHERE o.playground_of = $1
		ORDER BY p.created_at, p.id
		LIMIT 1`, realPrincipalID).Scan(&orgID, &selfID)
	return orgID, selfID, err
}

// build makes the organisation and its people in one transaction, then fills
// it through the ordinary services as the playground self. A failure while
// filling purges the half-built playground, so the next try starts clean.
func (s *Service) build(ctx context.Context, realPrincipalID, realName string) (string, string, error) {
	orgID, selfID := uuid.NewString(), uuid.NewString()
	people := map[string]string{"you": selfID}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", "", fmt.Errorf("playground: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO orgs (id, slug, name, playground_of, entered_at)
		VALUES ($1, $2, $3, $4, now())`,
		orgID, "playground-"+strings.ReplaceAll(orgID, "-", "")[:16], Name, realPrincipalID); err != nil {
		return "", "", fmt.Errorf("playground: create organisation: %w", err)
	}
	// The playground self first, and strictly earliest: whoever was created
	// first in an organisation is its owner. No credentials, no identity.
	name := strings.TrimSpace(realName)
	if name == "" {
		name = "You"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO principals (id, org_id, kind, display_name, created_at)
		VALUES ($1, $2, 'user', $3, now() - interval '1 minute')`, selfID, orgID, name); err != nil {
		return "", "", fmt.Errorf("playground: create you: %w", err)
	}
	// Every grantable capability, so every lesson works for anyone -- and
	// deliberately not super_admin. See the package comment.
	for _, c := range authz.Grantable {
		if _, err := tx.Exec(ctx, `
			INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
			VALUES ($1, $2, $3, $2)`, orgID, selfID, string(c)); err != nil {
			return "", "", fmt.Errorf("playground: grant %s: %w", c, err)
		}
	}
	// The made-up colleagues: names only, nothing to sign in with, no email,
	// so nothing is ever sent to them either.
	for i, p := range seedPeople {
		id := uuid.NewString()
		people[p.Key] = id
		if _, err := tx.Exec(ctx, `
			INSERT INTO principals (id, org_id, kind, display_name, created_at)
			VALUES ($1, $2, 'user', $3, now() + make_interval(secs => $4))`,
			id, orgID, p.Name, i+1); err != nil {
			return "", "", fmt.Errorf("playground: create %s: %w", p.Name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", fmt.Errorf("playground: commit: %w", err)
	}
	s.is.Store(orgID, true)

	if err := s.fill(ctx, orgID, people, time.Now()); err != nil {
		if perr := s.Purge(context.WithoutCancel(ctx), orgID); perr != nil {
			return "", "", fmt.Errorf("%w (and the half-built playground could not be removed: %v)", err, perr)
		}
		return "", "", err
	}
	return orgID, selfID, nil
}
