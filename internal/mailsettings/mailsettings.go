// Package mailsettings holds each organisation's own outbound email relay, and
// the mailer that sends notifications through it.
//
// Sending used to be one relay for the whole deployment, read from the
// environment. That is right for a single estate and wrong for a tenant who
// wants their mail to leave under their own name and their own provider. The
// settings live per org; the mailer reads them at send time. With none set, or
// enabled off, email is simply not sent and the in-app inbox carries on.
//
// The relay password is the one credential that cannot be hashed -- it is
// handed to the relay verbatim -- so it is sealed with the deployment's
// encryption key (internal/platform/secret). No key, no stored password.
package mailsettings

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/platform/mail"
	"github.com/Embernet-ai/hearth/internal/platform/secret"
)

// ErrInvalid is a setting that cannot be saved as given.
var ErrInvalid = errors.New("mailsettings: invalid")

// sealContext ties a sealed password to this use, so a value sealed for one
// purpose cannot be opened as another.
const sealContext = "org-mail-password"

type Service struct {
	db  *pgxpool.Pool
	box *secret.Box // nil when the deployment configured no encryption key
}

func New(db *pgxpool.Pool, box *secret.Box) *Service { return &Service{db: db, box: box} }

// CanStorePassword reports whether a relay password can be saved at all, which
// needs an encryption key. The page tells the admin when it cannot.
func (s *Service) CanStorePassword() bool { return s.box != nil }

// Settings is what an admin sees. The password is never read back -- only
// whether one is stored.
type Settings struct {
	Enabled     bool
	Host        string
	Port        int
	From        string
	FromName    string
	Username    string
	HasPassword bool
	StartTLS    bool
}

// Get returns an org's settings, or a sensible blank default if none are saved.
func (s *Service) Get(ctx context.Context, orgID string) (*Settings, error) {
	out := &Settings{Port: 587, StartTLS: true}
	var sealed string
	err := s.db.QueryRow(ctx, `
		SELECT enabled, host, port, from_address, from_name, username, password_sealed, starttls
		  FROM org_mail_settings WHERE org_id = $1`, orgID).
		Scan(&out.Enabled, &out.Host, &out.Port, &out.From, &out.FromName, &out.Username, &sealed, &out.StartTLS)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mailsettings: get: %w", err)
	}
	out.HasPassword = sealed != ""
	return out, nil
}

// SaveParams is a change from the settings page. An empty Password leaves the
// stored one untouched; ClearPassword removes it.
type SaveParams struct {
	Enabled       bool
	Host          string
	Port          int
	From          string
	FromName      string
	Username      string
	Password      string
	ClearPassword bool
	StartTLS      bool
}

// Save writes an org's settings. Enabling requires a host and a from address,
// because an enabled relay with neither sends nothing and looks broken.
func (s *Service) Save(ctx context.Context, orgID, actorID string, p SaveParams) error {
	p.Host = strings.TrimSpace(p.Host)
	p.From = strings.TrimSpace(p.From)
	p.Username = strings.TrimSpace(p.Username)
	if p.Port == 0 {
		p.Port = 587
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("%w: the port must be between 1 and 65535", ErrInvalid)
	}
	if p.Enabled && (p.Host == "" || p.From == "") {
		return fmt.Errorf("%w: a host and a from address are needed to turn email on", ErrInvalid)
	}
	if !strings.Contains(p.Host, ":") && strings.Contains(p.Host, " ") {
		return fmt.Errorf("%w: the host should be a name like smtp.example.com", ErrInvalid)
	}

	// Work out the new sealed password: keep, clear, or replace.
	var sealedExpr string
	args := []any{orgID, p.Enabled, p.Host, p.Port, p.From, p.FromName, p.Username, p.StartTLS, actorID}
	switch {
	case p.ClearPassword:
		sealedExpr = "''"
	case p.Password != "":
		if s.box == nil {
			return fmt.Errorf("%w: this deployment has no encryption key, so a relay password cannot be stored", ErrInvalid)
		}
		sealed, err := s.box.SealString(p.Password, sealContext)
		if err != nil {
			return fmt.Errorf("mailsettings: seal: %w", err)
		}
		args = append(args, base64.StdEncoding.EncodeToString(sealed))
		sealedExpr = "$10"
	default:
		sealedExpr = "COALESCE((SELECT password_sealed FROM org_mail_settings WHERE org_id = $1), '')"
	}

	_, err := s.db.Exec(ctx, `
		INSERT INTO org_mail_settings
		    (org_id, enabled, host, port, from_address, from_name, username, starttls, password_sealed, updated_at, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8, `+sealedExpr+`, now(), $9)
		ON CONFLICT (org_id) DO UPDATE SET
		    enabled = EXCLUDED.enabled, host = EXCLUDED.host, port = EXCLUDED.port,
		    from_address = EXCLUDED.from_address, from_name = EXCLUDED.from_name,
		    username = EXCLUDED.username, starttls = EXCLUDED.starttls,
		    password_sealed = EXCLUDED.password_sealed, updated_at = now(), updated_by = EXCLUDED.updated_by`,
		args...)
	if err != nil {
		return fmt.Errorf("mailsettings: save: %w", err)
	}
	return nil
}

