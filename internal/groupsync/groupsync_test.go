package groupsync

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPlanAddsMissingAndRemovesStrays(t *testing.T) {
	have := []Member{{"a@example.com", "MEMBER"}, {"gone@example.com", "MEMBER"}}
	got := Plan("g@example.com", []string{"A@example.com", "new@example.com"}, have, nil)
	want := []Change{
		{Group: "g@example.com", Email: "new@example.com", Why: "on the Hearth team or department"},
		{Group: "g@example.com", Email: "gone@example.com", Remove: true, Why: "not an active member of the Hearth team or department"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestPlanNeverRemovesOwnersManagersOrProtected(t *testing.T) {
	have := []Member{
		{"owner@example.com", "OWNER"}, {"mgr@example.com", "MANAGER"},
		{"keep@example.com", "MEMBER"}, {"stray@example.com", "MEMBER"},
	}
	got := Plan("g@example.com", nil, have, []string{"KEEP@example.com"})
	if len(got) != 1 || got[0].Email != "stray@example.com" || !got[0].Remove {
		t.Fatalf("only the stray plain member should go: %+v", got)
	}
}

func TestPlanIsEmptyWhenInStep(t *testing.T) {
	if got := Plan("g", []string{"a@example.com"}, []Member{{"A@Example.com", "MEMBER"}}, nil); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

type fakeGoogle struct {
	mu      sync.Mutex
	members map[string]string
	calls   []string
	tokens  int
}

func newFake(t *testing.T) (*Google, *fakeGoogle) {
	t.Helper()
	f := &fakeGoogle{members: map[string]string{"x@example.com": "MEMBER", "boss@example.com": "OWNER"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if r.Form.Get("grant_type") == "" || len(parts) != 3 {
			http.Error(w, `{"error":"bad"}`, 400)
			return
		}
		f.tokens++
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	})
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", 401)
			return false
		}
		return true
	}
	mux.HandleFunc("/groups/", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "GET":
			var ms []map[string]string
			for e, ro := range f.members {
				ms = append(ms, map[string]string{"email": e, "role": ro, "type": "USER"})
			}
			ms = append(ms, map[string]string{"email": "sub@example.com", "role": "MEMBER", "type": "GROUP"})
			_ = json.NewEncoder(w).Encode(map[string]any{"members": ms})
		case r.Method == "POST":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["role"] != "MEMBER" {
				http.Error(w, "role", 400)
				return
			}
			f.members[b["email"]] = "MEMBER"
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "DELETE":
			delete(f.members, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
			w.WriteHeader(204)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	kj, _ := json.Marshal(map[string]string{
		"client_email": "sa@example.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":    srv.URL + "/token",
	})
	g, err := NewGoogle(kj, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	g.APIBase = srv.URL
	return g, f
}

func TestGoogleListAddRemoveAndTokenReuse(t *testing.T) {
	g, f := newFake(t)
	ctx := context.Background()
	ms, err := g.Members(ctx, "grp@example.com")
	if err != nil || len(ms) != 2 {
		t.Fatalf("members %v %v (nested groups must be ignored)", ms, err)
	}
	if err := g.Add(ctx, "grp@example.com", "n@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := g.Remove(ctx, "grp@example.com", "x@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.members["n@example.com"]; !ok {
		t.Fatal("not added")
	}
	if _, ok := f.members["x@example.com"]; ok {
		t.Fatal("not removed")
	}
	if f.tokens != 1 {
		t.Fatalf("token minted %d times", f.tokens)
	}
}

func TestGoogleTokenFailureNamesTheScopeAndLeaksNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"unauthorized_client","error_description":"SECRETECHO"}`))
	}))
	defer srv.Close()
	g, _ := newFake(t)
	g.TokenURL = srv.URL
	_, err := g.Members(context.Background(), "g@example.com")
	if err == nil || !strings.Contains(err.Error(), "unauthorized_client") || !strings.Contains(err.Error(), "admin.directory.group.member") || strings.Contains(err.Error(), "SECRETECHO") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewGoogleRefusesABadKey(t *testing.T) {
	if _, err := NewGoogle([]byte(`{"client_email":"a","private_key":"nope"}`), "a@example.com"); err == nil {
		t.Fatal("accepted a bad key")
	}
}
