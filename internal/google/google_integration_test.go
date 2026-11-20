package google

// The OAuth flow and the calendar pull, against a fake Google and a real
// Postgres.
//
// A fake rather than the real thing, because the interesting cases cannot be
// produced on demand against Google: a refresh token that has been revoked, a
// token that expires between two calls, a response with no refresh token in it.
// Those are exactly the paths that are wrong in most implementations of this,
// so they are the ones worth driving.
//
// What the fake cannot prove is that Google behaves as documented. That is
// checked separately, by connecting once for real.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/platform/secret"
)

// fakeGoogle is enough of Google to drive every path here.
type fakeGoogle struct {
	srv *httptest.Server
	// refreshValid flips to false to simulate somebody revoking Hearth in
	// their Google account.
	refreshValid bool
	// withRefresh controls whether the code exchange returns a refresh token.
	withRefresh bool
	exchanges   int
	refreshes   int
	events      []map[string]any
}

func idToken(sub, email string) string {
	body, _ := json.Marshal(map[string]string{"sub": sub, "email": email})
	return "x." + base64.RawURLEncoding.EncodeToString(body) + ".y"
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	f := &fakeGoogle{refreshValid: true, withRefresh: true}
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.FormValue("grant_type") {
		case "authorization_code":
			f.exchanges++
			resp := map[string]any{
				"access_token": "access-1", "expires_in": 3600,
				"scope":    ScopeCalendarRead + " " + ScopeGmailRead,
				"id_token": idToken("google-sub-1", "dana@example.com"),
			}
			if f.withRefresh {
				resp["refresh_token"] = "refresh-1"
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "refresh_token":
			f.refreshes++
			if !f.refreshValid {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "invalid_grant", "error_description": "Token has been expired or revoked.",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fmt.Sprintf("access-refreshed-%d", f.refreshes),
				"expires_in":   3600,
			})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})

	mux.HandleFunc("/calendar/v3/calendars/primary/events", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": f.events})
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type fx struct {
	svc  *Service
	db   *pgxpool.Pool
	fake *fakeGoogle
	org  string
	dana string
}

func setup(t *testing.T) *fx {
	t.Helper()
	dsn := pgtest.Database(t, "hearth_test_google")
	ctx := context.Background()

	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pools.Close)

	key := make([]byte, secret.KeyBytes)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := secret.New(key)
	if err != nil {
		t.Fatalf("box: %v", err)
	}

	fake := newFakeGoogle(t)
	f := &fx{db: pools.Web, fake: fake, org: uuid.NewString(), dana: uuid.NewString()}
	f.svc = New(pools.Web, Config{
		ClientID: "cid", ClientSecret: "csecret",
		RedirectURL: "https://hearth.example.com/integrations/google/callback",
	}, box)
	f.svc.tokenURL = fake.srv.URL + "/token"
	f.svc.apiBase = fake.srv.URL
	f.svc.http = fake.srv.Client()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Google Test')`, f.org, "g-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Dana')`, f.dana, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM google_events WHERE org_id=$1`,
			`DELETE FROM google_connections WHERE org_id=$1`,
			`SELECT audit_purge_org($1)`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func TestTheAuthorisationURLAsksForWhatProducesARefreshToken(t *testing.T) {
	f := setup(t)
	raw := f.svc.AuthURL("state-123", []string{ScopeCalendarRead})

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	// Both of these, together, are what produce a refresh token. Without
	// prompt=consent a second connection returns an access token and no
	// refresh token, and the connection silently dies within the hour.
	if q.Get("access_type") != "offline" {
		t.Error("access_type is not offline, so no refresh token will be issued")
	}
	if q.Get("prompt") != "consent" {
		t.Error("prompt is not consent, so a reconnection returns no refresh token")
	}
	if q.Get("state") != "state-123" {
		t.Error("the state did not survive")
	}
	if !strings.Contains(q.Get("scope"), ScopeCalendarRead) {
		t.Errorf("the scope asked for is %q", q.Get("scope"))
	}
	if q.Get("redirect_uri") != "https://hearth.example.com/integrations/google/callback" {
		t.Errorf("redirect_uri is %q, and Google compares it literally", q.Get("redirect_uri"))
	}
}

