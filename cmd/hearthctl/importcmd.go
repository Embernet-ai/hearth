package main

// Importing a whole ClickUp workspace.
//
// A separate command rather than a button, because this is a migration: it runs
// once, it runs long, and it wants to be watchable. A browser request that
// walks 167 lists and writes 1,132 documents is a request that times out behind
// a proxy somebody has to go and find, and re-running it from a form is how you
// end up unsure whether it half-finished.
//
// Safe to run twice. Every task is matched on its ClickUp id, so a second run
// updates what it already brought in rather than duplicating it, which is what
// makes "run it again and see" a reasonable thing to do at 11pm.
//
//	hearthctl import-clickup -token pk_... [-dry-run] [-space Ops]

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/importer/clickup"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

func runImportClickUp(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("import-clickup", flag.ExitOnError)
	token := envFlag(fs, "token", "CLICKUP_TOKEN", "ClickUp personal API token (pk_...), or CLICKUP_TOKEN")
	only := fs.String("space", "",
		"import only this ClickUp space, by name. Empty means all of them")
	dry := fs.Bool("dry-run", false,
		"walk the workspace and report what would be imported, writing nothing")
	dsn := envFlag(fs, "dsn", "HEARTH_DATABASE_URL", "Postgres connection string, or HEARTH_DATABASE_URL")
	people := fs.Bool("people", true,
		"create an account for each workspace member, so assignees resolve")
	withDocs := fs.Bool("docs", true,
		"also import the workspace's documents as wiki pages")
	withComments := fs.Bool("comments", false,
		"also import task comments. One request per task, so on a large "+
			"workspace this is the slowest part by far")
	archived := fs.Bool("archived", false,
		"include archived folders and lists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*token) == "" {
		return errors.New("a ClickUp token is required: -token pk_... or CLICKUP_TOKEN")
	}

	client := clickup.NewClient(*token)
	client.Archived = *archived

	// The walk happens first either way. On a dry run it is the whole job; on a
	// real one it is the thing that proves the token works before anything is
	// written.
	lists, err := client.Lists(ctx)
	if err != nil {
		return err
	}
	bySpace := map[string]int{}
	listsPerSpace := map[string]int{}
	for _, l := range lists {
		if *only != "" && !strings.EqualFold(l.Space, *only) {
			continue
		}
		bySpace[l.Space] += l.Tasks
		listsPerSpace[l.Space]++
	}
	names := make([]string, 0, len(bySpace))
	for s := range bySpace {
		names = append(names, s)
	}
	sort.Strings(names)

	total, totalLists := 0, 0
	fmt.Println("workspace:")
	for _, n := range names {
		fmt.Printf("  %-24s %3d lists  ~%4d tasks  -> %s\n",
			n, listsPerSpace[n], bySpace[n], kindFor(n))
		total += bySpace[n]
		totalLists += listsPerSpace[n]
	}
	fmt.Printf("  %-24s %3d lists  ~%4d tasks\n", "TOTAL", len(lists), total)

	if *dry {
		fmt.Println("\ndry run: nothing was written")
		return nil
	}

	// Deliberately not config.Load(): that insists on object storage, mail and
	// the rest, and an import touches none of them. Requiring a valid S3
	// endpoint to write rows to Postgres is how a migration gets blocked on a
	// setting nobody needs.
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("a database is required: -dsn or HEARTH_DATABASE_URL")
	}
	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()

	az := authz.New(pools.Web)
	store := importStore{
		Store: docs.New(pools.Web, az),
		plant: plant.New(pools.Web, az),
		// secure=false: this never issues a cookie, it only reads the
		// directory. The flag decides the session cookie's prefix and nothing
		// an import touches.
		auth:   auth.New(pools.Web, az, false),
		time:   timelog.New(pools.Web, az),
		chat:   channels.New(pools.Web, az),
		notify: notify.New(pools.Web),
	}

	orgID, principalID, err := soleOwner(ctx, pools)
	if err != nil {
		return err
	}
	// The full subject set, not just the principal. Grants can hang off the
	// org and off channel membership as well as off the person, and an import
	// running with a narrower set than the owner actually has would be refused
	// on documents that owner can plainly edit.
	actor, err := az.SubjectsFor(ctx, orgID, principalID)
	if err != nil {
		return fmt.Errorf("resolve the owner's subjects: %w", err)
	}

	fmt.Printf("\nimporting as %s into org %s\n\n", actor.Principal(), orgID)

	if *people {
		// Before the tasks, so an assignee has somebody to resolve to.
		//
		// A provisioned account carries a credential row with an empty password
		// hash, which exists so the address resolves. OIDC sign-in adopts an
		// existing account by that address, so somebody created here and signing
		// in through Vord tomorrow lands on this principal with these tasks
		// already assigned, rather than on a second empty one.
		n, probs, err := clickup.ProvisionMembers(ctx, store, orgID, filtered{client, ""})
		if err != nil {
			return fmt.Errorf("provision members: %w", err)
		}
		fmt.Println(fmt.Sprintf("  people: %d accounts ready", n))
		for _, p := range probs {
			fmt.Println("    " + p)
		}
		fmt.Println()
	}
	res, err := clickup.RunWorkspace(ctx, store, orgID, actor, filtered{client, *only},
		clickup.Options{
			Progress: func(done, total int, l clickup.List, r *clickup.Result) {
				where := l.Space
				if l.Folder != "" {
					where += "/" + l.Folder
				}
				fmt.Println(fmt.Sprintf("  [%3d/%3d] %-46s +%d",
					done, total, trim(where+"/"+l.Name, 46), r.Created+r.Updated))
			},
		})
	if err != nil {
		return err
	}

	if *withDocs && *only == "" {
		fmt.Println()
		teamID, terr := client.TeamID(ctx)
		if terr != nil {
			return terr
		}
		dres, derr := clickup.RunDocs(ctx, store, orgID, actor, client, teamID, clickup.Options{})
		switch {
		case derr != nil:
			fmt.Println("  docs: " + derr.Error())
		default:
			fmt.Println(fmt.Sprintf("  docs: created %d  updated %d  failed %d",
				dres.Created, dres.Updated, dres.Failed))
			for _, p := range dres.Problems {
				fmt.Println("    " + p)
			}
		}
	}

	if *withComments {
		fmt.Println()
		fmt.Println(fmt.Sprintf("  comments: %d tasks to read, one request each", len(res.Documents)))
		people := map[string]string{}
		if list, perr := store.People(ctx, orgID); perr == nil {
			for _, p := range list {
				if p.Email != "" {
					people[strings.ToLower(p.Email)] = p.PrincipalID
				}
			}
		}
		last := 0
		posted, probs := clickup.RunComments(ctx, store, orgID, actor, client,
			res.Documents, people, principalID, func(done, total int) {
				// Every fiftieth, because a line per task is 1,132 lines of
				// noise and no more informative than a count.
				if done-last >= 50 || done == total {
					last = done
					fmt.Println(fmt.Sprintf("    %d/%d", done, total))
				}
			})
		fmt.Println(fmt.Sprintf("  comments: %d posted", posted))
		for _, p := range probs {
			fmt.Println("    " + p)
		}
	}

	fmt.Println("done:")
	spaces := make([]string, 0, len(res.BySpace))
	for s := range res.BySpace {
		spaces = append(spaces, s)
	}
	sort.Strings(spaces)
	for _, s := range spaces {
		r := res.BySpace[s]
		fmt.Printf("  %-24s created %4d  updated %4d  failed %3d  as %s\n",
			s, r.Created, r.Updated, r.Failed, r.Kind)
	}
	fmt.Printf("  %-24s created %4d  updated %4d  failed %3d\n",
		"TOTAL", res.Created, res.Updated, res.Failed)

	if len(res.Problems) > 0 {
		fmt.Println("\nproblems:")
		for _, p := range res.Problems {
			fmt.Println("  " + p)
		}
	}
	// A failure count is not an error: the run did what it could and said so.
	// Exiting non-zero would make a CronJob retry a migration that mostly
	// worked.
	return nil
}

