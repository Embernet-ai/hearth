// Package audit is the record of who did what, kept where it cannot be
// quietly rewritten.
//
// # Why writing is a function and reading is a service
//
// Recording is a free function that takes the transaction the change is
// already running in, so the row about a deletion commits with the deletion or
// not at all. There is nothing to construct, nothing to inject, and nothing to
// forget to wire: adding the record is one line beside the event that is
// already being published there.
//
// The alternative, a recorder passed into every service, is a dependency
// fifteen constructors have to carry and one of them eventually does not. Worse,
// it makes the record a separate write, and a separate write is one that can
// fail on its own, leaving something deleted with no trace of who deleted it.
//
// Reading is a service because it is a page with filters on it, and pages need
// a pool.
//
// # Append only, and the database says so
//
// A trigger refuses UPDATE outright and refuses DELETE unless a session setting
// nothing in this application ever sets is on. That setting exists for exactly
// one thing, removing a whole organisation, through a function that takes an
// org and nothing finer. A test asserts no source file outside a fixture calls
// it, and another test watches the trigger refuse.
//
// This matters more than it looks. "We only ever insert" is a property of
// today's code, and an auditor is right to disbelieve it. A trigger is a
// property of the data.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/platform/httpx"
)

// Actions, as dotted verbs.
//
// Deliberately not an enum in the database: adding a verb would need a
// migration, and the pressure that creates is to log the nearest existing verb
// instead, which is how an audit log stops describing what happened.
const (
	ActionDocumentDelete  = "document.delete"
	ActionDocumentPurge   = "document.purge"
	ActionGrantAdd        = "grant.add"
	ActionGrantRemove     = "grant.remove"
	ActionExport          = "export.download"
	ActionPersonAdd       = "person.add"
	ActionPersonStatus    = "person.status"
	ActionPasswordLink    = "person.password_link"
	ActionPasswordSet     = "person.password_set"
	ActionTeamCreate      = "team.create"
	ActionTeamDelete      = "team.delete"
	ActionTeamRename      = "team.rename"
	ActionTeamMemberAdd   = "team.member.add"
	ActionTeamMemberDrop  = "team.member.remove"
	ActionTokenMint       = "token.mint"
	ActionTokenRevoke     = "token.revoke"
	ActionSessionRevoke   = "session.revoke"
	ActionBackup          = "backup.taken"
	ActionRestore         = "backup.restored"
	ActionTimeApproveBulk = "time.approve.bulk"
	ActionTimeReassign    = "time.reassign"
	ActionAssignBulk      = "assign.bulk"
	ActionTimeAutoApprove = "person.time_auto_approve"
	ActionApproverSet     = "person.approver"
	ActionTimeImport      = "time.import"
	ActionDailyReportFile = "daily_report.file"
	// ActionTaskCloseWithParent is a subtask moved to a new status (done,
	// cancelled, in progress, blocked or to do) because the task it sits under
	// was. One entry per subtask, naming whoever changed the parent; the entry's
	// after holds status and with_parent. The name is from the first version,
	// which only closed, and stays so rows from both read as one list.
	ActionTaskCloseWithParent = "task.close_with_parent"
	ActionSpaceDelete         = "space.delete"
	// ActionTimeRemove is one time entry taken out of every list, by whichever
	// path did it: its author, an admin of its task, the admin bulk remove, or
	// an importer clearing its own rows (time.remove_imported).
	ActionTimeRemove         = "time.remove"
	ActionTimeRemoveImported = "time.remove_imported"

	// ActionChannelArchive is an admin taking a channel out of every list (hearthctl channel-remove).
	ActionChannelArchive = "channel.archive"
)

// Entry is one thing that happened.
type Entry struct {
	OrgID string
	// ActorID is who. Empty means nobody: something the system did on its own,
	// such as a directory pushing a change or a scheduled export. It means
	// nobody, never "we did not record it", and the page says so.
	ActorID    string
	Action     string
	EntityType string
	EntityID   string
	Before     any
	After      any
	// IP is where from, for the questions that are really about a laptop.
	IP string
}

