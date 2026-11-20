package timelog_test

// Tags, against a real Postgres. The interesting checks are the ones that
// protect the whole point of the feature: applying a tag twice must not
// create a second row (that is what makes BatchTag safe to re-run), and
// tagging an entry must obey the same edit boundary Correct already does.

import (
	"context"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/timelog"
)

func grant(t *testing.T, f *fx, principalID, capability string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		 VALUES ($1,$2,$3,$2) ON CONFLICT DO NOTHING`, f.org, principalID, capability); err != nil {
		t.Fatalf("grant %s: %v", capability, err)
	}
	t.Cleanup(func() {
		_, _ = f.pools.Web.Exec(context.Background(),
			`DELETE FROM capability_grants WHERE org_id=$1`, f.org)
	})
}

// Creating a tag is org taxonomy, not a per-entry action -- gated on People,
// and a second tag with the same name (any case) is refused rather than
// forking into two spellings of the same idea.
func TestCreatingATagNeedsThePeopleCapability(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.svc.CreateTag(ctx, f.org, f.alice, "Onsite", "worked at the client site"); err == nil {
		t.Fatal("created a tag without the People capability")
	}

	grant(t, f, f.alice, "people")
	tag, err := f.svc.CreateTag(ctx, f.org, f.alice, "Onsite", "worked at the client site")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tag.Name != "Onsite" || tag.Description != "worked at the client site" {
		t.Errorf("tag came back wrong: %+v", tag)
	}

	if _, err := f.svc.CreateTag(ctx, f.org, f.alice, "onsite", "a second spelling"); err != timelog.ErrTagExists {
		t.Errorf("expected ErrTagExists for a case-different duplicate, got %v", err)
	}

	tags, err := f.svc.ListTags(ctx, f.org)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("expected exactly one tag to exist, got %d: %+v", len(tags), tags)
	}
}

// Applying a tag twice is a no-op, not a second row -- the whole mechanism
// BatchTag relies on to be safe against a week that is only partly tagged.
func TestTaggingAnEntryIsIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	grant(t, f, f.alice, "people")

	tag, err := f.svc.CreateTag(ctx, f.org, f.alice, "Remote", "worked from home")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	entry, err := f.svc.LogOwn(ctx, f.org, alice, "", 60, time.Now(), "no job, just an hour")
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	for range 2 {
		if err := f.svc.TagEntry(ctx, f.org, alice, entry.ID, tag.ID); err != nil {
			t.Fatalf("tag entry: %v", err)
		}
	}
	tags, err := f.svc.EntryTags(ctx, f.org, entry.ID)
	if err != nil {
		t.Fatalf("entry tags: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("applying the same tag twice produced %d rows, not one: %+v", len(tags), tags)
	}

	if err := f.svc.UntagEntry(ctx, f.org, alice, entry.ID, tag.ID); err != nil {
		t.Fatalf("untag: %v", err)
	}
	tags, err = f.svc.EntryTags(ctx, f.org, entry.ID)
	if err != nil {
		t.Fatalf("entry tags after untag: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("untag left %d tags behind: %+v", len(tags), tags)
	}
}

// Tagging obeys the exact boundary Correct already does: the entry's own
// author may tag it, a stranger may not, even with a valid tag ID.
func TestTaggingRespectsTheSameEditPermissionAsCorrect(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	grant(t, f, f.alice, "people")

	tag, err := f.svc.CreateTag(ctx, f.org, f.alice, "Travel", "on the road for a client")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	entry, err := f.svc.LogOwn(ctx, f.org, alice, "", 45, time.Now(), "")
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	bob := f.subj(t, f.bob)
	if err := f.svc.TagEntry(ctx, f.org, bob, entry.ID, tag.ID); err == nil {
		t.Error("a stranger tagged somebody else's entry")
	}
	if err := f.svc.TagEntry(ctx, f.org, alice, entry.ID, tag.ID); err != nil {
		t.Errorf("its own author could not tag it: %v", err)
	}
}

// BatchTag is self-service only, and skips whatever a week already carries --
// the actual point of it existing, since re-running it must not be something
// somebody has to think twice about.
func TestBatchTagOnlyTagsYourOwnHistoryAndSkipsAlreadyTagged(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	grant(t, f, f.alice, "people")

	tag, err := f.svc.CreateTag(ctx, f.org, f.alice, "Onsite", "")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}

	monday := time.Now().Truncate(24*time.Hour).AddDate(0, 0, -int(time.Now().Weekday()))
	var entries []*timelog.Entry
	for i := range 5 {
		e, err := f.svc.LogOwn(ctx, f.org, alice, "", 60, monday.AddDate(0, 0, i), "day "+string(rune('A'+i)))
		if err != nil {
			t.Fatalf("log day %d: %v", i, err)
		}
		entries = append(entries, e)
	}
	// Two of the five are already tagged by hand -- the realistic case this
	// exists for: somebody tagged Monday and Tuesday one at a time, then wants
	// the rest of the week done in one click.
	if err := f.svc.TagEntry(ctx, f.org, alice, entries[0].ID, tag.ID); err != nil {
		t.Fatalf("pre-tag day 0: %v", err)
	}
	if err := f.svc.TagEntry(ctx, f.org, alice, entries[1].ID, tag.ID); err != nil {
		t.Fatalf("pre-tag day 1: %v", err)
	}

	bob := f.subj(t, f.bob)
	if _, err := f.svc.BatchTag(ctx, f.org, bob, f.alice, tag.ID, monday, monday.AddDate(0, 0, 7)); err == nil {
		t.Error("bob batch-tagged alice's history")
	}

	n, err := f.svc.BatchTag(ctx, f.org, alice, f.alice, tag.ID, monday, monday.AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("batch tag: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 newly-tagged entries (the 2 already tagged should be skipped), got %d", n)
	}

	for i, e := range entries {
		tags, err := f.svc.EntryTags(ctx, f.org, e.ID)
		if err != nil {
			t.Fatalf("entry tags day %d: %v", i, err)
		}
		if len(tags) != 1 {
			t.Errorf("day %d: expected exactly one tag after batch, got %d", i, len(tags))
		}
	}

	// Running it again over the same week changes nothing -- every entry
	// already carries the tag, so this must report zero, not re-count them.
	n, err = f.svc.BatchTag(ctx, f.org, alice, f.alice, tag.ID, monday, monday.AddDate(0, 0, 7))
	if err != nil {
		t.Fatalf("batch tag again: %v", err)
	}
	if n != 0 {
		t.Errorf("re-running batch tag over an already-tagged week reported %d new tags, want 0", n)
	}
}
