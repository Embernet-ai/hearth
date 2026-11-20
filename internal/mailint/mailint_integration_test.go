package mailint_test

// Work arriving by email, against a real Postgres.
//
// The two claims are that a message creates one work order and no duplicate on
// retry, and that a reply threads under the original in the sender's client.
// Both are about what happens the second time, which is why neither can be
// checked by sending one message and looking at it.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/mailint"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

type fx struct {
	svc   *mailint.Service
	docs  *docs.Service
	crm   *crm.Service
	az    *authz.Service
	pools *pg.Pools
	org   string
	alice string
	addr  string
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
	d := docs.New(pools.Web, az)
	c := crm.New(pools.Web, az)
	f := &fx{
		docs: d, crm: c, az: az, pools: pools,
		org: uuid.NewString(), alice: uuid.NewString(),
	}
	f.svc = mailint.New(pools.Web, d, channels.New(pools.Web, az), c, "works.example")
	// Unique per run, so two runs against the same database do not fight over
	// the one unique index on the address.
	f.addr = "maintenance-" + f.org[:8] + "@works.example"

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Mail Test')`, f.org, "mt-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM email_messages WHERE org_id=$1`,
			`DELETE FROM inbound_addresses WHERE org_id=$1`,
			`DELETE FROM contacts WHERE org_id=$1`,
			`DELETE FROM companies WHERE org_id=$1`,
			`DELETE FROM messages WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = pools.Web.Exec(ctx, q, f.org)
		}
	})
	return f
}

func (f *fx) subj(t *testing.T) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, f.alice)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

func (f *fx) address(t *testing.T) {
	t.Helper()
	if _, err := f.svc.SaveAddress(context.Background(), f.org, f.subj(t),
		mailint.Address{Address: f.addr, Kind: docs.KindWorkOrder}); err != nil {
		t.Fatalf("save address: %v", err)
	}
}

// message builds a raw message the way a client sends one.
func (f *fx) message(id, subject, body string, headers ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: Dana Whitfield <dana@example.com>\r\n")
	fmt.Fprintf(&b, "To: %s\r\n", f.addr)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Message-Id: <%s>\r\n", id)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	for i := 0; i+1 < len(headers); i += 2 {
		fmt.Fprintf(&b, "%s: %s\r\n", headers[i], headers[i+1])
	}
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

func (f *fx) countWorkOrders(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pools.Web.QueryRow(context.Background(),
		`SELECT count(*) FROM documents WHERE org_id=$1 AND kind='work_order' AND deleted_at IS NULL`,
		f.org).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// -----------------------------------------------------------------------------

// The criterion: one work order, and no duplicate when the relay retries.
//
// Every MTA in existence redelivers when an acknowledgement is lost, so this is
// the ordinary case rather than an edge one.
func TestAnEmailCreatesOneWorkOrderAndNoDuplicateOnRetry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.address(t)

	raw := f.message("first@example.com", "Line 3 conveyor is making a noise",
		"It started this morning, on the drive end.\n")

	filed, err := f.svc.Receive(ctx, raw)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if !filed.Created || filed.DocumentID == "" {
		t.Fatalf("nothing was created: %+v", filed)
	}
	if n := f.countWorkOrders(t); n != 1 {
		t.Fatalf("%d work orders after one message, want 1", n)
	}

	// The relay retries, three times, exactly as one does.
	for i := 0; i < 3; i++ {
		again, err := f.svc.Receive(ctx, raw)
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if !again.Duplicate {
			t.Errorf("retry %d was not recognised as one", i)
		}
		if again.DocumentID != filed.DocumentID {
			t.Errorf("retry %d landed on %s rather than %s",
				i, again.DocumentID, filed.DocumentID)
		}
	}
	if n := f.countWorkOrders(t); n != 1 {
		t.Errorf("%d work orders after three retries, want 1.\n"+
			"A relay redelivers whenever an acknowledgement is lost, and a path "+
			"that answers that with four work orders is one the crew stops "+
			"trusting on its second bad afternoon.", n)
	}

	// And the work order says what arrived and who from.
	d, err := f.docs.Get(ctx, f.org, f.subj(t), filed.DocumentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if d.Title != "Line 3 conveyor is making a noise" {
		t.Errorf("title is %q", d.Title)
	}
	if !strings.Contains(d.Body, "dana@example.com") {
		t.Errorf("the body does not say who sent it: %q", d.Body)
	}
	if !strings.Contains(d.Body, "drive end") {
		t.Errorf("the body lost the message: %q", d.Body)
	}
	if d.Task == nil || d.Task.Status != docs.StatusTodo {
		t.Errorf("the work order has no lifecycle: %+v", d.Task)
	}
}

// The criterion: a reply threads under the original in the sender's client.
//
// Which means two things have to be right. A reply arriving here has to land on
// the work it answers rather than making a second one, and a reply leaving here
// has to carry the headers a client threads on.
func TestARepliesThreadInBothDirections(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.address(t)

	first, err := f.svc.Receive(ctx, f.message("orig@example.com", "Pump is leaking", "Badly.\n"))
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	// Our reply out. This is what the sender's client threads on.
	reply, err := f.svc.PrepareReply(ctx, f.org, first.DocumentID, "On our way.")
	if err != nil {
		t.Fatalf("prepare reply: %v", err)
	}
	if reply.To != "dana@example.com" {
		t.Errorf("replying to %q", reply.To)
	}
	if reply.Subject != "Re: Pump is leaking" {
		t.Errorf("subject is %q", reply.Subject)
	}
	if reply.InReplyTo != "orig@example.com" {
		t.Errorf("in-reply-to is %q, want the message being answered", reply.InReplyTo)
	}
	h := reply.Headers()
	if h["In-Reply-To"] != "<orig@example.com>" {
		t.Errorf("In-Reply-To header is %q", h["In-Reply-To"])
	}
	if !strings.Contains(h["References"], "<orig@example.com>") {
		t.Errorf("References is %q, and a client that rewrites In-Reply-To "+
			"threads on this one", h["References"])
	}
	if h["Message-Id"] == "" || !strings.Contains(h["Message-Id"], "works.example") {
		t.Errorf("Message-Id is %q, and a right hand side that does not resolve "+
			"gets the reply filed as spam", h["Message-Id"])
	}
	if err := f.svc.RecordOutbound(ctx, f.org, first.DocumentID, reply); err != nil {
		t.Fatalf("record outbound: %v", err)
	}

	// Their reply back. It quotes our Message-ID, and has to land on the same
	// work order rather than opening a second one.
	back := f.message("theirs@example.com", "Re: Pump is leaking", "Thanks.\n",
		"In-Reply-To", "<"+reply.MessageID+">",
		"References", "<orig@example.com> <"+reply.MessageID+">")
	second, err := f.svc.Receive(ctx, back)
	if err != nil {
		t.Fatalf("receive reply: %v", err)
	}
	if second.Created {
		t.Error("a reply opened a second work order")
	}
	if second.DocumentID != first.DocumentID {
		t.Errorf("the reply landed on %s rather than %s", second.DocumentID, first.DocumentID)
	}
	if n := f.countWorkOrders(t); n != 1 {
		t.Errorf("%d work orders after a message and a reply, want 1", n)
	}

	// And the thread reads in order.
	thread, err := f.svc.Thread(ctx, f.org, first.DocumentID)
	if err != nil {
		t.Fatalf("thread: %v", err)
	}
	if len(thread) != 3 {
		t.Fatalf("%d messages on the thread, want 3", len(thread))
	}
	if !thread[0].Inbound() || thread[1].Inbound() || !thread[2].Inbound() {
		t.Errorf("the directions read %v, want in, out, in",
			[]string{thread[0].Direction, thread[1].Direction, thread[2].Direction})
	}
}

// A client that rewrites In-Reply-To still threads, because References is kept
// whole. Phones do this constantly.
func TestAReplyThreadsOnReferencesWhenInReplyToIsWrong(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.address(t)

	first, err := f.svc.Receive(ctx, f.message("start@example.com", "Fault", "Body.\n"))
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	// In-Reply-To points at something nobody has ever seen; References still
	// carries the original.
	back := f.message("phone@example.com", "Re: Fault", "From my phone.\n",
		"In-Reply-To", "<nonsense@elsewhere.example>",
		"References", "<start@example.com>")
	second, err := f.svc.Receive(ctx, back)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if second.DocumentID != first.DocumentID {
		t.Errorf("the reply did not thread, so the crew get two work orders "+
			"for one fault: %s vs %s", second.DocumentID, first.DocumentID)
	}
}

// A message to an address nobody configured is refused cleanly. Retrying it
// does not improve it, and creating something from it would file spam as work.
func TestAMessageToAnUnknownAddressIsRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.address(t)

	raw := []byte("From: someone@example.com\r\n" +
		"To: nobody@elsewhere.example\r\n" +
		"Subject: Not for us\r\nMessage-Id: <stray@example.com>\r\n\r\nHello.\r\n")
	if _, err := f.svc.Receive(ctx, raw); !errors.Is(err, mailint.ErrNoAddress) {
		t.Errorf("receive returned %v, want ErrNoAddress", err)
	}
	if n := f.countWorkOrders(t); n != 0 {
		t.Errorf("%d work orders from a message that was not for us", n)
	}
}

// A message from somebody on file is attached to them, so the job says who to
// ring back.
func TestTheSenderIsLinkedWhenTheyAreOnFile(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.address(t)

	contact, err := f.crm.CreateContact(ctx, f.org, f.subj(t), crm.Contact{
		Name: "Dana Whitfield", Email: "dana@example.com",
	})
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}

	filed, err := f.svc.Receive(ctx, f.message("known@example.com", "Fault", "Body.\n"))
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	parties, err := f.crm.PartiesFor(ctx, f.org, f.subj(t), filed.DocumentID)
	if err != nil {
		t.Fatalf("parties: %v", err)
	}
	if len(parties) != 1 || parties[0].ID != contact.ID {
		t.Errorf("the sender was not attached to the work order: %+v", parties)
	}

	// And a message from a stranger is still work. Refusing it because they
	// are not in the address book would be a filing system with opinions.
	stray, err := f.svc.Receive(ctx, []byte(
		"From: stranger@example.com\r\nTo: "+f.addr+"\r\n"+
			"Subject: Also a fault\r\nMessage-Id: <stranger@example.com>\r\n\r\nBody.\r\n"))
	if err != nil {
		t.Fatalf("a message from somebody unknown was refused: %v", err)
	}
	if !stray.Created {
		t.Error("a message from somebody unknown created nothing")
	}
}

// Replying to work that never arrived by email has nowhere to go, and says so
// rather than sending into the dark.
func TestReplyingToWorkThatDidNotArriveByEmailIsRefused(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	d, err := f.docs.Create(ctx, f.org, f.subj(t), docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Typed in by hand",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.svc.PrepareReply(ctx, f.org, d.ID, "Hello?"); !errors.Is(err, mailint.ErrNotFound) {
		t.Errorf("PrepareReply returned %v, want not-found", err)
	}
}

// An address is one row. Two addresses that are the same string with different
// capitalisation would make where a message lands depend on how the sender
// typed it.
func TestAnAddressIsUniqueRegardlessOfCase(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.address(t)

	if _, err := f.svc.SaveAddress(ctx, f.org, f.subj(t), mailint.Address{
		Address: strings.ToUpper(f.addr), Kind: docs.KindTask,
	}); err == nil {
		t.Error("the same address was accepted twice in different case")
	}

	// And a message addressed in capitals still finds it.
	raw := []byte("From: dana@example.com\r\nTo: " + strings.ToUpper(f.addr) + "\r\n" +
		"Subject: Shouting\r\nMessage-Id: <caps@example.com>\r\n\r\nBody.\r\n")
	if _, err := f.svc.Receive(ctx, raw); err != nil {
		t.Errorf("a message addressed in capitals was not matched: %v", err)
	}
}