// Execer is the little of pgx a write needs, so this can be handed either a
// transaction or a pool. A transaction is the point; the pool exists for the
// handful of changes that are one statement and have none of their own.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Record writes one row, in whatever transaction the caller is already in.
//
// It returns an error rather than swallowing one. A caller that ignores it has
// decided the change matters more than the record of it, which is sometimes
// true and should always be visible at the call site.
func Record(ctx context.Context, db Execer, e Entry) error {
	if strings.TrimSpace(e.Action) == "" {
		return errors.New("audit: an entry with no action says nothing")
	}
	before, err := encode(e.Before)
	if err != nil {
		return err
	}
	after, err := encode(e.After)
	if err != nil {
		return err
	}
	// The address comes off the request context rather than being threaded
	// through fifteen service signatures. A caller that genuinely knows better,
	// such as a directory pushing a change over a connection of its own, sets
	// it explicitly and this leaves it alone.
	ip := recordedIP(ctx, e)
	_, err = db.Exec(ctx, `
		INSERT INTO audit_log
		    (org_id, actor_id, action, entity_type, entity_id, before, after, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.OrgID, nullUUID(e.ActorID), e.Action, e.EntityType,
		nullUUID(e.EntityID), before, after, nullIP(ip))
	if err != nil {
		return fmt.Errorf("audit: record %s: %w", e.Action, err)
	}
	return nil
}

func encode(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("audit: encode: %w", err)
	}
	return out, nil
}

func nullUUID(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

// nullIP drops anything that is not an address rather than failing the change
// the row describes. A proxy header nobody configured is not a reason to refuse
// a deletion.
func nullIP(v string) any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(v); err == nil {
		v = host
	}
	if net.ParseIP(v) == nil {
		return nil
	}
	return v
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// Row is one entry as a page shows it.
type Row struct {
	ID         int64
	OrgID      string
	ActorID    *string
	ActorName  string
	Action     string
	EntityType string
	EntityID   *string
	Before     string
	After      string
	IP         string
	At         time.Time
}

// Filter narrows the log. Every field is optional and they compose.
type Filter struct {
	Action   string
	ActorID  string
	EntityID string
	Since    *time.Time
	Until    *time.Time
	Limit    int
	// Before is the id to page back from, so a long log can be read to the end
	// without an offset that shifts under new rows.
	Before int64
}

// Service reads the log.
type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// List reads the log, newest first.
func (s *Service) List(ctx context.Context, orgID string, f Filter) ([]Row, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args := []any{orgID, limit}
	where := []string{"a.org_id = $1"}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.Action != "" {
		add("a.action = $%d", f.Action)
	}
	if f.ActorID != "" {
		add("a.actor_id = $%d", f.ActorID)
	}
	if f.EntityID != "" {
		add("a.entity_id = $%d", f.EntityID)
	}
	if f.Since != nil {
		add("a.at >= $%d", *f.Since)
	}
	if f.Until != nil {
		add("a.at < $%d", *f.Until)
	}
	if f.Before > 0 {
		add("a.id < $%d", f.Before)
	}

	rows, err := s.db.Query(ctx, `
		SELECT a.id, a.org_id::text, a.actor_id::text,
		       COALESCE(p.display_name, ''), a.action, a.entity_type,
		       a.entity_id::text, COALESCE(a.before::text, ''),
		       COALESCE(a.after::text, ''), COALESCE(host(a.ip), ''), a.at
		  FROM audit_log a
		  LEFT JOIN principals p ON p.id = a.actor_id
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY a.id DESC
		 LIMIT $2`, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.OrgID, &r.ActorID, &r.ActorName, &r.Action,
			&r.EntityType, &r.EntityID, &r.Before, &r.After, &r.IP, &r.At); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: rows: %w", err)
	}
	return out, nil
}

// Actions are the verbs actually present in this org's log, for the filter.
//
// Read from the data rather than from the constants above, so the control
// offers what happened here rather than everything that could.
func (s *Service) Actions(ctx context.Context, orgID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT action FROM audit_log WHERE org_id = $1 ORDER BY 1`, orgID)
	if err != nil {
		return nil, fmt.Errorf("audit: actions: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("audit: scan action: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Count is how many entries this org has, for the page to say so.
func (s *Service) Count(ctx context.Context, orgID string) (int64, error) {
	var n int64
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE org_id = $1`, orgID).Scan(&n); err != nil {
		return 0, fmt.Errorf("audit: count: %w", err)
	}
	return n, nil
}

// Tx is the transaction shape Record accepts, spelled out so a caller can see
// that a pgx.Tx satisfies it.
var _ Execer = (pgx.Tx)(nil)
var _ Execer = (*pgxpool.Pool)(nil)

// recordedIP is the address an audit row keeps: the one the entry names, else the
// request's client, which is the client the proxies saw when the deployment lists
// them and the connection is one of theirs, and the socket address otherwise.
func recordedIP(ctx context.Context, e Entry) string {
	if e.IP != "" {
		return e.IP
	}
	return httpx.ClientIP(ctx)
}
