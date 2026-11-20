package mailsettings

// Per-org mail settings, against a real Postgres.
//
// The claims worth a database: a saved password comes back decryptable but is
// never returned as plaintext; an empty password on resave leaves the stored
// one alone while ClearPassword removes it; enabling without a host or from is
// refused; and the mailer falls back to the deployment relay for an org that
// has set up none of its own.

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/platform/mail"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/secret"
)

type fx struct {
	svc   *Service
	pools *pg.Pools
	org   string
	actor string
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := pg.Migrate(ctx, pools, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	key := make([]byte, secret.KeyBytes)
	for i := range key {
		key[i] = byte(i + 1)
	}
	box, err := secret.New(key)
	if err != nil {
		t.Fatalf("box: %v", err)
	}

	f := &fx{
		svc:   New(pools.Web, box),
		pools: pools,
		org:   uuid.NewString(),
		actor: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Mail Settings Test')`, f.org, "ms-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Admin')`, f.actor, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM org_mail_settings WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

// A saved relay comes back ready to send, with the password decrypted, but the
// settings an admin reads never carry the plaintext.
func TestSaveAndConfigRoundTrip(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	err := f.svc.Save(ctx, f.org, f.actor, SaveParams{
		Enabled: true, Host: "smtp.example.com", Port: 2525,
		From: "hearth@example.com", FromName: "Acme Hearth",
		Username: "acme", Password: "s3cret", StartTLS: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := f.svc.Get(ctx, f.org)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Enabled || got.Host != "smtp.example.com" || got.Port != 2525 {
		t.Errorf("settings not stored: %+v", got)
	}
	if !got.HasPassword {
		t.Errorf("HasPassword false after saving one")
	}

	cfg, on, err := f.svc.configFor(ctx, f.org)
	if err != nil || !on {
		t.Fatalf("configFor: on=%v err=%v", on, err)
	}
	if cfg.Host != "smtp.example.com:2525" {
		t.Errorf("host:port wrong: %q", cfg.Host)
	}
	if cfg.From != "Acme Hearth <hearth@example.com>" {
		t.Errorf("from header wrong: %q", cfg.From)
	}
	if cfg.Password != "s3cret" {
		t.Errorf("password did not decrypt: %q", cfg.Password)
	}
}

// An empty password on resave keeps the stored one; ClearPassword removes it.
func TestPasswordIsKeptUnlessChanged(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	must := func(p SaveParams) {
		t.Helper()
		if err := f.svc.Save(ctx, f.org, f.actor, p); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	must(SaveParams{Enabled: true, Host: "h", From: "f@x", Password: "first", Port: 587})

	// Resave with no password: the stored one must survive.
	must(SaveParams{Enabled: true, Host: "h2", From: "f@x", Port: 587})
	cfg, _, err := f.svc.configFor(ctx, f.org)
	if err != nil {
		t.Fatalf("configFor: %v", err)
	}
	if cfg.Password != "first" {
		t.Errorf("password not preserved on resave: %q", cfg.Password)
	}
	if cfg.Host != "h2:587" {
		t.Errorf("host not updated: %q", cfg.Host)
	}

	// Now clear it.
	must(SaveParams{Enabled: true, Host: "h2", From: "f@x", Port: 587, ClearPassword: true})
	got, _ := f.svc.Get(ctx, f.org)
	if got.HasPassword {
		t.Errorf("password still present after ClearPassword")
	}
}

// Enabling with nothing to send through, or a nonsense port, is refused rather
// than saved as a relay that silently sends nothing.
func TestInvalidSettingsRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	cases := []SaveParams{
		{Enabled: true, Host: "", From: "f@x", Port: 587},
		{Enabled: true, Host: "h", From: "", Port: 587},
		{Enabled: true, Host: "h", From: "f@x", Port: 70000},
	}
	for i, c := range cases {
		if err := f.svc.Save(ctx, f.org, f.actor, c); err == nil {
			t.Errorf("case %d: expected refusal, got nil", i)
		}
	}
}

// With no encryption key, a relay password cannot be stored and the attempt is
// refused rather than written in the clear.
func TestNoKeyRefusesPassword(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	nokey := New(f.pools.Web, nil)

	if nokey.CanStorePassword() {
		t.Errorf("CanStorePassword true with no box")
	}
	err := nokey.Save(ctx, f.org, f.actor, SaveParams{
		Enabled: true, Host: "h", From: "f@x", Port: 587, Password: "nope",
	})
	if err == nil {
		t.Errorf("storing a password with no key should be refused")
	}
	// But a relay with no password is fine without a key.
	if err := nokey.Save(ctx, f.org, f.actor, SaveParams{
		Enabled: true, Host: "h", From: "f@x", Port: 587,
	}); err != nil {
		t.Errorf("passwordless save refused with no key: %v", err)
	}
}

// An org with no settings of its own falls back to the deployment relay, and
// the mailer reports itself configured only when one of the two can send.
func TestMailerFallsBackToEnvRelay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	relay := mail.New(mail.Config{Host: "relay:25", From: "ops@x"})
	m := NewMailer(f.svc, relay)
	if !m.Configured(ctx, f.org) {
		t.Errorf("mailer should fall back to the configured env relay")
	}

	// With no org settings and no fallback, there is nowhere to send.
	bare := NewMailer(f.svc, nil)
	if bare.Configured(ctx, f.org) {
		t.Errorf("mailer with no org settings and no fallback claims configured")
	}
}
