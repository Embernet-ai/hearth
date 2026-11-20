package push_test

// Web Push, against a real Postgres and a fake push service that is a real HTTP server.
//
// What is checked is what goes wrong in the field: a subscription that points somewhere it should not, a
// dead one that is tried forever, a key that changes on an upgrade and strands every phone, and a person
// who turned a kind off being rung for it anyway.

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
	"github.com/Embernet-ai/hearth/internal/push"
)

type env struct {
	pools *pg.Pools
	org   string
	alice string
	bob   string
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := pgtest.Database(t, "hearth_test_push")
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
	e := &env{pools: pools, org: uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString()}
	if _, err := pools.Web.Exec(ctx, `INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Push Test')`, e.org, "pt-"+e.org[:8]); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"Alice": e.alice, "Bob": e.bob} {
		if _, err := pools.Web.Exec(ctx,
			`INSERT INTO principals (id, org_id, kind, display_name, handle) VALUES ($1,$2,'user',$3,$4)`,
			id, e.org, name, name+"-"+id[:8]); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pools.Web.Exec(context.Background(), `SELECT audit_purge_org($1)`, e.org)
		_, _ = pools.Web.Exec(context.Background(), `DELETE FROM principals WHERE org_id=$1`, e.org)
		_, _ = pools.Web.Exec(context.Background(), `DELETE FROM orgs WHERE id=$1`, e.org)
	})
	return e
}

// fakePushService is a push service: it takes the POST a browser's vendor would, records it, and answers with
// the status the test chose for that path.
type fakePushService struct {
	*httptest.Server
	mu     sync.Mutex
	status map[string]int
	seen   []seenPush
}

type seenPush struct {
	path string
	auth string
	enc  string
	ttl  string
	body int
}

