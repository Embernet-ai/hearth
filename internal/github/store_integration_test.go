package github_test

// The GitHub connection against a real Postgres, with a fake GitHub: connecting keeps the token sealed and never in the clear,
// choosing is limited and checked, one person's choice is not another's, and disconnecting deletes the token.

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/github"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type sealer struct{}

func (sealer) SealString(p, c string) ([]byte, error) {
	return []byte("sealed:" + c + ":" + base64.StdEncoding.EncodeToString([]byte(p))), nil
}
func (sealer) OpenString(b []byte, c string) (string, error) {
	pre := []byte("sealed:" + c + ":")
	if !bytes.HasPrefix(b, pre) {
		return "", http.ErrNoCookie
	}
	d, err := base64.StdEncoding.DecodeString(string(bytes.TrimPrefix(b, pre)))
	return string(d), err
}

func TestConnectionIsSealedPerPersonAndForgotten(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	if err := pg.Migrate(ctx, pools, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))); err != nil {
		t.Fatal(err)
	}
	org, ann, bob := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, a ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, a...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'GH Test')`, org, "gh-"+org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$3,'user','Ann'), ($2,$3,'user','Bob')`, ann, bob, org)
	t.Cleanup(func() {
		_, _ = pools.Web.Exec(ctx, `DELETE FROM github_connections WHERE org_id=$1`, org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM principals WHERE org_id=$1`, org)
		_, _ = pools.Web.Exec(ctx, `DELETE FROM orgs WHERE id=$1`, org)
	})

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("GitHub was sent a %s: the connection only reads", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer ghp_secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"login":"octo"}`))
	}))
	t.Cleanup(gh.Close)
	svc := github.NewService(pools.Web, sealer{}, &github.Client{Base: gh.URL, HC: gh.Client()})

	if _, err := svc.Connect(ctx, org, ann, "nope"); err == nil {
		t.Fatal("a refused token was kept")
	}
	if _, err := svc.Get(ctx, ann); err != github.ErrNotConnected {
		t.Fatalf("after a refused token: %v", err)
	}
	conn, err := svc.Connect(ctx, org, ann, " ghp_secret ")
	if err != nil || conn.Login != "octo" {
		t.Fatalf("connect: %+v %v", conn, err)
	}
	var stored []byte
	if err := pools.Web.QueryRow(ctx, `SELECT token FROM github_connections WHERE principal_id=$1`, ann).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("ghp_secret")) {
		t.Fatal("the token is in the database in the clear")
	}

	// Choosing: checked names, no duplicates, at most twelve, and only the person's own.
	if err := svc.Choose(ctx, ann, []string{"org/app", "ORG/app", "org/site"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := svc.Get(ctx, ann); len(c.Repos) != 2 {
		t.Errorf("repos = %v, want two (a name twice counts once)", c.Repos)
	}
	if err := svc.Choose(ctx, ann, []string{"not a repo"}); err == nil {
		t.Error("a bad name was accepted")
	}
	var many []string
	for i := 0; i < 13; i++ {
		many = append(many, "org/r"+string(rune('a'+i)))
	}
	if err := svc.Choose(ctx, ann, many); err == nil {
		t.Error("thirteen repositories were accepted")
	}
	if err := svc.Choose(ctx, bob, []string{"org/app"}); err != github.ErrNotConnected {
		t.Errorf("bob chose repositories with no connection: %v", err)
	}

	if err := svc.Disconnect(ctx, ann); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pools.Web.QueryRow(ctx, `SELECT count(*) FROM github_connections WHERE principal_id=$1`, ann).Scan(&n)
	if n != 0 {
		t.Errorf("the connection and its token are still there after disconnecting")
	}
}