func kindFor(space string) string {
	if k, ok := clickup.DefaultKindBySpace[space]; ok {
		return k
	}
	return docs.KindTask
}

// filtered narrows the walk to one space, so a migration can be rehearsed on
// the smallest one before being trusted with all eight.
type filtered struct {
	c    *clickup.Client
	only string
}

func (f filtered) Lists(ctx context.Context) ([]clickup.List, error) {
	all, err := f.c.Lists(ctx)
	if err != nil || f.only == "" {
		return all, err
	}
	var out []clickup.List
	for _, l := range all {
		if strings.EqualFold(l.Space, f.only) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f filtered) TasksIn(ctx context.Context, listID string) ([]clickup.Task, error) {
	return f.c.TasksIn(ctx, listID)
}

func (f filtered) Members(ctx context.Context) ([]clickup.Person, error) {
	return f.c.Members(ctx)
}

// soleOwner finds the organisation and the principal to import as.
//
// An import has no signed-in human behind it, and every write in this
// application goes through the same grant check a person's request does. So it
// runs as the owner: the account that first-run setup created, which is the one
// principal guaranteed to hold a grant at the root.
//
// Refuses rather than guesses if there is more than one organisation. A
// migration that picks the wrong tenant is not something to discover later.
// Personal playgrounds (orgs.playground_of) are not tenants to import into,
// and are not counted.
func soleOwner(ctx context.Context, pools *pg.Pools) (string, string, error) {
	var orgs int
	if err := pools.Web.QueryRow(ctx, `SELECT count(*) FROM orgs WHERE playground_of IS NULL`).Scan(&orgs); err != nil {
		return "", "", fmt.Errorf("count orgs: %w", err)
	}
	if orgs != 1 {
		return "", "", fmt.Errorf(
			"expected exactly one organisation, found %d; refusing to guess which to import into", orgs)
	}

	var orgID, principalID string
	err := pools.Web.QueryRow(ctx, `
		SELECT p.org_id::text, p.id::text
		FROM principals p
		JOIN local_credentials lc ON lc.principal_id = p.id
		JOIN orgs o ON o.id = p.org_id AND o.playground_of IS NULL
		ORDER BY p.created_at
		LIMIT 1`).Scan(&orgID, &principalID)
	if err != nil {
		return "", "", fmt.Errorf("find the owner: %w", err)
	}
	return orgID, principalID, nil
}

// importStore joins the document service to the space service, the same pairing
// the web handler makes. Duplicated rather than exported from internal/web,
// because a command importing a package whose job is to serve HTTP would drag
// the whole server in to get four lines of glue.
type importStore struct {
	docs.Store
	plant  plant.Store
	auth   *auth.Service
	time   timelog.Store
	chat   *channels.Service
	notify *notify.Service
}

// People lets the importer turn a ClickUp assignee into a real principal
// rather than a line of prose in the body.
func (s importStore) ProvisionPerson(ctx context.Context, orgID, externalID, displayName, email string) (string, error) {
	return s.auth.ProvisionUser(ctx, orgID, externalID, displayName, email)
}

func (s importStore) People(ctx context.Context, orgID string) ([]clickup.Person, error) {
	list, err := s.auth.People(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]clickup.Person, 0, len(list))
	for _, p := range list {
		out = append(out, clickup.Person{
			PrincipalID: p.PrincipalID, DisplayName: p.DisplayName, Email: p.Email,
		})
	}
	return out, nil
}