func newFake(t *testing.T) *fakePushService {
	f := &fakePushService{status: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, seenPush{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Encoding"), r.Header.Get("TTL"), len(b)})
		code := f.status[r.URL.Path]
		f.mu.Unlock()
		if code == 0 {
			code = http.StatusCreated
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePushService) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.seen {
		if s.path == path {
			n++
		}
	}
	return n
}

// browserSub makes a subscription the way a browser does: an uncompressed P-256 point and 16 bytes of auth secret.
func browserSub(t *testing.T, endpoint string) push.Subscription {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	var s push.Subscription
	s.Endpoint = endpoint
	s.Keys.P256dh = base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	s.Keys.Auth = base64.RawURLEncoding.EncodeToString(auth)
	return s
}

func realService(t *testing.T, e *env) *push.Service {
	t.Helper()
	keys, err := push.LoadKeys(context.Background(), e.pools.Web, "", nil)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	s, err := push.New(e.pools.Web, push.Options{
		Keys: keys, Subject: "https://hearth.test",
		ValidEndpoint: func(u string) bool { return strings.HasPrefix(u, "http://127.0.0.1") },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A push goes to the push service through the real sender: encrypted, signed with the installation's VAPID key,
// with a time to live. Only the person's own devices are rung.
func TestAPushReachesThePersonsDevicesEncryptedAndSigned(t *testing.T) {
	e := setup(t)
	fake := newFake(t)
	s := realService(t, e)
	ctx := context.Background()

	if err := s.Subscribe(ctx, e.org, e.alice, browserSub(t, fake.URL+"/alice-phone"), "Android Chrome"); err != nil {
		t.Fatal(err)
	}
	if err := s.Subscribe(ctx, e.org, e.bob, browserSub(t, fake.URL+"/bob-phone"), "iPhone Safari"); err != nil {
		t.Fatal(err)
	}
	s.Notify(ctx, push.Message{PrincipalID: e.alice, Kind: push.KindAssigned, Body: "Bob assigned you a job", URL: "/documents/x"})
	s.Flush()

	if fake.count("/alice-phone") != 1 {
		t.Fatalf("alice's phone got %d pushes, want 1", fake.count("/alice-phone"))
	}
	if fake.count("/bob-phone") != 0 {
		t.Fatal("bob's phone was rung for alice's notification")
	}
	got := fake.seen[0]
	if !strings.HasPrefix(got.auth, "vapid t=") || !strings.Contains(got.auth, "k=") {
		t.Fatalf("the push is not signed with VAPID: %q", got.auth)
	}
	if got.enc != "aes128gcm" || got.body < 60 || got.ttl == "" {
		t.Fatalf("the push is not an encrypted Web Push message: %+v", got)
	}
}

// A person who turned a kind off is not rung for it, and a kind they left on still is.
func TestAKindTurnedOffDoesNotRing(t *testing.T) {
	e := setup(t)
	fake := newFake(t)
	s := realService(t, e)
	ctx := context.Background()
	if err := s.Subscribe(ctx, e.org, e.alice, browserSub(t, fake.URL+"/a"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKinds(ctx, e.alice, []string{push.KindAssigned}); err != nil {
		t.Fatal(err)
	}
	s.Notify(ctx, push.Message{PrincipalID: e.alice, Kind: push.KindCommented, Body: "x", URL: "/"})
	s.Notify(ctx, push.Message{PrincipalID: e.alice, Kind: push.KindAssigned, Body: "y", URL: "/"})
	s.Flush()
	if n := fake.count("/a"); n != 1 {
		t.Fatalf("got %d pushes, want only the assigned one", n)
	}
}

// 404 and 410 mean the browser dropped the subscription. It is forgotten, not tried on every notification forever.
// A 500 is the push service's trouble and the device stays.
func TestADeadSubscriptionIsPrunedAndATroubledOneIsKept(t *testing.T) {
	e := setup(t)
	fake := newFake(t)
	fake.status["/gone"], fake.status["/missing"], fake.status["/busy"] = 410, 404, 500
	s := realService(t, e)
	ctx := context.Background()
	for _, p := range []string{"/gone", "/missing", "/busy", "/fine"} {
		if err := s.Subscribe(ctx, e.org, e.alice, browserSub(t, fake.URL+p), p); err != nil {
			t.Fatal(err)
		}
	}
	s.Notify(ctx, push.Message{PrincipalID: e.alice, Kind: push.KindChanged, Body: "x", URL: "/"})
	s.Flush()

	devs, err := s.Devices(ctx, e.alice)
	if err != nil {
		t.Fatal(err)
	}
	left := map[string]int{}
	for _, d := range devs {
		left[d.Label] = d.LastStatus
	}
	if _, ok := left["/gone"]; ok {
		t.Fatal("a 410 subscription is still kept")
	}
	if _, ok := left["/missing"]; ok {
		t.Fatal("a 404 subscription is still kept")
	}
	if left["/busy"] != 500 {
		t.Fatalf("a 500 should leave the device and record the answer, got %v", left)
	}
	if left["/fine"] != 201 {
		t.Fatalf("a good push should record 201, got %v", left)
	}
}

// The send-a-test button reports what happened, and ignores the kinds: asking for a test is asking to be rung.
func TestTheTestButtonRingsAndReports(t *testing.T) {
	e := setup(t)
	fake := newFake(t)
	fake.status["/dead"] = 410
	s := realService(t, e)
	ctx := context.Background()
	if err := s.SetKinds(ctx, e.alice, nil); err != nil {
		t.Fatal(err)
	}
	_ = s.Subscribe(ctx, e.org, e.alice, browserSub(t, fake.URL+"/ok"), "")
	_ = s.Subscribe(ctx, e.org, e.alice, browserSub(t, fake.URL+"/dead"), "")
	r, err := s.Test(ctx, e.alice, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Sent != 1 || r.Removed != 1 || r.Failed != 0 {
		t.Fatalf("test result %+v, want 1 sent and 1 removed", r)
	}
	// Somebody else's endpoint is not theirs to test.
	r, _ = s.Test(ctx, e.bob, fake.URL+"/ok")
	if r.Sent+r.Removed+r.Failed != 0 {
		t.Fatalf("bob tested alice's device: %+v", r)
	}
}

// A subscription is only kept when it names a browser's push service, so nobody can aim the server's outbound
// posts at an address inside the network. This runs against the real allow list.
func TestOnlyABrowsersPushServiceIsAcceptedAndSharedDevicesMove(t *testing.T) {
	e := setup(t)
	keys, _ := push.LoadKeys(context.Background(), e.pools.Web, "", nil)
	s, err := push.New(e.pools.Web, push.Options{Keys: keys, Subject: "https://hearth.test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, bad := range []string{
		"http://fcm.googleapis.com/fcm/send/x", "https://169.254.169.254/x", "https://evil.example/fcm.googleapis.com",
		"https://fcm.googleapis.com.evil.example/x", "https://user@fcm.googleapis.com/x", "https://fcm.googleapis.com:8443/x",
		"https://localhost/x", "",
	} {
		if err := s.Subscribe(ctx, e.org, e.alice, browserSub(t, bad), ""); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, good := range []string{
		"https://fcm.googleapis.com/fcm/send/abc", "https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/abc", "https://wns2-par02p.notify.windows.com/w/?token=abc",
	} {
		if err := s.Subscribe(ctx, e.org, e.alice, browserSub(t, good), "x"); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
	// One handset, two sign-ins: the device belongs to whoever turned it on last.
	same := browserSub(t, "https://fcm.googleapis.com/fcm/send/shared")
	_ = s.Subscribe(ctx, e.org, e.alice, same, "")
	_ = s.Subscribe(ctx, e.org, e.bob, same, "")
	a, _ := s.Devices(ctx, e.alice)
	for _, d := range a {
		if strings.HasSuffix(d.Endpoint, "/shared") {
			t.Fatal("the shared handset still rings for alice after bob turned it on")
		}
	}
	// Removing is the person's own.
	b, _ := s.Devices(ctx, e.bob)
	if len(b) != 1 {
		t.Fatalf("bob has %d devices, want 1", len(b))
	}
	_ = s.Unsubscribe(ctx, e.alice, b[0].ID)
	if b, _ = s.Devices(ctx, e.bob); len(b) != 1 {
		t.Fatal("alice removed bob's device")
	}
	_ = s.Unsubscribe(ctx, e.bob, b[0].ID)
	if b, _ = s.Devices(ctx, e.bob); len(b) != 0 {
		t.Fatal("bob could not remove his own device")
	}
}

// The key is made once and read back by every start after, from a file when the deployment mounts one. A key that
// changed on an upgrade would strand every subscribed phone.
func TestTheVAPIDKeySurvivesAStart(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	first, err := push.LoadKeys(ctx, e.pools.Web, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := push.LoadKeys(ctx, e.pools.Web, "", nil)
	if err != nil || again != first {
		t.Fatalf("the key changed between starts: %v", err)
	}
	if raw, _ := base64.RawURLEncoding.DecodeString(first.Public); len(raw) != 65 || raw[0] != 4 {
		t.Fatalf("the public key is not an uncompressed P-256 point (%d bytes)", len(raw))
	}

	// A mounted key file wins, in the PEM form a Secret holds.
	dir := t.TempDir()
	pem := dir + "/key.pem"
	var n int
	if err := e.pools.Web.QueryRow(ctx, `SELECT count(*) FROM push_vapid`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("push_vapid rows %d %v", n, err)
	}
	var stored []byte
	_ = e.pools.Web.QueryRow(ctx, `SELECT key_pem FROM push_vapid`).Scan(&stored)
	if err := os.WriteFile(pem, stored, 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := push.LoadKeys(ctx, e.pools.Web, pem, nil)
	if err != nil || fromFile != first {
		t.Fatalf("a key file did not give the same pair: %v", err)
	}
	if _, err := push.LoadKeys(ctx, e.pools.Web, dir+"/missing.pem", nil); err == nil {
		t.Fatal("a key file that is not there must not fall back to a new key")
	}
}

func TestKindsListIsTheInboxsOwn(t *testing.T) {
	want := []string{"assigned", "mentioned", "commented", "changed", "due_soon", "approvals"}
	var got []string
	for _, k := range push.Kinds {
		got = append(got, k.ID)
	}
	b1, _ := json.Marshal(got)
	b2, _ := json.Marshal(want)
	if string(b1) != string(b2) {
		t.Fatalf("kinds %s, want %s", b1, b2)
	}
}