func TestConnectingStoresTokensEncrypted(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	conn, err := f.svc.Connect(ctx, f.org, f.dana, "the-code")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if conn.Email != "dana@example.com" || conn.GoogleSub != "google-sub-1" {
		t.Errorf("the connection did not read the account: %+v", conn)
	}
	// Scopes come from what Google returned, not from what was asked for.
	if !conn.Has(ScopeCalendarRead) || !conn.Has(ScopeGmailRead) {
		t.Errorf("scopes are %v", conn.Scopes)
	}

	// The tokens must not be readable in the row.
	var access, refresh []byte
	if err := f.db.QueryRow(ctx,
		`SELECT access_token, refresh_token FROM google_connections WHERE principal_id=$1`,
		f.dana).Scan(&access, &refresh); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if strings.Contains(string(refresh), "refresh-1") || strings.Contains(string(access), "access-1") {
		t.Fatal("a token is stored in the clear, so a leaked backup is a pile of " +
			"working Google credentials")
	}
}

// Without a refresh token the connection dies within the hour and cannot renew
// itself. Storing it anyway produces something that works in testing and fails
// quietly the next morning.
func TestAConnectionWithNoRefreshTokenIsRefused(t *testing.T) {
	f := setup(t)
	f.fake.withRefresh = false

	_, err := f.svc.Connect(context.Background(), f.org, f.dana, "the-code")
	if err == nil {
		t.Fatal("a connection with no refresh token was stored")
	}
	if !strings.Contains(err.Error(), "refresh token") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
	if _, err := f.svc.Get(context.Background(), f.dana); !errors.Is(err, ErrNotConnected) {
		t.Error("a refused connection was stored anyway")
	}
}

