package main

// Removing documents by id, through the same service function the web page's
// delete button calls (docs.Service.Delete: a soft delete, with its event and its
// audit entry in one transaction), as the organisation's longest standing super
// admin. It is the way to take a known set of documents out, such as the copies
// an import made twice, when nobody with admin on them is signed in. Like
// crm-delete it grants that admin admin on each document first, which leaves a
// grant row saying who decided it.
//
// It is built so it cannot delete the wrong thing: every id must be a live
// document that carries an external reference from the named source, so the
// originals, which carry none, are refused, and the whole list is checked before
// the first one goes. Nothing is deleted without -go.
//
//	hearthctl doc-delete -source twenty -ids ID[,ID]            what would be removed
//	hearthctl doc-delete -source twenty -ids ID[,ID] -go        remove them

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

// splitIDs reads a comma separated list, dropping blanks and repeats and keeping
// the order given.
func splitIDs(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range strings.Split(s, ",") {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func runDocDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doc-delete", flag.ExitOnError)
	dsn := envFlag(fs, "dsn", "HEARTH_DATABASE_URL", "Postgres connection string, or HEARTH_DATABASE_URL")
	idList := fs.String("ids", "", "comma separated document ids to remove")
	source := fs.String("source", "", "the external reference source every document must carry (twenty, clickup, ...)")
	kind := fs.String("kind", "", "the document kind every id must be (task, doc, ...); empty accepts any")
	do := fs.Bool("go", false, "remove them; without it nothing is deleted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ids := splitIDs(*idList)
	if len(ids) == 0 {
		return errors.New("doc-delete needs -ids")
	}
	if strings.TrimSpace(*source) == "" {
		return errors.New("doc-delete needs -source: a document with no reference from it is never removed, which is what keeps the originals")
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}
	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()
	az := authz.New(pools.Web)
	orgID, _, err := soleOwner(ctx, pools)
	if err != nil {
		return err
	}

	// Check the whole list first. One id that is not what it was said to be stops
	// the run before anything is removed.
	type target struct{ id, kind, title string }
	var plan []target
	for _, id := range ids {
		var t target
		t.id = id
		err := pools.Web.QueryRow(ctx, `
			SELECT d.kind, d.title FROM documents d
			WHERE d.id::text=$1 AND d.org_id=$2 AND d.deleted_at IS NULL
			  AND EXISTS (SELECT 1 FROM external_refs r
			              WHERE r.org_id=d.org_id AND r.entity_id=d.id AND r.source=$3)`,
			id, orgID, *source).Scan(&t.kind, &t.title)
		if err != nil {
			return fmt.Errorf("document %s is not a live document with a %q reference, so nothing was removed: %w", id, *source, err)
		}
		if *kind != "" && t.kind != *kind {
			return fmt.Errorf("document %s is a %s, not a %s, so nothing was removed", id, t.kind, *kind)
		}
		plan = append(plan, t)
	}
	for _, t := range plan {
		fmt.Printf("%s %s %s\n", map[bool]string{true: "removing", false: "would remove"}[*do], t.id, t.title)
	}
	if !*do {
		fmt.Printf("%d documents, nothing removed; add -go\n", len(plan))
		return nil
	}

	var principalID string
	if err := pools.Web.QueryRow(ctx, `SELECT principal_id::text FROM capability_grants
		WHERE org_id=$1 AND capability='super_admin' ORDER BY granted_at LIMIT 1`, orgID).Scan(&principalID); err != nil {
		return fmt.Errorf("find a super admin to act as: %w", err)
	}
	actor, err := az.SubjectsFor(ctx, orgID, principalID)
	if err != nil {
		return fmt.Errorf("resolve the owner's subjects: %w", err)
	}
	set, err := backendSet(ctx, pools.Web, az)
	if err != nil {
		return err
	}
	var ids2 []docRef
	for _, t := range plan {
		ids2 = append(ids2, docRef{t.id, t.title})
	}
	if err := deleteDocuments(ctx, set.Docs, az, orgID, principalID, actor, ids2, os.Stdout); err != nil {
		return err
	}
	fmt.Printf("%d documents removed\n", len(plan))
	return nil
}

type docRef struct{ id, title string }

// deleteDocuments deletes each document through the store it is given, after the admin grant that records who decided it. In
// the EmberOps back end that store removes the EmberOps record as well.
func deleteDocuments(ctx context.Context, ds docs.Store, az admins, orgID, principalID string, actor core.Subjects, list []docRef, w io.Writer) error {
	for _, t := range list {
		if err := az.Grant(ctx, orgID, core.Ref{Type: core.EntityDocument, ID: t.id},
			core.Subject{Kind: core.SubjectPrincipal, ID: principalID}, core.AccessAdmin, principalID); err != nil {
			return fmt.Errorf("grant admin on document %s: %w", t.id, err)
		}
		if err := ds.Delete(ctx, orgID, actor, t.id); err != nil {
			return fmt.Errorf("delete document %s: %w", t.id, err)
		}
		fmt.Fprintf(w, "deleted document %s %s\n", t.id, t.title)
	}
	return nil
}
