package stores_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/fake"
	"github.com/Embernet-ai/hearth/internal/emberops/stores"
)

// Hearth principal ids used across the tests.
const (
	ann = "11111111-1111-1111-1111-111111111111"
	bob = "22222222-2222-2222-2222-222222222222"
	org = "00000000-0000-0000-0000-000000000001"
)

type env struct {
	f    *fake.Frappe
	c    *emberops.Client
	base stores.Base
	dir  stores.Directory
	ctx  context.Context // acting as ann
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f := fake.New(map[string]string{"ann-tok": "ann@example.com", "bob-tok": "bob@example.com", "svc-tok": "svc@example.com"})
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.URL)
	c := emberops.New(emberops.Config{BaseURL: u, Service: emberops.StaticToken("svc-tok")})
	f.Put("User", map[string]any{"name": "ann@example.com", "hearth_id": ann, "full_name": "Ann"})
	f.Put("User", map[string]any{"name": "bob@example.com", "hearth_id": bob, "full_name": "Bob"})
	f.Put("Employee", map[string]any{"name": "EMP-ann", "hearth_id": ann, "user_id": "ann@example.com"})
	f.Put("Employee", map[string]any{"name": "EMP-bob", "hearth_id": bob, "user_id": "bob@example.com"})
	return &env{
		f: f, c: c, base: stores.Base{C: c}, dir: stores.Directory{C: c},
		ctx: emberops.WithUser(context.Background(), emberops.StaticToken("ann-tok")),
	}
}

func (e *env) as(tok string) context.Context {
	return emberops.WithUser(context.Background(), emberops.StaticToken(tok))
}
