package channels_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/channels"
)

// A contractor account can mention only the people it may be shown: themselves
// and whoever holds a grant of their own on a folder the account can open. A
// mention of anybody else stays plain text, the same as a handle nobody has, so
// a mention is not a way to learn who else is here. Everybody else mentions
// everybody, as they always did.
func TestAContractorMentionsOnlyThePeopleItMayBeShown(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	carol := uuid.NewString()
	space := uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO principals (id, org_id, kind, display_name, handle, contractor) VALUES ($1,$2,'user','Carol',$3,true)`,
		carol, f.org, "carol-"+carol[:8])
	exec(`INSERT INTO spaces (id, org_id, name, kind, created_by) VALUES ($1,$2,'Folder','folder',$3)`, space, f.org, f.alice)
	t.Cleanup(func() { _, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM spaces WHERE org_id=$1`, f.org) })
	// The contractor and Bob hold the folder; Alice does not.
	for _, who := range []string{carol, f.bob} {
		exec(`INSERT INTO grants (id, org_id, entity_type, entity_id, subject_key, level, granted_by)
		      VALUES (gen_random_uuid(), $1, 'space', $2, $3, 'view', $4)`, f.org, space, "principal:"+who, f.alice)
	}

	room, err := f.svc.Create(ctx, f.org, carol, "Carol's room", "", channels.Public)
	if err != nil {
		t.Fatal(err)
	}
	mentionedPeople := func(author string, body string) map[string]bool {
		t.Helper()
		m, err := f.svc.Post(ctx, f.org, f.subj(t, author), channels.PostParams{ChannelID: room.ID, Body: body})
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		got := map[string]bool{}
		for _, x := range m.Mentions {
			got[x.Label] = true
		}
		return got
	}

	both := "@" + f.handle(t, f.bob) + " and @" + f.handle(t, f.alice)
	got := mentionedPeople(carol, both)
	if !got["Bob"] {
		t.Error("the contractor cannot mention somebody who holds the folder")
	}
	if got["Alice"] {
		t.Error("the contractor mentioned somebody who holds nothing she can open")
	}
	// An ordinary account mentions everybody, as before.
	if err := f.svc.Join(ctx, f.org, f.bob, room.ID); err != nil {
		t.Fatal(err)
	}
	got = mentionedPeople(f.bob, both)
	if !got["Alice"] || !got["Bob"] {
		t.Errorf("an ordinary account's mentions changed: %v", got)
	}
}
