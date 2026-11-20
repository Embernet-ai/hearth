package channels_test

// Channels, against a real Postgres.
//
// The four checks that matter are all about the permission model, because that
// is what this phase is really delivering: joining a channel is how access is
// granted, and leaving is how it is taken away. Everything else here is a
// conversation, and a conversation is easy.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type fx struct {
	svc   *channels.Service
	docs  *docs.Service
	az    *authz.Service
	pools *pg.Pools
	org   string
	alice string
	bob   string
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

	az := authz.New(pools.Web)
	f := &fx{
		svc: channels.New(pools.Web, az), docs: docs.New(pools.Web, az), az: az, pools: pools,
		org: uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Channels Test')`, f.org, "ch-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name, handle) VALUES ($1,$2,'user','Alice',$3)`,
		f.alice, f.org, "alice-"+f.alice[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name, handle) VALUES ($1,$2,'user','Bob',$3)`,
		f.bob, f.org, "bob-"+f.bob[:8])

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM messages WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM channel_members WHERE channel_id IN (SELECT id FROM channels WHERE org_id=$1)`,
			`DELETE FROM channels WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) subj(t *testing.T, p string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, p)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

func (f *fx) handle(t *testing.T, p string) string {
	t.Helper()
	var h string
	if err := f.pools.Web.QueryRow(context.Background(),
		`SELECT handle::text FROM principals WHERE id=$1`, p).Scan(&h); err != nil {
		t.Fatalf("handle: %v", err)
	}
	return h
}

// The claim the whole phase exists to make good on. Joining a channel reveals
// what is attached to it, and leaving hides it again, with nothing copied
// anywhere in between.
func TestJoiningAChannelRevealsWhatIsAttachedAndLeavingHidesIt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	c, err := f.svc.Create(ctx, f.org, f.alice, "Line 3 maintenance", "the conveyor", channels.Public)
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Replace the outboard bearing",
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if err := f.svc.Attach(ctx, f.org, alice, c.ID, d.ID); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Bob is not in it yet, so it is not his.
	if _, err := f.docs.Get(ctx, f.org, f.subj(t, f.bob), d.ID); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("bob could see an attached document before joining: %v", err)
	}

	if err := f.svc.Join(ctx, f.org, f.bob, c.ID); err != nil {
		t.Fatalf("join: %v", err)
	}
	// The subject set is recomputed per request, which is what makes this
	// immediate rather than something a background job has to catch up on.
	if _, err := f.docs.Get(ctx, f.org, f.subj(t, f.bob), d.ID); err != nil {
		t.Fatalf("joining did not reveal the attached document: %v", err)
	}

	if err := f.svc.Leave(ctx, c.ID, f.bob); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := f.docs.Get(ctx, f.org, f.subj(t, f.bob), d.ID); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("leaving did not hide the attached document: %v", err)
	}
}

// A private channel is not joinable by walking up to it, and does not appear in
// a list. Listing it with a lock icon tells somebody it exists, which is most
// of what private means.
func TestAPrivateChannelIsNotJoinableOrVisible(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	c, err := f.svc.Create(ctx, f.org, f.alice, "Rates", "contract talk", channels.Private)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := f.svc.Join(ctx, f.org, f.bob, c.ID); !errors.Is(err, channels.ErrNotFound) {
		t.Fatalf("bob joined a private channel: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.org, f.bob, c.ID); !errors.Is(err, channels.ErrNotFound) {
		t.Fatalf("bob could open a private channel: %v", err)
	}
	list, err := f.svc.List(ctx, f.org, f.bob)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, ch := range list {
		if ch.ID == c.ID {
			t.Fatal("a private channel appeared in a non-member's list")
		}
	}

	// Somebody already inside can add him, and then it is his.
	if err := f.svc.Invite(ctx, f.org, f.alice, c.ID, f.bob); err != nil {
		t.Fatalf("invite: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.org, f.bob, c.ID); err != nil {
		t.Fatalf("bob still cannot open a channel he was added to: %v", err)
	}
	// And a bystander cannot hand out membership.
	carol := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Carol')`,
		carol, f.org); err != nil {
		t.Fatalf("carol: %v", err)
	}
	other, err := f.svc.Create(ctx, f.org, f.alice, "Elsewhere", "", channels.Private)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.Invite(ctx, f.org, carol, other.ID, f.bob); !errors.Is(err, channels.ErrNotFound) {
		t.Fatalf("a non-member handed out membership: %v", err)
	}
}

