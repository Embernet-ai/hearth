package notify_test

// Notifications, against a real Postgres.
//
// The three checks the plan asks for are all about restraint rather than
// delivery. Sending a notification is easy; the reason people turn a product's
// notifications off is that it sends the wrong ones, twice, about things they
// did themselves.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/platform/pg/pgtest"
)

type fx struct {
	svc      *notify.Service
	fan      *notify.Fanout
	docs     *docs.Service
	channels *channels.Service
	az       *authz.Service
	queue    *jobs.Queue
	relay    *jobs.Relay
	worker   *jobs.Worker
	pools    *pg.Pools
	mail     *fakeMailer
	org      string
	alice    string
	bob      string
}

// fakeMailer records what would have gone out.
//
// A fake here rather than a real relay, because what is being checked is which
// messages are composed and when, not whether net/smtp works. The relay itself
// has no branching worth testing and every branch that matters is above it.
type fakeMailer struct {
	sent []sentMail
}

type sentMail struct{ to, subject, body string }

func (m *fakeMailer) Configured(context.Context, string) bool { return true }
func (m *fakeMailer) Send(_ context.Context, _, to, subject, body string) error {
	m.sent = append(m.sent, sentMail{to, subject, body})
	return nil
}

func setup(t *testing.T) *fx {
	t.Helper()
	// A database of this package's own. Sharing one with every other
	// integration package meant this fixture's drain claimed jobs belonging
	// to another one, and the failure surfaced over there as work that
	// never happened. See internal/platform/pg/pgtest.
	dsn := pgtest.Database(t, "hearth_test_notify")
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
	svc := notify.New(pools.Web)
	f := &fx{
		svc: svc, az: az, pools: pools, mail: &fakeMailer{},
		docs: docs.New(pools.Web, az), channels: channels.New(pools.Web, az),
		queue: jobs.NewQueue(pools.Jobs, time.Minute),
		relay: jobs.NewRelay(pools.Jobs, log),
		org:   uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	f.fan = notify.NewFanout(pools.Jobs, svc, f.queue, f.mail, "https://hearth.test", log)
	f.worker = jobs.NewWorker(f.queue, f.relay, log, jobs.WorkerOptions{ID: "notify-test"})
	f.fan.Register(f.worker)
	f.relay.Route(notify.Routes()...)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Notify Test')`, f.org, "nt-"+f.org[:8])
	for name, id := range map[string]string{"Alice": f.alice, "Bob": f.bob} {
		exec(`INSERT INTO principals (id, org_id, kind, display_name, handle) VALUES ($1,$2,'user',$3,$4)`,
			id, f.org, name, name+"-"+id[:8])
		exec(`INSERT INTO local_credentials (principal_id, org_id, email, password_hash)
		      VALUES ($1,$2,$3,'x')`, id, f.org, id[:8]+"@example.invalid")
	}

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM notifications WHERE org_id=$1`,
			`DELETE FROM watches WHERE principal_id IN (SELECT id FROM principals WHERE org_id=$1)`,
			`DELETE FROM notification_prefs WHERE principal_id IN (SELECT id FROM principals WHERE org_id=$1)`,
			`DELETE FROM jobs WHERE org_id=$1`,
			`DELETE FROM jobs_dead WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`,
			`DELETE FROM messages WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM channel_members WHERE channel_id IN (SELECT id FROM channels WHERE org_id=$1)`,
			`DELETE FROM channels WHERE org_id=$1`,
			`DELETE FROM local_credentials WHERE org_id=$1`,
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

// drain runs the outbox and every job it produces, synchronously.
func (f *fx) drain(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	kinds := f.worker.Kinds()
	for round := 0; round < 200; round++ {
		if _, err := f.relay.Drain(ctx); err != nil {
			t.Fatalf("drain: %v", err)
		}
		job, err := f.queue.Claim(ctx, "test", kinds)
		if errors.Is(err, jobs.ErrNoJob) {
			n, err := f.relay.Drain(ctx)
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if n == 0 {
				return
			}
			continue
		}
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// The recurring jobs reschedule themselves, so running them here would
		// never terminate. They have their own test.
		if job.Kind == notify.KindDues || job.Kind == notify.KindFollowups || job.Kind == notify.KindDigest {
			if err := f.queue.Complete(ctx, job.ID); err != nil {
				t.Fatalf("complete: %v", err)
			}
			continue
		}
		if err := f.worker.RunOne(ctx, job); err != nil {
			t.Fatalf("run %s: %v", job.Kind, err)
		}
		if err := f.queue.Complete(ctx, job.ID); err != nil {
			t.Fatalf("complete: %v", err)
		}
	}
	t.Fatal("the queue never emptied")
}

// Being assigned produces exactly one notification. Not two, which is what
// happens when the assignment event and the update event both fan out.
func TestBeingAssignedProducesExactlyOneNotification(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Replace the outboard bearing",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{AssigneeID: &f.bob},
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	f.drain(t)

	inbox, err := f.svc.Inbox(ctx, f.bob, false, 50)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("bob has %d notifications, want exactly 1: %+v", len(inbox), summarise(inbox))
	}
	if inbox[0].Reason != notify.ReasonAssigned {
		t.Errorf("reason = %q", inbox[0].Reason)
	}
	if inbox[0].Title != "Replace the outboard bearing" {
		t.Errorf("title = %q", inbox[0].Title)
	}

	// Running the fan-out again must not produce a second row. Delivery is
	// at-least-once, so this is the normal case rather than an anomaly.
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE outbox SET published_at = NULL WHERE org_id=$1`, f.org); err != nil {
		t.Fatalf("replay: %v", err)
	}
	f.drain(t)
	inbox, _ = f.svc.Inbox(ctx, f.bob, false, 50)
	if len(inbox) != 1 {
		t.Fatalf("a redelivered event produced %d notifications", len(inbox))
	}

	// And being assigned starts him watching, so the next change reaches him.
	watching, err := f.svc.IsWatching(ctx, f.bob, d.Ref())
	if err != nil {
		t.Fatalf("is watching: %v", err)
	}
	if !watching {
		t.Error("being assigned did not start him watching")
	}
}

