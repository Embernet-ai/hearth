package auth

// The Microsoft Entra and Google Workspace presets on the sign-in, and groups becoming capabilities.
//
// A stand-in identity provider with a real signing key, discovery document, token endpoint and (for Entra's
// group overage) a Graph endpoint, so the real go-oidc verifier runs: signature, audience, expiry, nonce. What a
// stand-in cannot show is that Microsoft and Google emit exactly these claims in practice. The claim names here
// are the ones their documentation lists (Entra: tid, oid, preferred_username, groups, roles, _claim_names;
// Google: hd, email_verified), and the live check is a real sign-in.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

type fakeIdP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	mu        sync.Mutex
	nonce     string
	claims    map[string]any
	templated bool
	graphCode int
	graphIDs  []string
}

func newIdP(t *testing.T, templated bool) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIdP{key: key, templated: templated, graphCode: 200}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
			issuer := p.srv.URL + strings.TrimSuffix(r.URL.Path, "/.well-known/openid-configuration")
			if templated {
				issuer = p.srv.URL + "/{tenantid}/v2.0"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
				"jwks_uri": p.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
				"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			})
		case r.URL.Path == "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
		case r.URL.Path == "/token":
			p.mu.Lock()
			c := map[string]any{}
			for k, v := range p.claims {
				c[k] = v
			}
			c["nonce"] = p.nonce
			p.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "graph-at", "token_type": "Bearer", "expires_in": 3600, "id_token": p.sign(t, c)})
		case r.URL.Path == "/v1.0/me/getMemberObjects":
			if r.Header.Get("Authorization") != "Bearer graph-at" {
				w.WriteHeader(401)
				return
			}
			if p.graphCode != 200 {
				w.WriteHeader(p.graphCode)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": p.graphIDs})
		default:
			w.WriteHeader(404)
		}
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeIdP) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	body, _ := json.Marshal(claims)
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "k1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := s.Sign(body)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := obj.CompactSerialize()
	return out
}

