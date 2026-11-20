package notify_test

// Push rides the inbox: a notification that is written rings the person's devices, with the same sentence and
// nothing more, and one that is not written (their own change, a repeat) does not.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/push"
)

type capture struct {
	mu   sync.Mutex
	sent []map[string]string
}

func (c *capture) send(_ context.Context, body []byte, _ push.Subscription, _ push.SendOptions) (int, error) {
	var m map[string]string
	_ = json.Unmarshal(body, &m)
	c.mu.Lock()
	c.sent = append(c.sent, m)
	c.mu.Unlock()
	return 201, nil
}

func pushFx(t *testing.T) (*fx, *push.Service, *capture) {
	t.Helper()
	f := setup(t)
	keys, err := push.LoadKeys(context.Background(), f.pools.Web, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &capture{}
	ps, err := push.New(f.pools.Web, push.Options{
		Keys: keys, Send: c.send, ValidEndpoint: func(string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SetPusher(ps)
	var sub push.Subscription
	sub.Endpoint = "https://fcm.googleapis.com/fcm/send/" + uuid.NewString()
	sub.Keys.P256dh, sub.Keys.Auth = "k", "a"
	if err := ps.Subscribe(context.Background(), f.org, f.bob, sub, "Android Chrome"); err != nil {
		t.Fatal(err)
	}
	return f, ps, c
}

func TestANotificationThatReachesTheInboxRingsTheDeviceWithTitleOnly(t *testing.T) {
	f, ps, c := pushFx(t)
	ctx := context.Background()
	ref := core.Ref{Type: core.EntityDocument, ID: uuid.NewString()}
	d := notify.Delivery{OrgID: f.org, PrincipalID: f.bob, Reason: notify.ReasonAssigned, Ref: ref,
		Title: "Replace the outboard bearing", Excerpt: "the secret body of the item", ActorID: f.alice, DedupeKey: "k1"}
	if _, err := f.svc.Deliver(ctx, d); err != nil {
		t.Fatal(err)
	}
	// The same event again writes nothing and rings nothing.
	if _, err := f.svc.Deliver(ctx, d); err != nil {
		t.Fatal(err)
	}
	// Bob's own change never reaches Bob.
	own := d
	own.ActorID, own.DedupeKey = f.bob, "k2"
	_, _ = f.svc.Deliver(ctx, own)
	ps.Flush()

	if len(c.sent) != 1 {
		t.Fatalf("%d pushes, want exactly 1 (a repeat and his own change are not rung)", len(c.sent))
	}
	m := c.sent[0]
	if m["body"] != "Alice assigned you Replace the outboard bearing" {
		t.Fatalf("body %q", m["body"])
	}
	if m["url"] != "/documents/"+ref.ID {
		t.Fatalf("url %q opens the item behind sign-in", m["url"])
	}
	for k, v := range m {
		if strings.Contains(v, "secret body") {
			t.Fatalf("the item's content is in the push (%s)", k)
		}
	}
}

func TestPushFollowsTheKindsAndTheQuietHours(t *testing.T) {
	f, ps, c := pushFx(t)
	ctx := context.Background()
	ref := core.Ref{Type: core.EntityTimeOff, ID: uuid.NewString()}

	// Approvals are the time off and department requests and their answers.
	if err := ps.SetKinds(ctx, f.bob, []string{push.KindApprovals}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.svc.Deliver(ctx, notify.Delivery{OrgID: f.org, PrincipalID: f.bob, Reason: notify.ReasonAssigned, Ref: ref,
		Title: "a time off request", ActorID: f.alice, DedupeKey: "t1"})
	_, _ = f.svc.Deliver(ctx, notify.Delivery{OrgID: f.org, PrincipalID: f.bob, Reason: notify.ReasonMentioned,
		Ref: core.Ref{Type: core.EntityDocument, ID: uuid.NewString()}, Title: "a job", ActorID: f.alice, DedupeKey: "t2"})
	ps.Flush()
	if len(c.sent) != 1 || c.sent[0]["kind"] != push.KindApprovals {
		t.Fatalf("pushes %v, want only the approval", c.sent)
	}

	// Quiet hours hold a push as they hold email: it is in the inbox for when the quiet ends.
	if err := ps.SetKinds(ctx, f.bob, []string{push.KindMentioned}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	from, to := (now.Hour()*60+now.Minute()+1439)%1440, (now.Hour()*60+now.Minute()+60)%1440
	if err := f.svc.SetPrefs(ctx, f.bob, notify.Prefs{Email: notify.EmailOff, QuietFrom: &from, QuietTo: &to, Timezone: "UTC", DigestAt: 480}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.svc.Deliver(ctx, notify.Delivery{OrgID: f.org, PrincipalID: f.bob, Reason: notify.ReasonMentioned,
		Ref: core.Ref{Type: core.EntityDocument, ID: uuid.NewString()}, Title: "another job", ActorID: f.alice, DedupeKey: "t3"})
	ps.Flush()
	if len(c.sent) != 1 {
		t.Fatalf("a push went out during quiet hours: %v", c.sent)
	}
	n, err := f.svc.Unread(ctx, f.bob)
	if err != nil || n != 3 {
		t.Fatalf("the inbox holds %d unread (%v), want all three", n, err)
	}
}