func TestAnExpiredAccessTokenIsRefreshedWithoutAnybodyNoticing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.svc.Connect(ctx, f.org, f.dana, "the-code"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Expire it. This is the ordinary case: an access token lasts an hour and
	// this connection will be used for months.
	if _, err := f.db.Exec(ctx,
		`UPDATE google_connections SET expires_at = now() - interval '1 hour' WHERE principal_id=$1`,
		f.dana); err != nil {
		t.Fatalf("expire: %v", err)
	}

	f.fake.events = []map[string]any{{
		"id": "evt-1", "summary": "Stand-up", "status": "confirmed",
		"start": map[string]any{"dateTime": time.Now().Add(time.Hour).Format(time.RFC3339)},
		"end":   map[string]any{"dateTime": time.Now().Add(2 * time.Hour).Format(time.RFC3339)},
	}}
	n, err := f.svc.SyncCalendar(ctx, f.org, f.dana, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("sync after expiry: %v", err)
	}
	if n != 1 {
		t.Errorf("synced %d events, want 1", n)
	}
	if f.fake.refreshes != 1 {
		t.Errorf("the token was refreshed %d times, want once", f.fake.refreshes)
	}

	// And the new token was stored, so the next call does not refresh again.
	if _, err := f.svc.SyncCalendar(ctx, f.org, f.dana, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if f.fake.refreshes != 1 {
		t.Errorf("refreshed %d times across two calls; the refreshed token was not stored", f.fake.refreshes)
	}
}

// Somebody revoking Hearth in their Google account is a different answer from a
// transient failure: no amount of retrying brings it back, and the page has to
// ask them to connect again.
func TestARevokedConnectionSaysReconnectRatherThanFailingForever(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.svc.Connect(ctx, f.org, f.dana, "the-code"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := f.db.Exec(ctx,
		`UPDATE google_connections SET expires_at = now() - interval '1 hour' WHERE principal_id=$1`,
		f.dana); err != nil {
		t.Fatalf("expire: %v", err)
	}
	f.fake.refreshValid = false

	_, err := f.svc.SyncCalendar(ctx, f.org, f.dana, time.Now(), time.Now().Add(24*time.Hour))
	if !errors.Is(err, ErrReconnect) {
		t.Fatalf("a revoked connection gave %v, want ErrReconnect", err)
	}

	// And it is recorded, so the page can say so rather than failing the same
	// way every time somebody opens their calendar.
	conn, err := f.svc.Get(ctx, f.dana)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if conn.Live() {
		t.Error("the connection still reads as live after being revoked")
	}
	if conn.LastError == "" {
		t.Error("nothing was recorded about why it stopped working")
	}
}

func TestAnAllDayEventLandsOnTheRightDay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.svc.Connect(ctx, f.org, f.dana, "the-code"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Google's all-day end is exclusive: a single day on the 15th arrives as
	// start 15th, end 16th. Storing that verbatim makes every one-day event
	// look like two.
	f.fake.events = []map[string]any{{
		"id": "leave-1", "summary": "Leave", "status": "confirmed",
		"start": map[string]any{"date": "2026-09-15"},
		"end":   map[string]any{"date": "2026-09-16"},
	}}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	if _, err := f.svc.SyncCalendar(ctx, f.org, f.dana, from, from.AddDate(0, 1, 0)); err != nil {
		t.Fatalf("sync: %v", err)
	}

	var start, end time.Time
	var allDay bool
	if err := f.db.QueryRow(ctx,
		`SELECT starts_at, ends_at, all_day FROM google_events WHERE google_id='leave-1'`).
		Scan(&start, &end, &allDay); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !allDay {
		t.Error("an all-day event was stored as timed")
	}
	if start.Day() != 15 || end.Day() != 15 {
		t.Errorf("a one-day event spans %s to %s, and should be the 15th to the 15th",
			start.Format("2 Jan"), end.Format("2 Jan"))
	}
}

// A cancelled event has to disappear rather than linger, which means the window
// is replaced rather than merged into.
func TestSomethingDeletedInGoogleDisappearsHere(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.svc.Connect(ctx, f.org, f.dana, "the-code"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	from := time.Now().Add(-time.Hour)
	to := from.Add(48 * time.Hour)

	f.fake.events = []map[string]any{{
		"id": "evt-1", "summary": "Meeting", "status": "confirmed",
		"start": map[string]any{"dateTime": time.Now().Add(time.Hour).Format(time.RFC3339)},
		"end":   map[string]any{"dateTime": time.Now().Add(2 * time.Hour).Format(time.RFC3339)},
	}}
	if _, err := f.svc.SyncCalendar(ctx, f.org, f.dana, from, to); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Gone from Google entirely.
	f.fake.events = nil
	if _, err := f.svc.SyncCalendar(ctx, f.org, f.dana, from, to); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	var n int
	_ = f.db.QueryRow(ctx, `SELECT count(*) FROM google_events WHERE principal_id=$1`, f.dana).Scan(&n)
	if n != 0 {
		t.Errorf("%d events survived a sync that no longer returns them, so a "+
			"cancelled meeting stays on the calendar forever", n)
	}
}

func TestDisconnectingRemovesTheTokensAndTheEvents(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.svc.Connect(ctx, f.org, f.dana, "the-code"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	f.fake.events = []map[string]any{{
		"id": "evt-1", "summary": "Meeting", "status": "confirmed",
		"start": map[string]any{"dateTime": time.Now().Add(time.Hour).Format(time.RFC3339)},
		"end":   map[string]any{"dateTime": time.Now().Add(2 * time.Hour).Format(time.RFC3339)},
	}}
	if _, err := f.svc.SyncCalendar(ctx, f.org, f.dana, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if err := f.svc.Disconnect(ctx, f.dana); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	// The row goes, not just a flag: keeping an encrypted refresh token for a
	// connection somebody ended is keeping a credential nobody asked us to hold.
	var conns, events int
	_ = f.db.QueryRow(ctx, `SELECT count(*) FROM google_connections WHERE principal_id=$1`, f.dana).Scan(&conns)
	_ = f.db.QueryRow(ctx, `SELECT count(*) FROM google_events WHERE principal_id=$1`, f.dana).Scan(&events)
	if conns != 0 {
		t.Error("the connection row survived a disconnect, refresh token and all")
	}
	if events != 0 {
		t.Error("the cached events survived a disconnect")
	}
}

func TestWithoutAnEncryptionKeyNothingIsStored(t *testing.T) {
	f := setup(t)
	f.svc.box = nil

	if _, err := f.svc.Connect(context.Background(), f.org, f.dana, "the-code"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("connecting with no key gave %v, want ErrNoKey. The alternative "+
			"is writing a refresh token to the database in the clear", err)
	}
}