// Your own change never notifies you. This is the single fastest way to teach
// somebody that these messages are noise.
func TestYourOwnChangeNeverNotifiesYou(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Her own job",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// She assigns it to herself, moves it, and comments on it.
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{AssigneeID: &f.alice},
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	f.drain(t)
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusInProgress)},
	}); err != nil {
		t.Fatalf("status: %v", err)
	}
	if _, err := f.channels.Post(ctx, f.org, alice, channels.PostParams{
		Entity: d.Ref(), Body: "starting on this",
	}); err != nil {
		t.Fatalf("comment: %v", err)
	}
	f.drain(t)

	inbox, err := f.svc.Inbox(ctx, f.alice, false, 50)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if len(inbox) != 0 {
		t.Fatalf("alice was told about her own work %d times: %+v", len(inbox), summarise(inbox))
	}

	// And somebody else watching it IS told, so the zero above is restraint
	// rather than a fan-out that does nothing.
	if err := f.az.Grant(ctx, f.org, d.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := f.svc.Watch(ctx, f.bob, d.Ref(), notify.WatchManual); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusDone)},
	}); err != nil {
		t.Fatalf("status: %v", err)
	}
	f.drain(t)

	his, _ := f.svc.Inbox(ctx, f.bob, false, 50)
	if len(his) != 1 || his[0].Reason != notify.ReasonChanged {
		t.Fatalf("the watcher got %d notifications: %+v", len(his), summarise(his))
	}
}

// Unwatching stops delivery immediately, with no window where one more arrives.
func TestUnwatchingStopsDeliveryImmediately(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Noisy job",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.Watch(ctx, f.bob, d.Ref(), notify.WatchManual); err != nil {
		t.Fatalf("watch: %v", err)
	}

	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusInProgress)},
	}); err != nil {
		t.Fatalf("status: %v", err)
	}
	f.drain(t)
	if got, _ := f.svc.Inbox(ctx, f.bob, false, 50); len(got) != 1 {
		t.Fatalf("watching produced %d notifications, want 1", len(got))
	}

	if err := f.svc.Unwatch(ctx, f.bob, d.Ref()); err != nil {
		t.Fatalf("unwatch: %v", err)
	}
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusDone)},
	}); err != nil {
		t.Fatalf("status: %v", err)
	}
	f.drain(t)

	if got, _ := f.svc.Inbox(ctx, f.bob, false, 50); len(got) != 1 {
		t.Fatalf("unwatching left %d notifications, want the original 1", len(got))
	}
}