// A mention is a real link, so a work order knows every conversation that
// referenced it. That is the claim, and this is it working in both directions.
func TestAMentionAppearsOnTheMentionedEntity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	c, err := f.svc.Create(ctx, f.org, f.alice, "Shift handover", "", channels.Public)
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Conveyor bearing",
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	body := "Handing over: /documents/" + d.ID + " still needs the bearing, ask @" +
		f.handle(t, f.bob) + " about the spare."
	m, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{ChannelID: c.ID, Body: body})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(m.Mentions) != 2 {
		t.Fatalf("%d mentions recorded, want the document and the person: %+v", len(m.Mentions), m.Mentions)
	}

	// The reverse direction, which is the one somebody actually asks for.
	mentioned, err := f.svc.MentionedIn(ctx, f.org, alice, d.Ref(), 10)
	if err != nil {
		t.Fatalf("mentioned in: %v", err)
	}
	if len(mentioned) != 1 || mentioned[0].ID != m.ID {
		t.Fatalf("the work order does not know about the message: %d found", len(mentioned))
	}

	// A mention in a room somebody is not in is not theirs to read, even though
	// they can see the thing it named.
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	private, err := f.svc.Create(ctx, f.org, f.alice, "Quiet room", "", channels.Private)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{
		ChannelID: private.ID, Body: "secretly, /documents/" + d.ID + " is a nightmare",
	}); err != nil {
		t.Fatalf("post: %v", err)
	}
	theirs, err := f.svc.MentionedIn(ctx, f.org, f.subj(t, f.bob), d.Ref(), 10)
	if err != nil {
		t.Fatalf("mentioned in as bob: %v", err)
	}
	for _, msg := range theirs {
		if msg.ChannelID != nil && *msg.ChannelID == private.ID {
			t.Fatal("a message from a private channel leaked through the mention list")
		}
	}

	// A handle nobody has stays plain text rather than erroring, because
	// somebody typing an email address in a sentence has not made a mistake.
	plain, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{
		ChannelID: c.ID, Body: "email it to @nobody-here and cc me",
	})
	if err != nil {
		t.Fatalf("post with an unknown handle: %v", err)
	}
	if len(plain.Mentions) != 0 {
		t.Errorf("an unknown handle became a mention: %+v", plain.Mentions)
	}
}

// A comment needs COMMENT access, which is the level that has been in the enum
// since the beginning with nothing using it.
func TestCommentingNeedsCommentAccessAndNotEdit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Pump seal",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Bob can see nothing yet.
	if _, err := f.svc.Post(ctx, f.org, f.subj(t, f.bob), channels.PostParams{
		Entity: d.Ref(), Body: "I have thoughts",
	}); err == nil {
		t.Fatal("a stranger commented on a document they cannot see")
	}

	// View is not enough. Reading something is not permission to write on it.
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant view: %v", err)
	}
	if _, err := f.svc.Post(ctx, f.org, f.subj(t, f.bob), channels.PostParams{
		Entity: d.Ref(), Body: "I have thoughts",
	}); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("view access was enough to comment: %v", err)
	}

	// Comment is, and it is still not enough to edit.
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessComment, f.alice); err != nil {
		t.Fatalf("grant comment: %v", err)
	}
	bob := f.subj(t, f.bob)
	if _, err := f.svc.Post(ctx, f.org, bob, channels.PostParams{
		Entity: d.Ref(), Body: "The spare is on order.",
	}); err != nil {
		t.Fatalf("comment access was not enough to comment: %v", err)
	}
	title := "bob was here"
	if _, err := f.docs.Update(ctx, f.org, bob, d.ID, docs.UpdateParams{Title: &title}); err == nil {
		t.Fatal("comment access was enough to edit the document")
	}

	list, err := f.svc.Comments(ctx, f.org, alice, d.Ref(), 10)
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	if len(list) != 1 || list[0].Body != "The spare is on order." {
		t.Fatalf("%d comments: %+v", len(list), list)
	}
	if list[0].AuthorName != "Bob" {
		t.Errorf("author rendered as %q", list[0].AuthorName)
	}
}

