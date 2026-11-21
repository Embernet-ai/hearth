package main

// Taking a space out of the tree, through the same service functions the pages
// call, as the organisation's longest standing super admin.
//
// A space that stands for work we are not doing is closed out and removed, and
// the work in it is kept: every open task is cancelled (an ordinary status
// change, with its history), every document is taken out of the space and left
// where Tasks and search still find it, and then the empty space is deleted,
// which writes its own audit row. Nothing here deletes a document. Like
// doc-delete it grants that admin admin on each document first, which leaves a
// grant row saying who decided it.
//
// A space with a child space is refused: say which one you mean, one at a time.
// Nothing is changed without -go.
//
//	hearthctl space-remove -space ID            what would happen
//	hearthctl space-remove -space ID -go        do it

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
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runSpaceRemove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("space-remove", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres connection string, or HEARTH_DATABASE_URL")
	space := fs.String("space", "", "the id of the space to close out and remove")
	do := fs.Bool("go", false, "do it; without it nothing is changed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*space) == "" {
		return errors.New("space-remove needs -space")
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

	var name, kind string
	var children int
	if err := pools.Web.QueryRow(ctx, `
		SELECT name, kind, (SELECT count(*) FROM spaces c WHERE c.parent_id = s.id)
		  FROM spaces s WHERE s.id::text = $1 AND s.org_id = $2`, *space, orgID).Scan(&name, &kind, &children); err != nil {
		return fmt.Errorf("space %s is not in this organisation: %w", *space, err)
	}
	if children > 0 {
		return fmt.Errorf("space %q holds %d space(s); remove those first, one at a time", name, children)
	}

	type item struct{ id, kind, title, status string }
	rows, err := pools.Web.Query(ctx, `
		SELECT d.id::text, d.kind, d.title, COALESCE(t.status, '')
		  FROM documents d LEFT JOIN task_fields t ON t.document_id = d.id
		 WHERE d.space_id::text = $1 AND d.org_id = $2 AND d.deleted_at IS NULL
		 ORDER BY d.created_at`, *space, orgID)
	if err != nil {
		return err
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.kind, &it.title, &it.status); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	fmt.Printf("space %q (%s), %d item(s)\n", name, kind, len(items))
	open := func(st string) bool {
		return st == docs.StatusTodo || st == docs.StatusInProgress || st == docs.StatusBlocked
	}
	for _, it := range items {
		act := "unfile"
		if open(it.status) {
			act = "cancel, unfile"
		}
		fmt.Printf("%s  %s  %s  [%s]  %s\n", map[bool]string{true: "doing", false: "would"}[*do], it.id, it.title, it.status, act)
	}
	if !*do {
		fmt.Println("nothing changed; add -go")
		return nil
	}

	var principalID string
	if err := pools.Web.QueryRow(ctx, `SELECT principal_id::text FROM capability_grants
		WHERE org_id=$1 AND capability='super_admin' ORDER BY granted_at LIMIT 1`, orgID).Scan(&principalID); err != nil {
		return fmt.Errorf("find a super admin to act as: %w", err)
	}
	actor, err := az.SubjectsFor(ctx, orgID, principalID)
	if err != nil {
		return fmt.Errorf("resolve the admin's subjects: %w", err)
	}
	set, err := backendSet(ctx, pools.Web, az)
	if err != nil {
		return err
	}
	var work []spaceItem
	for _, it := range items {
		work = append(work, spaceItem{it.id, it.kind, it.title, it.status})
	}
	if err := closeOutSpace(ctx, set.Docs, set.Plant, az, orgID, principalID, actor, *space, work, os.Stdout); err != nil {
		return err
	}
	fmt.Printf("space %q removed, %d item(s) kept\n", name, len(items))
	return nil
}

type spaceItem struct{ id, kind, title, status string }

// admins is the one grant a command needs to make before it may change a document: admin on it, which leaves a row saying who
// decided it.
type admins interface {
	Grant(ctx context.Context, orgID string, ref core.Ref, subject core.Subject, level core.AccessLevel, grantedBy string) error
}

// closeOutSpace cancels the open tasks in a space, takes every document out of it, and deletes the empty space, through the
// stores it is given: the Postgres services in the standalone back end, the EmberOps stores in the other, so the EmberOps copies
// follow.
func closeOutSpace(ctx context.Context, ds docs.Store, ps plant.Store, az admins, orgID, principalID string, actor core.Subjects,
	space string, items []spaceItem, w io.Writer) error {
	open := func(st string) bool {
		return st == docs.StatusTodo || st == docs.StatusInProgress || st == docs.StatusBlocked
	}
	cancelled := docs.StatusCancelled
	for _, it := range items {
		if err := az.Grant(ctx, orgID, core.Ref{Type: core.EntityDocument, ID: it.id},
			core.Subject{Kind: core.SubjectPrincipal, ID: principalID}, core.AccessAdmin, principalID); err != nil {
			return fmt.Errorf("grant admin on %s: %w", it.id, err)
		}
		if open(it.status) {
			if _, err := ds.Update(ctx, orgID, actor, it.id, docs.UpdateParams{Task: &docs.TaskPatch{Status: &cancelled}}); err != nil {
				return fmt.Errorf("cancel %s: %w", it.id, err)
			}
			fmt.Fprintf(w, "cancelled %s %s\n", it.id, it.title)
		}
		if err := ps.MoveDocument(ctx, orgID, actor, it.id, nil); err != nil {
			return fmt.Errorf("take %s out of the space: %w", it.id, err)
		}
	}
	if err := ps.DeleteSpace(ctx, orgID, actor, space); err != nil {
		return fmt.Errorf("delete space %s: %w", space, err)
	}
	return nil
}