// Being mentioned beats watching, because being named is a stronger reason and
// somebody who is both should be told the stronger one, once.
func TestAMentionWinsOverAWatchAndArrivesOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Bearing",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.Watch(ctx, f.bob, d.Ref(), notify.WatchManual); err != nil {
		t.Fatalf("watch: %v", err)
	}

	var handle string
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT handle::text FROM principals WHERE id=$1`, f.bob).Scan(&handle); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if _, err := f.channels.Post(ctx, f.org, alice, channels.PostParams{
		Entity: d.Ref(), Body: "@" + handle + " can you look at this",
	}); err != nil {
		t.Fatalf("comment: %v", err)
	}
	f.drain(t)

	inbox, _ := f.svc.Inbox(ctx, f.bob, false, 50)
	if len(inbox) != 1 {
		t.Fatalf("%d notifications for one comment that both mentions and concerns him: %+v",
			len(inbox), summarise(inbox))
	}
	if inbox[0].Reason != notify.ReasonMentioned {
		t.Errorf("reason = %q, want mentioned", inbox[0].Reason)
	}
}

// A plain edit is not worth telling anybody about. Doing so for every save is
// the noise that makes people stop reading.
func TestAPlainEditDoesNotNotify(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindDoc, Title: "Runbook", Body: "v1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.svc.Watch(ctx, f.bob, d.Ref(), notify.WatchManual); err != nil {
		t.Fatalf("watch: %v", err)
	}
	body := "v2"
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{Body: &body}); err != nil {
		t.Fatalf("update: %v", err)
	}
	f.drain(t)

	if got, _ := f.svc.Inbox(ctx, f.bob, false, 50); len(got) != 0 {
		t.Fatalf("a typo fix notified %d people: %+v", len(got), summarise(got))
	}
}

// Quiet hours hold email rather than dropping it, and the window wraps
// midnight, which is the normal case for a night shift.
func TestQuietHoursWrapMidnight(t *testing.T) {
	from, to := 22*60, 7*60
	p := notify.Prefs{QuietFrom: &from, QuietTo: &to, Timezone: "UTC"}

	cases := []struct {
		hour int
		want bool
	}{{23, true}, {2, true}, {6, true}, {7, false}, {12, false}, {21, false}, {22, true}}
	for _, tc := range cases {
		at := time.Date(2026, 8, 13, tc.hour, 0, 0, 0, time.UTC)
		if got := p.Quiet(at); got != tc.want {
			t.Errorf("at %02d:00 quiet = %v, want %v", tc.hour, got, tc.want)
		}
	}

	// No window set means never quiet, rather than always.
	if (notify.Prefs{Timezone: "UTC"}).Quiet(time.Now()) {
		t.Error("somebody with no quiet hours was treated as always quiet")
	}
}

// Email goes out immediately only for people who asked for it, and the digest
// covers everybody else without repeating what has already been sent.
func TestEmailHonoursThePreference(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	if err := f.svc.SetPrefs(ctx, f.bob, notify.Prefs{
		Email: notify.EmailImmediate, Timezone: "UTC", DigestAt: 480,
	}); err != nil {
		t.Fatalf("prefs: %v", err)
	}

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Urgent job",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{AssigneeID: &f.bob},
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	f.drain(t)

	if len(f.mail.sent) != 1 {
		t.Fatalf("%d emails sent, want 1", len(f.mail.sent))
	}
	if !contains(f.mail.sent[0].body, "Urgent job") {
		t.Errorf("the email does not name the job: %q", f.mail.sent[0].body)
	}
	// It has to carry a real address, or nobody can act on it from their mail
	// client and the whole thing is a nudge to go and look.
	if !contains(f.mail.sent[0].body, "https://hearth.test/documents/"+d.ID) {
		t.Errorf("the email has no link back: %q", f.mail.sent[0].body)
	}

	// Nothing pending now, so a second send is silent rather than a repeat.
	before := len(f.mail.sent)
	f.drain(t)
	if len(f.mail.sent) != before {
		t.Errorf("the same notification was emailed twice")
	}

	// Somebody set to off gets nothing at all.
	if err := f.svc.SetPrefs(ctx, f.bob, notify.Prefs{
		Email: notify.EmailOff, Timezone: "UTC", DigestAt: 480,
	}); err != nil {
		t.Fatalf("prefs: %v", err)
	}
	if _, err := f.docs.Update(ctx, f.org, alice, d.ID, docs.UpdateParams{
		Task: &docs.TaskPatch{Status: ptr(docs.StatusInProgress)},
	}); err != nil {
		t.Fatalf("status: %v", err)
	}
	f.drain(t)
	if len(f.mail.sent) != before {
		t.Errorf("somebody with email off was sent %d", len(f.mail.sent)-before)
	}
	// The inbox still has it, which is the point: off means no email, not no
	// notification.
	if got, _ := f.svc.Inbox(ctx, f.bob, false, 50); len(got) < 2 {
		t.Errorf("turning email off also stopped the inbox: %d rows", len(got))
	}
}

// A time zone the runtime cannot resolve is refused, because accepting it means
// every quiet-hours comparison silently falls back to UTC.
func TestAnUnknownTimeZoneIsRefused(t *testing.T) {
	f := setup(t)
	err := f.svc.SetPrefs(context.Background(), f.alice, notify.Prefs{
		Email: notify.EmailDigest, Timezone: "Mars/Olympus_Mons", DigestAt: 480,
	})
	if err == nil {
		t.Fatal("an unknown time zone was accepted")
	}
}

func summarise(list []*notify.Notification) []string {
	out := make([]string, len(list))
	for i, n := range list {
		out[i] = n.Reason + ": " + n.Sentence()
	}
	return out
}

func contains(h, n string) bool { return strings.Contains(h, n) }

func ptr[T any](v T) *T { return &v }