// A message must publish an event in the same transaction as itself, or a
// notification can describe something that was rolled back.
func TestPostingPublishesOneEventInTheSameTransaction(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	c, err := f.svc.Create(ctx, f.org, f.alice, "Events", "", channels.Public)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{
		ChannelID: c.ID, Body: "something happened",
	}); err != nil {
		t.Fatalf("post: %v", err)
	}

	var n int
	if err := f.pools.Web.QueryRow(ctx, `
		SELECT count(*) FROM outbox
		WHERE org_id=$1 AND entity_type='channel' AND entity_id=$2 AND event='message'`,
		f.org, c.ID).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 1 {
		t.Errorf("%d message events, want 1", n)
	}
}

// Unread is what makes a channel list glanceable. last_read_at has been a
// column with nothing writing to it since the substrate landed.
func TestUnreadCountsWhatArrivedSinceYouLooked(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	c, err := f.svc.Create(ctx, f.org, f.alice, "Noisy", "", channels.Public)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.Join(ctx, f.org, f.bob, c.ID); err != nil {
		t.Fatalf("join: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{
			ChannelID: c.ID, Body: "message",
		}); err != nil {
			t.Fatalf("post: %v", err)
		}
	}

	n, err := f.svc.Unread(ctx, c.ID, f.bob)
	if err != nil {
		t.Fatalf("unread: %v", err)
	}
	if n != 3 {
		t.Errorf("bob has %d unread, want 3", n)
	}

	// Your own messages are not unread to you, which sounds obvious and is the
	// commonest thing this kind of counter gets wrong.
	if mine, err := f.svc.Unread(ctx, c.ID, f.alice); err != nil || mine != 0 {
		t.Errorf("alice has %d unread of her own messages (err=%v)", mine, err)
	}

	if err := f.svc.MarkRead(ctx, c.ID, f.bob); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if n, err := f.svc.Unread(ctx, c.ID, f.bob); err != nil || n != 0 {
		t.Errorf("bob has %d unread after reading (err=%v)", n, err)
	}
}

// Attaching requires admin on the document and membership of the channel.
// Widening who can see something is not the same authority as changing it, and
// handing it to a room you cannot see into is worse.
func TestAttachingNeedsAdminAndMembership(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	c, err := f.svc.Create(ctx, f.org, f.alice, "Shared", "", channels.Public)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Runbook"})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}

	// Bob has edit, and is in the channel, and still may not widen it.
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessEdit, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := f.svc.Join(ctx, f.org, f.bob, c.ID); err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := f.svc.Attach(ctx, f.org, f.subj(t, f.bob), c.ID, d.ID); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("edit access was enough to share a document with a whole channel: %v", err)
	}

	// Alice has admin but is not in some other channel.
	other, err := f.svc.Create(ctx, f.org, f.bob, "Not hers", "", channels.Private)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.Attach(ctx, f.org, alice, other.ID, d.ID); !errors.Is(err, channels.ErrNotFound) {
		t.Fatalf("a document was handed to a channel the sharer is not in: %v", err)
	}

	// Her own channel, her own document.
	if err := f.svc.Attach(ctx, f.org, alice, c.ID, d.ID); err != nil {
		t.Fatalf("attach: %v", err)
	}
	attached, err := f.svc.AttachedDocuments(ctx, f.org, c.ID)
	if err != nil {
		t.Fatalf("attached: %v", err)
	}
	if len(attached) != 1 || attached[0].ID != d.ID {
		t.Fatalf("%d documents attached", len(attached))
	}
}

// Deleting somebody else's words in a shared room is a moderation power this
// does not have, and inventing one quietly would be worse than not having it.
func TestOnlyTheAuthorOrAnAdminCanDeleteAMessage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	c, err := f.svc.Create(ctx, f.org, f.alice, "Room", "", channels.Public)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.Join(ctx, f.org, f.bob, c.ID); err != nil {
		t.Fatalf("join: %v", err)
	}
	m, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{ChannelID: c.ID, Body: "mine"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if err := f.svc.Delete(ctx, f.org, f.subj(t, f.bob), m.ID); !errors.Is(err, channels.ErrNotFound) {
		t.Fatalf("bob deleted alice's message: %v", err)
	}
	if err := f.svc.Delete(ctx, f.org, alice, m.ID); err != nil {
		t.Fatalf("alice could not delete her own message: %v", err)
	}

	// A comment on a document, where the document's admin IS a moderator,
	// because they own the thing being discussed.
	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Runbook"})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessComment, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	theirs, err := f.svc.Post(ctx, f.org, f.subj(t, f.bob), channels.PostParams{
		Entity: d.Ref(), Body: "bob's comment",
	})
	if err != nil {
		t.Fatalf("post comment: %v", err)
	}
	if err := f.svc.Delete(ctx, f.org, alice, theirs.ID); err != nil {
		t.Fatalf("the document's admin could not remove a comment on it: %v", err)
	}
}