func (s importStore) CreateSpace(ctx context.Context, orgID string, actor core.Subjects,
	name, kind string, parentID *string) (string, error) {

	sp, err := s.plant.CreateSpace(ctx, orgID, actor, name, kind, parentID)
	if err != nil {
		return "", err
	}
	return sp.ID, nil
}

// trim shortens a path for a fixed-width progress line, keeping the end -- the
// list name is what identifies it, and the space is already obvious from the
// lines around it.
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-(n-3):]
}

// LogTime records effort the source had already tracked.
func (s importStore) LogTime(ctx context.Context, orgID string, actor core.Subjects,
	docID string, minutes int, on time.Time, note string) error {

	if s.time == nil {
		return errTimelogMissing
	}
	_, err := s.time.Log(ctx, orgID, actor, docID, minutes, on, note)
	return err
}

// SetEstimate carries the source's estimate onto the task.
func (s importStore) SetEstimate(ctx context.Context, orgID string, actor core.Subjects,
	docID string, minutes *int) error {

	if s.time == nil {
		return errTimelogMissing
	}
	return s.time.SetEstimate(ctx, orgID, actor, docID, minutes)
}

// errTimelogMissing should never fire here: hearthctl always builds one.
var errTimelogMissing = errors.New("hearthctl: time tracking is not configured")

// AddChecklistItem rebuilds a step the source carried, returning its id.
func (s importStore) AddChecklistItem(ctx context.Context, orgID string, actor core.Subjects,
	docID, text string) (string, error) {

	it, err := s.Store.AddChecklistItem(ctx, orgID, actor, docID, text, "")
	if err != nil {
		return "", err
	}
	return it.ID, nil
}

// PostComment carries a conversation that already happened.
func (s importStore) PostComment(ctx context.Context, orgID, principalID string,
	docID, body string, at time.Time, parentID string) (string, error) {

	if s.chat == nil {
		return "", errNoChat
	}
	return s.chat.PostImported(ctx, orgID, principalID,
		core.Ref{Type: core.EntityDocument, ID: docID}, body, at, parentID)
}

// Watch records that somebody was following this where it came from.
func (s importStore) Watch(ctx context.Context, principalID, docID string) error {
	if s.notify == nil {
		return nil
	}
	return s.notify.Watch(ctx, principalID, core.Ref{Type: core.EntityDocument, ID: docID}, "manual")
}

// errNoChat is returned when there is nowhere to put a comment.
var errNoChat = errors.New("hearthctl: messaging is not configured")

// ClearImportedTime removes the entries a previous import wrote, so a re-run
// replaces the hours rather than adding a second copy of them.
func (s importStore) ClearImportedTime(ctx context.Context, orgID, docID, note string) error {
	if s.time == nil {
		return nil
	}
	return s.time.DeleteImported(ctx, orgID, docID, note)
}

// SetAssignees records everybody a task was assigned to, beyond the one the
// task field holds.
func (s importStore) SetAssignees(ctx context.Context, docID string, principalIDs []string, primary string) error {
	return s.Store.SetAssignees(ctx, docID, principalIDs, primary)
}

// FindSpaceByName finds a space wherever somebody has filed it.
func (s importStore) FindSpaceByName(ctx context.Context, orgID, name string) (string, error) {
	if s.plant == nil {
		return "", nil
	}
	return s.plant.FindSpaceByName(ctx, orgID, name)
}