// configFor builds the relay config to send through, and whether email is on
// for this org. The password is opened here and nowhere else.
func (s *Service) configFor(ctx context.Context, orgID string) (mail.Config, bool, error) {
	var (
		enabled, starttls                      bool
		host, from, fromName, username, sealed string
		port                                   int
	)
	err := s.db.QueryRow(ctx, `
		SELECT enabled, host, port, from_address, from_name, username, password_sealed, starttls
		  FROM org_mail_settings WHERE org_id = $1`, orgID).
		Scan(&enabled, &host, &port, &from, &fromName, &username, &sealed, &starttls)
	if errors.Is(err, pgx.ErrNoRows) {
		return mail.Config{}, false, nil
	}
	if err != nil {
		return mail.Config{}, false, fmt.Errorf("mailsettings: config: %w", err)
	}
	if !enabled || host == "" || from == "" {
		return mail.Config{}, false, nil
	}
	password := ""
	if sealed != "" && s.box != nil {
		if raw, err := base64.StdEncoding.DecodeString(sealed); err == nil {
			if pw, err := s.box.OpenString(raw, sealContext); err == nil {
				password = pw
			}
		}
	}
	fromHeader := from
	if fromName != "" {
		fromHeader = fromName + " <" + from + ">"
	}
	return mail.Config{
		Host:     host + ":" + strconv.Itoa(port),
		From:     fromHeader,
		Username: username,
		Password: password,
		StartTLS: starttls,
	}, true, nil
}

// Mailer sends through each org's own relay, and is what notify.Fanout holds as
// its notify.Mailer. An org with no relay of its own, or with email turned off,
// falls back to the deployment-wide env relay if one is set; with neither, mail
// is simply not sent and Configured reports false, so the fanout skips email
// and the in-app inbox carries the notification alone.
type Mailer struct {
	svc      *Service
	fallback *mail.Sender // deployment-wide env relay; may be nil or unconfigured
}

// NewMailer wraps the settings service. fallback may be nil.
func NewMailer(svc *Service, fallback *mail.Sender) *Mailer {
	return &Mailer{svc: svc, fallback: fallback}
}

// sender returns the relay to use for an org: its own if enabled and complete,
// otherwise the env fallback, otherwise nil.
func (m *Mailer) sender(ctx context.Context, orgID string) *mail.Sender {
	if m == nil {
		return nil
	}
	if cfg, on, err := m.svc.configFor(ctx, orgID); err == nil && on {
		return mail.New(cfg)
	}
	if m.fallback != nil && m.fallback.Configured() {
		return m.fallback
	}
	return nil
}

// Configured reports whether this org has anywhere to send to.
func (m *Mailer) Configured(ctx context.Context, orgID string) bool {
	return m.sender(ctx, orgID).Configured()
}

// Send delivers one message on behalf of an org. With nowhere to send, it is a
// no-op rather than an error: a tenant who has not set up mail should see the
// inbox work, not a stream of failed-send errors in the job queue.
func (m *Mailer) Send(ctx context.Context, orgID, to, subject, body string) error {
	s := m.sender(ctx, orgID)
	if !s.Configured() {
		return nil
	}
	return s.Send(ctx, to, subject, body)
}

// SendTest sends a one-off check message straight through the org's own relay,
// ignoring the env fallback. The point of the test is to prove the org's own
// settings work, so falling back to a different relay would make a broken
// configuration look fine. It reports a plain error when the org has no relay.
func (m *Mailer) SendTest(ctx context.Context, orgID, to string) error {
	cfg, on, err := m.svc.configFor(ctx, orgID)
	if err != nil {
		return err
	}
	if !on {
		return fmt.Errorf("%w: email is not turned on for this organisation", ErrInvalid)
	}
	return mail.New(cfg).Send(ctx, to,
		"Hearth test email",
		"This is a test from Hearth.\n\nIf you are reading it, your email settings work.\n")
}