// Deleting a comment is not the end of it: Restore brings back the same row,
// same shape as timelog's Remove/Restore for a time entry.
func TestDeleteThenRestoreBringsBackTheSameComment(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Runbook"})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	m, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{Entity: d.Ref(), Body: "original"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if err := f.svc.Delete(ctx, f.org, alice, m.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	gone, err := f.svc.Comments(ctx, f.org, alice, d.Ref(), 10)
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	for _, c := range gone {
		if c.ID == m.ID {
			t.Fatalf("removed comment still listed")
		}
	}

	if err := f.svc.Restore(ctx, f.org, alice, m.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	back, err := f.svc.Comments(ctx, f.org, alice, d.Ref(), 10)
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	found := false
	for _, c := range back {
		if c.ID == m.ID && c.Body == "original" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored comment not listed, or body changed")
	}
}

// Same idempotency rule timelog's Remove/Restore settled on: a stale page
// re-submitting the same click should not surface as an error.
func TestDeleteAndRestoreOnAMessageAreIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Runbook"})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	m, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{Entity: d.Ref(), Body: "hi"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if err := f.svc.Restore(ctx, f.org, alice, m.ID); err != nil {
		t.Fatalf("restoring an active comment should be a no-op, not an error: %v", err)
	}
	if err := f.svc.Delete(ctx, f.org, alice, m.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := f.svc.Delete(ctx, f.org, alice, m.ID); err != nil {
		t.Fatalf("deleting an already-removed comment should be a no-op, not an error: %v", err)
	}
}

// Fixing a typo is the whole point of Edit: same author, new body, and the
// row remembers it was touched.
func TestAuthorCanEditTheirOwnComment(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Runbook"})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	m, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{Entity: d.Ref(), Body: "typo"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if err := f.svc.Edit(ctx, f.org, alice, m.ID, "fixed"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	list, err := f.svc.Comments(ctx, f.org, alice, d.Ref(), 10)
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	for _, c := range list {
		if c.ID == m.ID {
			if c.Body != "fixed" {
				t.Fatalf("body = %q, want fixed", c.Body)
			}
			if c.EditedAt == nil {
				t.Fatalf("EditedAt not set after an edit")
			}
			return
		}
	}
	t.Fatalf("edited comment not found")
}

// Edit is author-only -- unlike Delete, an entity admin does not get to
// rewrite somebody else's words. See channels.Edit's own reasoning: doing
// that quietly would misattribute the admin's wording to the original
// author's name.
func TestAdminCannotEditSomeoneElsesComment(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	bob := f.subj(t, f.bob)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Runbook"})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessComment, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	m, err := f.svc.Post(ctx, f.org, bob, channels.PostParams{Entity: d.Ref(), Body: "bob's words"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if err := f.svc.Edit(ctx, f.org, alice, m.ID, "alice rewrote it"); !errors.Is(err, channels.ErrNotFound) {
		t.Fatalf("the document's admin edited somebody else's comment: %v", err)
	}
}

// Editing a removed comment is a real conflict, not a stale click -- same
// rule timelog.Correct uses for a deleted time entry: restore it first.
func TestEditRefusesARemovedComment(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Runbook"})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	m, err := f.svc.Post(ctx, f.org, alice, channels.PostParams{Entity: d.Ref(), Body: "hi"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if err := f.svc.Delete(ctx, f.org, alice, m.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := f.svc.Edit(ctx, f.org, alice, m.ID, "sneaking this in"); !errors.Is(err, channels.ErrDeleted) {
		t.Fatalf("edit on a removed comment = %v, want ErrDeleted", err)
	}
}
