package docs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/docs"
)

// A wrong upload can be taken off a document and brought back: removed, not
// deleted. Only somebody who can edit the document may do either, and only for
// a file that is actually attached to that document.
func TestAnAttachmentIsRemovedAndRestored(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	d, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "With a file"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.svc.Create(ctx, f.org, alice, docs.CreateParams{Kind: docs.KindDoc, Title: "Without it"})
	if err != nil {
		t.Fatal(err)
	}
	fileID := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx, `
		INSERT INTO files (id, org_id, storage_key, filename, content_type, size_bytes, sha256, uploaded_by)
		VALUES ($1,$2,$3,'wrong.mp4','video/mp4',9,decode(repeat('00',32),'hex'),$4)`,
		fileID, f.org, "test/"+fileID, f.alice); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pools.Web.Exec(context.Background(), `DELETE FROM files WHERE id=$1`, fileID) })
	if err := f.svc.Attach(ctx, f.org, alice, d.ID, fileID); err != nil {
		t.Fatal(err)
	}

	count := func() (shown, removed int) {
		t.Helper()
		a, err := f.svc.Attachments(ctx, f.org, alice, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		r, err := f.svc.RemovedAttachments(ctx, f.org, alice, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(a), len(r)
	}

	if err := f.svc.RemoveAttachment(ctx, f.org, f.subj(t, f.dave), d.ID, fileID); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("somebody who cannot edit removed a file: %v", err)
	}
	if err := f.svc.RemoveAttachment(ctx, f.org, alice, other.ID, fileID); err != nil {
		t.Fatal(err)
	}
	if shown, _ := count(); shown != 1 {
		t.Fatal("removing through a document the file is not attached to took it off anyway")
	}

	if err := f.svc.RemoveAttachment(ctx, f.org, alice, d.ID, fileID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemoveAttachment(ctx, f.org, alice, d.ID, fileID); err != nil {
		t.Fatalf("removing twice should be a no-op: %v", err)
	}
	if shown, removed := count(); shown != 0 || removed != 1 {
		t.Fatalf("after remove: shown=%d removed=%d, want 0 and 1", shown, removed)
	}

	if err := f.svc.RestoreAttachment(ctx, f.org, alice, d.ID, fileID); err != nil {
		t.Fatal(err)
	}
	if shown, removed := count(); shown != 1 || removed != 0 {
		t.Fatalf("after restore: shown=%d removed=%d, want 1 and 0", shown, removed)
	}
}
