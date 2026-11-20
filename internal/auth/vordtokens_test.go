package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/platform/secret"
)

// memRow is an in-memory sessions row.
type memRow struct {
	mu   sync.Mutex
	blob []byte
}

func (m *memRow) update(_ context.Context, _ []byte, fn func([]byte) ([]byte, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := fn(m.blob)
	if err != nil {
		return err
	}
	if next != nil {
		m.blob = next
	}
	return nil
}

type idp struct {
	srv      *httptest.Server
	refresh  atomic.Int32
	rotate   bool
	revoked  atomic.Bool
	lastUsed atomic.Value
}

func newIDP(t *testing.T, rotate bool) *idp {
	t.Helper()
	p := &idp{rotate: rotate}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "refresh_token" || p.revoked.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		p.lastUsed.Store(r.FormValue("refresh_token"))
		n := p.refresh.Add(1)
		w.Header().Set("Content-Type", "application/json")
		body := `{"access_token":"access-` + string(rune('0'+n)) + `","token_type":"Bearer","expires_in":300`
		if p.rotate {
			body += `,"refresh_token":"refresh-` + string(rune('0'+n)) + `"`
		}
		_, _ = w.Write([]byte(body + "}"))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func vordFor(t *testing.T, p *idp, row *memRow, now func() time.Time) *VordTokens {
	t.Helper()
	box, err := secret.New(make32())
	if err != nil {
		t.Fatal(err)
	}
	return &VordTokens{
		box: box, rows: row, now: now,
		oauth: &oauth2.Config{ClientID: "hearth", ClientSecret: "x", Endpoint: oauth2.Endpoint{TokenURL: p.srv.URL}},
	}
}

func make32() []byte {
	b, _ := base64.StdEncoding.DecodeString("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	return b
}

func seed(t *testing.T, v *VordTokens, row *memRow, id []byte, access, refresh string, expiry time.Time) {
	t.Helper()
	blob, err := v.seal(id, access, refresh, expiry)
	if err != nil {
		t.Fatal(err)
	}
	row.blob = blob
}

func TestFreshAccessTokenIsReturnedWithoutRefreshing(t *testing.T) {
	p, row, now := newIDP(t, true), &memRow{}, time.Now()
	v := vordFor(t, p, row, func() time.Time { return now })
	id := []byte("sess")
	seed(t, v, row, id, "access-0", "refresh-0", now.Add(5*time.Minute))

	got, err := v.TokenSource(id).Token(context.Background())
	if err != nil || got != "access-0" || p.refresh.Load() != 0 {
		t.Fatalf("token=%q err=%v refreshes=%d", got, err, p.refresh.Load())
	}
}

func TestTokenIsRefreshedBeforeItExpiresAndStored(t *testing.T) {
	p, row, now := newIDP(t, true), &memRow{}, time.Now()
	v := vordFor(t, p, row, func() time.Time { return now })
	id := []byte("sess")
	// 30s left is inside the 60s skew: refresh now rather than risk a 401.
	seed(t, v, row, id, "access-0", "refresh-0", now.Add(30*time.Second))

	ts := v.TokenSource(id)
	got, err := ts.Token(context.Background())
	if err != nil || got != "access-1" {
		t.Fatalf("token=%q err=%v", got, err)
	}
	if p.lastUsed.Load() != "refresh-0" {
		t.Fatalf("refreshed with %v", p.lastUsed.Load())
	}
	// The rotated pair is what the next call sees; no second refresh.
	got, _ = ts.Token(context.Background())
	if got != "access-1" || p.refresh.Load() != 1 {
		t.Fatalf("token=%q refreshes=%d", got, p.refresh.Load())
	}
	st, _ := v.open(id, row.blob)
	if st.Refresh != "refresh-1" {
		t.Fatalf("rotated refresh token not stored: %+v", st)
	}
}

func TestRefreshTokenIsKeptWhenProviderDoesNotRotate(t *testing.T) {
	p, row, now := newIDP(t, false), &memRow{}, time.Now()
	v := vordFor(t, p, row, func() time.Time { return now })
	id := []byte("sess")
	seed(t, v, row, id, "old", "refresh-0", now.Add(-time.Minute))
	if _, err := v.TokenSource(id).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := v.open(id, row.blob)
	if st.Refresh != "refresh-0" {
		t.Fatalf("lost the refresh token: %+v", st)
	}
}

func TestRevokedVordSessionIsReportedAsExpired(t *testing.T) {
	p, row, now := newIDP(t, true), &memRow{}, time.Now()
	p.revoked.Store(true)
	v := vordFor(t, p, row, func() time.Time { return now })
	id := []byte("sess")
	seed(t, v, row, id, "old", "refresh-0", now.Add(-time.Minute))
	_, err := v.TokenSource(id).Token(context.Background())
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("got %v", err)
	}
}

func TestSessionWithoutTokensSaysSoInsteadOfBorrowingAnother(t *testing.T) {
	p, row := newIDP(t, true), &memRow{}
	v := vordFor(t, p, row, time.Now)
	_, err := v.TokenSource([]byte("sess")).Token(context.Background())
	if !errors.Is(err, ErrNoUserToken) {
		t.Fatalf("got %v", err)
	}
}

func TestSealedTokensAreBoundToTheirSession(t *testing.T) {
	p, row, now := newIDP(t, true), &memRow{}, time.Now()
	v := vordFor(t, p, row, func() time.Time { return now })
	seed(t, v, row, []byte("sess-a"), "a", "r", now.Add(time.Hour))
	if _, err := v.TokenSource([]byte("sess-b")).Token(context.Background()); err == nil {
		t.Fatal("tokens sealed for one session opened under another")
	}
}

func TestInvalidateForcesARefresh(t *testing.T) {
	p, row, now := newIDP(t, true), &memRow{}, time.Now()
	v := vordFor(t, p, row, func() time.Time { return now })
	id := []byte("sess")
	seed(t, v, row, id, "access-0", "refresh-0", now.Add(time.Hour))
	ts := v.TokenSource(id)
	ts.(emberops.Invalidator).Invalidate()
	got, err := ts.Token(context.Background())
	if err != nil || got != "access-1" {
		t.Fatalf("token=%q err=%v", got, err)
	}
}

func TestConcurrentCallersShareOneRefresh(t *testing.T) {
	p, row, now := newIDP(t, true), &memRow{}, time.Now()
	v := vordFor(t, p, row, func() time.Time { return now })
	id := []byte("sess")
	seed(t, v, row, id, "old", "refresh-0", now.Add(-time.Minute))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.TokenSource(id).Token(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if p.refresh.Load() != 1 {
		t.Fatalf("spent the refresh token %d times", p.refresh.Load())
	}
}