// claims builds an id token payload with the standard parts filled in.
func (p *fakeIdP) base(iss, aud, sub string, extra map[string]any) map[string]any {
	c := map[string]any{"iss": iss, "aud": aud, "sub": sub, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Add(-time.Minute).Unix()}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

type oidcFx struct {
	t      *testing.T
	db     *pgxpool.Pool
	svc    *Service
	org    string
	owner  string
	idp    *fakeIdP
	client string
}

func newOIDCFx(t *testing.T, templated bool) *oidcFx {
	t.Helper()
	dsn := pgtest.Database(t, "hearth_test_auth_oidc")
	ctx := context.Background()
	pools, err := pg.Open(ctx, pg.DefaultOptions(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pools.Close)
	for _, q := range []string{`DELETE FROM idp_capability_grants`, `DELETE FROM capability_grants`, `DELETE FROM user_identities`,
		`DELETE FROM local_credentials`, `DELETE FROM principals`, `DELETE FROM orgs`} {
		_, _ = pools.Web.Exec(ctx, q)
	}
	f := &oidcFx{t: t, db: pools.Web, idp: newIdP(t, templated), client: "hearth-client"}
	f.svc = New(pools.Web, authz.New(pools.Web), false)
	owner, err := f.svc.Setup(ctx, "Org", "Owner", "owner@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	f.owner = owner
	if err := pools.Web.QueryRow(ctx, `SELECT id::text FROM orgs LIMIT 1`).Scan(&f.org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pools.Web.Exec(c, `SELECT audit_purge_org($1)`, f.org)
		for _, q := range []string{`DELETE FROM idp_capability_grants`, `DELETE FROM capability_grants`, `DELETE FROM user_identities`,
			`DELETE FROM local_credentials`, `DELETE FROM principals`, `DELETE FROM orgs`} {
			_, _ = pools.Web.Exec(c, q)
		}
	})
	return f
}

func (f *oidcFx) rp(cfg OIDCConfig) *OIDC {
	f.t.Helper()
	cfg.ClientID, cfg.ClientSecret, cfg.RedirectURL = f.client, "secret", "https://hearth.example.com/auth/callback"
	cfg.authorityBase = f.idp.srv.URL
	cfg.graphBase = f.idp.srv.URL + "/v1.0"
	o, err := NewOIDC(context.Background(), f.svc, cfg, false)
	if err != nil {
		f.t.Fatalf("NewOIDC: %v", err)
	}
	return o
}

// signIn runs the whole flow, Start then Callback, as a browser would.
func (f *oidcFx) signIn(o *OIDC, claims map[string]any) error {
	f.t.Helper()
	rec := httptest.NewRecorder()
	o.Start(rec, httptest.NewRequest("GET", "/auth/start", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	cookie := rec.Result().Cookies()[0]
	parts := strings.Split(cookie.Value, ".")
	f.idp.mu.Lock()
	f.idp.nonce, f.idp.claims = parts[1], claims
	f.idp.mu.Unlock()

	req := httptest.NewRequest("GET", "/auth/callback?code=c&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	req.AddCookie(cookie)
	return o.Callback(context.Background(), httptest.NewRecorder(), req)
}

func (f *oidcFx) caps(sub string) map[string]bool {
	f.t.Helper()
	var pid string
	if err := f.db.QueryRow(context.Background(), `SELECT principal_id::text FROM user_identities WHERE subject=$1`, sub).Scan(&pid); err != nil {
		f.t.Fatalf("no identity for %s: %v", sub, err)
	}
	return f.capsOf(pid)
}

func (f *oidcFx) capsOf(pid string) map[string]bool {
	rows, _ := f.db.Query(context.Background(), `SELECT capability FROM capability_grants WHERE principal_id=$1`, pid)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		out[c] = true
	}
	return out
}

const tid = "8f1c2b7e-0000-4a1d-9c3e-5d2f7a6b1c90"

func (f *oidcFx) entraIssuer(t string) string { return f.idp.srv.URL + "/" + t + "/v2.0" }

// ---- Microsoft ------------------------------------------------------------

func TestEntraSingleTenantSignsInOnlyFromThatTenantAndKeysOnTheObjectID(t *testing.T) {
	f := newOIDCFx(t, false)
	o := f.rp(OIDCConfig{Provider: "microsoft", Tenant: tid, AllowedDomains: []string{"contoso.test"}})

	good := f.idp.base(f.entraIssuer(tid), f.client, "pairwise-sub", map[string]any{
		"tid": tid, "oid": "object-1", "preferred_username": "Dana@Contoso.test", "name": "Dana"})
	if err := f.signIn(o, good); err != nil {
		t.Fatalf("a good Entra sign-in failed: %v", err)
	}
	var subj, email string
	if err := f.db.QueryRow(context.Background(), `SELECT subject, email FROM user_identities WHERE issuer=$1`, f.entraIssuer(tid)).Scan(&subj, &email); err != nil {
		t.Fatal(err)
	}
	if subj != "object-1" {
		t.Errorf("linked on %q, want the object id (the pairwise sub is different in every app)", subj)
	}
	if email != "dana@contoso.test" {
		t.Errorf("email %q: Entra sends no email claim by default, so preferred_username stands in and the domain check uses it", email)
	}

	// The same token with the wrong tenant claim is refused, though it is signed and addressed to us.
	bad := f.idp.base(f.entraIssuer(tid), f.client, "x", map[string]any{"tid": "11111111-2222-3333-4444-555555555555", "oid": "o2", "preferred_username": "eve@contoso.test"})
	if err := f.signIn(o, bad); err == nil {
		t.Fatal("a token whose tid is not the configured tenant signed in")
	}
	// And a domain outside the allowed list is refused even from the right tenant (a guest).
	guest := f.idp.base(f.entraIssuer(tid), f.client, "x", map[string]any{"tid": tid, "oid": "o3", "preferred_username": "guest@gmail.test"})
	if err := f.signIn(o, guest); err == nil {
		t.Fatal("a guest from another domain signed in")
	}
}

func TestEntraMultiTenantChecksEachTokensIssuerAgainstItsTenantAndTheAllowList(t *testing.T) {
	f := newOIDCFx(t, true)
	other := "11111111-2222-3333-4444-555555555555"
	o := f.rp(OIDCConfig{Provider: "microsoft", Tenant: "organizations", AllowedTenants: []string{tid}})

	if err := f.signIn(o, f.idp.base(f.entraIssuer(tid), f.client, "s", map[string]any{"tid": tid, "oid": "o1", "preferred_username": "a@contoso.test"})); err != nil {
		t.Fatalf("an allowed tenant failed: %v", err)
	}
	if err := f.signIn(o, f.idp.base(f.entraIssuer(other), f.client, "s", map[string]any{"tid": other, "oid": "o2", "preferred_username": "b@other.test"})); err == nil {
		t.Fatal("a tenant outside the allow list signed in")
	}
	// An issuer that does not match the tenant the token claims is a forged pairing.
	if err := f.signIn(o, f.idp.base(f.entraIssuer(other), f.client, "s", map[string]any{"tid": tid, "oid": "o3", "preferred_username": "c@contoso.test"})); err == nil {
		t.Fatal("a token whose issuer names one tenant and whose tid names another signed in")
	}
	// Two tenants never share an identity: the issuer is part of the key.
	var n int
	_ = f.db.QueryRow(context.Background(), `SELECT count(DISTINCT issuer) FROM user_identities`).Scan(&n)
	if n != 1 {
		t.Errorf("%d issuers recorded", n)
	}
}

func TestEntraGroupOverageIsFetchedFromGraphAsThePerson(t *testing.T) {
	f := newOIDCFx(t, false)
	o := f.rp(OIDCConfig{Provider: "microsoft", Tenant: tid, RequiredRoles: []string{"group-guid-77"}})
	claims := f.idp.base(f.entraIssuer(tid), f.client, "s", map[string]any{"tid": tid, "oid": "o1", "preferred_username": "a@contoso.test",
		"_claim_names":   map[string]string{"groups": "src1"},
		"_claim_sources": map[string]any{"src1": map[string]string{"endpoint": "https://graph.windows.net/" + tid + "/users/o1/getMemberObjects"}}})

	f.idp.graphIDs = []string{"group-guid-1", "group-guid-77"}
	if err := f.signIn(o, claims); err != nil {
		t.Fatalf("a person in the required group, listed by Graph, was refused: %v", err)
	}
	f.idp.graphIDs = []string{"group-guid-1"}
	if err := f.signIn(o, claims); err == nil {
		t.Fatal("a person not in the required group signed in")
	}
	f.idp.graphCode = 403
	if err := f.signIn(o, claims); err == nil {
		t.Fatal("when Graph would not list the groups the sign-in carried on as if there were none")
	}
}

// ---- Google ---------------------------------------------------------------

func TestGoogleWorkspaceSignsInOnlyWithTheHostedDomainClaim(t *testing.T) {
	f := newOIDCFx(t, false)
	o := f.rp(OIDCConfig{Provider: "google", Issuer: f.idp.srv.URL, HostedDomain: "example.com"})
	mk := func(sub string, extra map[string]any) map[string]any {
		extra["email"] = sub + "@example.com"
		return f.idp.base(f.idp.srv.URL, f.client, sub, extra)
	}
	if err := f.signIn(o, mk("ana", map[string]any{"hd": "example.com", "email_verified": true})); err != nil {
		t.Fatalf("a Workspace account failed: %v", err)
	}
	if err := f.signIn(o, mk("pat", map[string]any{"hd": "Example.COM", "email_verified": true})); err != nil {
		t.Fatalf("hd in another case failed: %v", err)
	}
	if err := f.signIn(o, mk("gmail", map[string]any{"email_verified": true})); err == nil {
		t.Fatal("an account with no hd claim (a personal Gmail) signed in")
	}
	if err := f.signIn(o, mk("evil", map[string]any{"hd": "other.com", "email_verified": true})); err == nil {
		t.Fatal("another Workspace domain signed in")
	}
	if err := f.signIn(o, mk("unverified", map[string]any{"hd": "example.com", "email_verified": false})); err == nil {
		t.Fatal("an unverified address signed in")
	}
}

func TestPresetsRefuseAnIncompleteConfiguration(t *testing.T) {
	if _, err := (OIDCConfig{Provider: "microsoft"}).Resolve(); err == nil {
		t.Error("the Microsoft preset accepted no tenant")
	}
	if _, err := (OIDCConfig{Provider: "google"}).Resolve(); err == nil {
		t.Error("the Google preset accepted no domain")
	}
	if _, err := (OIDCConfig{Provider: "okta"}).Resolve(); err == nil {
		t.Error("an unknown preset was accepted")
	}
	c, err := (OIDCConfig{Provider: "microsoft", Tenant: tid}).Resolve()
	if err != nil || c.Issuer != "https://login.microsoftonline.com/"+tid+"/v2.0" {
		t.Errorf("issuer %q %v", c.Issuer, err)
	}
	if g, _ := (OIDCConfig{Provider: "google", HostedDomain: "example.com"}).Resolve(); g.Issuer != "https://accounts.google.com" {
		t.Errorf("google issuer %q", g.Issuer)
	}
}

// ---- groups to capabilities -----------------------------------------------

func TestAGroupGivesACapabilityAndLosingItTakesItBackButNeverAHandGrant(t *testing.T) {
	f := newOIDCFx(t, false)
	o := f.rp(OIDCConfig{Provider: "microsoft", Tenant: tid, RoleMap: map[string]string{"grp-audit": "audit", "grp-fin": "finance"}})
	claims := func(groups ...string) map[string]any {
		return f.idp.base(f.entraIssuer(tid), f.client, "s", map[string]any{"tid": tid, "oid": "o1", "preferred_username": "a@contoso.test", "groups": groups})
	}
	if err := f.signIn(o, claims("grp-audit", "unrelated")); err != nil {
		t.Fatal(err)
	}
	if c := f.caps("o1"); !c["audit"] || c["finance"] {
		t.Fatalf("after the first sign-in: %v", c)
	}
	// Somebody hands them finance by hand; the provider's group for it appears and disappears.
	pid := ""
	_ = f.db.QueryRow(context.Background(), `SELECT principal_id::text FROM user_identities WHERE subject='o1'`).Scan(&pid)
	if _, err := f.db.Exec(context.Background(), `INSERT INTO capability_grants (org_id, principal_id, capability, granted_by) VALUES ($1,$2,'finance',$3)`, f.org, pid, f.owner); err != nil {
		t.Fatal(err)
	}
	if err := f.signIn(o, claims("grp-audit", "grp-fin")); err != nil {
		t.Fatal(err)
	}
	if err := f.signIn(o, claims()); err != nil { // out of every group
		t.Fatal(err)
	}
	c := f.caps("o1")
	if c["audit"] {
		t.Error("losing the group did not take the capability back")
	}
	if !c["finance"] {
		t.Error("the provider took back a capability somebody granted by hand")
	}
}

func TestTheOwnerAndSuperAdminAreOutOfReachOfTheProvider(t *testing.T) {
	f := newOIDCFx(t, false)
	ctx := context.Background()
	if err := f.svc.ApplyIdPRoles(ctx, f.org, f.owner, []string{"audit"}); err != nil {
		t.Fatal(err)
	}
	if c := f.capsOf(f.owner); c["audit"] && !c["super_admin"] {
		t.Error("the owner was given a role by the provider")
	}
	p, _ := f.svc.AddPerson(ctx, f.org, f.owner, "Ana", "ana@example.com", "a-long-enough-password")
	if err := f.svc.ApplyIdPRoles(ctx, f.org, p, []string{"super_admin"}); err == nil {
		t.Fatal("a provider group was allowed to make somebody a super admin")
	}
	if err := f.svc.ApplyIdPRoles(ctx, f.org, p, []string{"nonsense"}); err == nil {
		t.Fatal("an unknown capability was accepted")
	}
}
