package api_test

// The public API, webhooks, and the round trip that makes an export real.
//
// The three claims are that the API refuses what the interface refuses for the
// same caller, that every importer is safe to run twice, and that a full
// export re-imports into an empty instance. All three are about the boundary
// between this product and everything outside it, which is the boundary that
// decides whether anybody can leave.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Embernet-ai/hearth/internal/api"
	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/importer"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

type fx struct {
	svc     *api.Service
	docs    *docs.Service
	az      *authz.Service
	handler http.Handler
	pools   *pg.Pools
	org     string
	alice   string
	bob     string
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
	svc := api.New(pools.Web, az, d).WithTime(timelog.New(pools.Web, az))
	f := &fx{
		svc: svc, docs: d, az: az, handler: svc.Routes(), pools: pools,
		org: uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(),
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pools.Web.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'API Test')`, f.org, "ap-"+f.org[:8])
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Alice')`, f.alice, f.org)
	exec(`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Bob')`, f.bob, f.org)

	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			// The audit log refuses an ordinary delete, so it takes the one
			// deliberate way out. That function removes a whole org and
			// nothing finer, and nothing outside a fixture calls it.
			`SELECT audit_purge_org($1)`,
			`DELETE FROM webhook_deliveries WHERE org_id=$1`,
			`DELETE FROM webhooks WHERE org_id=$1`,
			`DELETE FROM api_tokens WHERE org_id=$1`,
			`DELETE FROM time_entries WHERE org_id=$1`,
			`DELETE FROM external_refs WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM entity_links WHERE org_id=$1`,
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

func (f *fx) subj(t *testing.T, p string) core.Subjects {
	t.Helper()
	s, err := f.az.SubjectsFor(context.Background(), f.org, p)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}
	return s
}

func (f *fx) token(t *testing.T, principal string, scopes ...string) string {
	t.Helper()
	tok, err := f.svc.Mint(context.Background(), f.org, principal, "test", scopes, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok.Secret
}

// call makes a request the way a client does.
func (f *fx) call(t *testing.T, token, method, path string, body any) (int, map[string]any, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	raw := rec.Body.String()
	var out map[string]any
	_ = json.Unmarshal([]byte(raw), &out)
	return rec.Code, out, raw
}

// -----------------------------------------------------------------------------

// The criterion: the API refuses what the interface refuses, for the same
// caller.
//
// Checked by asking both. Not by asserting that the API says no, which would
// pass on an API that says no to everything, but by comparing it against the
// service call the HTML page makes with the same subject set.
func TestTheAPIRefusesWhatTheInterfaceRefusesForTheSameCaller(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice, bob := f.subj(t, f.alice), f.subj(t, f.bob)

	private, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Alice's private work order",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	bobToken := f.token(t, f.bob, api.ScopeWrite)

	// The interface, for Bob.
	_, pageErr := f.docs.Get(ctx, f.org, bob, private.ID)
	// The API, for Bob.
	code, _, _ := f.call(t, bobToken, http.MethodGet, "/v1/documents/"+private.ID, nil)

	if (pageErr != nil) != (code == http.StatusNotFound) {
		t.Errorf("the interface said %v and the API answered HTTP %d.\n"+
			"They have to agree for the same caller, or the API is a way "+
			"around the permission model rather than another door into it.",
			pageErr, code)
	}
	if code != http.StatusNotFound {
		t.Errorf("the API returned HTTP %d for a document Bob was never granted", code)
	}

	// The write side, which is where a back door would actually be used.
	code, _, _ = f.call(t, bobToken, http.MethodPatch, "/v1/documents/"+private.ID,
		map[string]any{"title": "Renamed by Bob"})
	if code == http.StatusOK {
		t.Error("the API let Bob rename a document he cannot even see")
	}
	code, _, _ = f.call(t, bobToken, http.MethodDelete, "/v1/documents/"+private.ID, nil)
	if code == http.StatusNoContent {
		t.Error("the API let Bob delete a document he cannot even see")
	}

	// And the list, which is the one people forget.
	code, out, _ := f.call(t, bobToken, http.MethodGet, "/v1/documents?kind=work_order", nil)
	if code != http.StatusOK {
		t.Fatalf("list returned HTTP %d", code)
	}
	if list, _ := out["documents"].([]any); len(list) != 0 {
		t.Errorf("Bob's list has %d documents he was not granted", len(list))
	}

	// Once granted view, both agree that he can read and neither lets him
	// write. A permission model that only says no is not being tested.
	if err := f.az.Grant(ctx, f.org, private.Ref(),
		core.Subject{Kind: core.SubjectPrincipal, ID: f.bob}, core.AccessView, f.alice); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := f.docs.Get(ctx, f.org, bob, private.ID); err != nil {
		t.Fatalf("the interface refuses a granted read: %v", err)
	}
	if code, _, _ = f.call(t, bobToken, http.MethodGet, "/v1/documents/"+private.ID, nil); code != http.StatusOK {
		t.Errorf("the API refuses a granted read: HTTP %d", code)
	}
	if code, _, body := f.call(t, bobToken, http.MethodPatch, "/v1/documents/"+private.ID,
		map[string]any{"title": "Renamed"}); code == http.StatusOK {
		t.Errorf("view was enough to write through the API: %s", body)
	}
}

// A read token cannot write, whatever its owner may do.
func TestAReadTokenCannotWrite(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	d, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Alice can edit this",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	readOnly := f.token(t, f.alice, api.ScopeRead)

	if code, _, _ := f.call(t, readOnly, http.MethodGet, "/v1/documents/"+d.ID, nil); code != http.StatusOK {
		t.Errorf("a read token could not read: HTTP %d", code)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPatch, "/v1/documents/" + d.ID},
		{http.MethodDelete, "/v1/documents/" + d.ID},
		{http.MethodPost, "/v1/documents"},
	} {
		if code, _, _ := f.call(t, readOnly, tc.method, tc.path,
			map[string]any{"title": "x"}); code != http.StatusForbidden {
			t.Errorf("%s %s with a read token returned HTTP %d, want 403",
				tc.method, tc.path, code)
		}
	}
}

func TestNoTokenAndASuspendedOwnerBothReachNothing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if code, _, _ := f.call(t, "", http.MethodGet, "/v1/documents", nil); code != http.StatusUnauthorized {
		t.Errorf("no token returned HTTP %d, want 401", code)
	}
	if code, _, _ := f.call(t, "hearth_api_nonsense", http.MethodGet, "/v1/documents", nil); code != http.StatusUnauthorized {
		t.Errorf("a wrong token returned HTTP %d, want 401", code)
	}

	tok := f.token(t, f.alice, api.ScopeRead)
	if code, _, _ := f.call(t, tok, http.MethodGet, "/v1/whoami", nil); code != http.StatusOK {
		t.Fatalf("a good token returned HTTP %d", code)
	}
	// Somebody leaves. Their tokens go with them on the next request rather
	// than on the next restart, which is the first thing an auditor asks.
	if _, err := f.pools.Web.Exec(ctx,
		`UPDATE principals SET status='suspended' WHERE id=$1`, f.alice); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if code, _, _ := f.call(t, tok, http.MethodGet, "/v1/whoami", nil); code != http.StatusUnauthorized {
		t.Errorf("a suspended person's token still works: HTTP %d", code)
	}
}

// A misspelled field is refused rather than ignored. A client that types
// "titel" and gets a 201 has created something empty and been told it worked.
func TestAnUnknownFieldIsRefused(t *testing.T) {
	f := setup(t)
	tok := f.token(t, f.alice, api.ScopeWrite)
	code, _, _ := f.call(t, tok, http.MethodPost, "/v1/documents",
		map[string]any{"titel": "Typo", "kind": "task"})
	if code != http.StatusBadRequest {
		t.Errorf("a misspelled field returned HTTP %d, want 400", code)
	}
}

// -----------------------------------------------------------------------------
// Importers
// -----------------------------------------------------------------------------

// The criterion: every importer is safe to run twice.
func TestEveryImporterIsSafeToRunTwice(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	files := map[string]string{
		importer.Jira: "Issue key,Summary,Status,Priority\n" +
			"RUN-1,Jira row one,To Do,High\nRUN-2,Jira row two,Done,Low\n",
		importer.Asana: "Task ID,Name,Notes\n" +
			"RUN-3,Asana row one,text\nRUN-4,Asana row two,\n",
		importer.Monday: "Item ID,Name,Status\n" +
			"RUN-5,Monday row one,Working on it\n",
		importer.Trello: `[{"id":"RUN-6","name":"Trello card","list":{"name":"Doing"}}]`,
		importer.Generic: "id,title,state\n" +
			"RUN-7,Generic row,open\n",
	}

	for source, body := range files {
		tasks, err := importer.Parse(source, strings.NewReader(body))
		if err != nil {
			t.Fatalf("%s parse: %v", source, err)
		}
		first, err := importer.Run(ctx, f.docs, f.org, alice, source, tasks,
			importer.Options{Kind: docs.KindWorkOrder})
		if err != nil {
			t.Fatalf("%s first run: %v", source, err)
		}
		if first.Created != len(tasks) || first.Failed != 0 {
			t.Errorf("%s first run created %d of %d with %d failures",
				source, first.Created, len(tasks), first.Failed)
		}

		// The same file again, which is what happens the moment somebody
		// realises they picked the wrong kind.
		second, err := importer.Run(ctx, f.docs, f.org, alice, source, tasks,
			importer.Options{Kind: docs.KindWorkOrder})
		if err != nil {
			t.Fatalf("%s second run: %v", source, err)
		}
		if second.Created != 0 || second.Updated != len(tasks) {
			t.Errorf("%s run twice created %d and updated %d, want 0 and %d.\n"+
				"An import is never right the first time, and one that cannot "+
				"be re-run means starting again with a clean database.",
				source, second.Created, second.Updated, len(tasks))
		}
		if second.Failed != 0 {
			t.Errorf("%s second run had %d failures: %v",
				source, second.Failed, second.Problems)
		}
	}

	// And the total is what was in the files, once.
	var total int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM documents WHERE org_id=$1 AND deleted_at IS NULL`,
		f.org).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 7 {
		t.Errorf("%d documents after importing seven rows twice, want 7", total)
	}
}

// A row with no id is created every time, and that is honest rather than a
// bug: there is nothing to recognise it by, and matching on the title would
// merge two genuinely different jobs called "Service the pump".
func TestARowWithNoIdCannotBeMatchedAndSaysSo(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	body := "title,state\nService the pump,open\n"
	for i := 0; i < 2; i++ {
		tasks, err := importer.Parse(importer.Generic, strings.NewReader(body))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if _, err := importer.Run(ctx, f.docs, f.org, alice, importer.Generic, tasks,
			importer.Options{Kind: docs.KindTask}); err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	var n int
	if err := f.pools.Web.QueryRow(ctx,
		`SELECT count(*) FROM documents WHERE org_id=$1 AND title='Service the pump'`,
		f.org).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("%d rows with no id after two runs, want 2.\n"+
			"Matching on the title instead would merge two genuinely different "+
			"jobs with the same name, which is worse than a duplicate.", n)
	}
}

// -----------------------------------------------------------------------------
// Export
// -----------------------------------------------------------------------------

// The criterion: a full export re-imports into an empty instance.
//
// Into a second org rather than a metaphor, because "an empty instance" is
// exactly what a second org with no rows is, and the round trip is the only
// thing that distinguishes an export from a file.
func TestAFullExportReimportsIntoAnEmptyInstance(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	// Something worth exporting: a few work orders in different states.
	wanted := map[string]string{
		"Export one":   docs.StatusTodo,
		"Export two":   docs.StatusInProgress,
		"Export three": docs.StatusDone,
	}
	for title, status := range wanted {
		if _, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
			Kind: docs.KindWorkOrder, Title: title, Body: "Body of " + title,
			Task: &docs.TaskFields{Status: status, Priority: 2},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	tok := f.token(t, f.alice, api.ScopeRead)
	req := httptest.NewRequest(http.MethodGet, "/v1/export?format=csv&kind=work_order", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export returned HTTP %d: %s", rec.Code, rec.Body.String())
	}
	csvBody := rec.Body.String()
	if !strings.Contains(csvBody, "Export one") {
		t.Fatalf("the export is missing rows: %q", csvBody)
	}

	// The empty instance.
	other := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO orgs (id, slug, name) VALUES ($1,$2,'Empty')`,
		other, "em-"+other[:8]); err != nil {
		t.Fatalf("create the empty org: %v", err)
	}
	carol := uuid.NewString()
	if _, err := f.pools.Web.Exec(ctx,
		`INSERT INTO principals (id, org_id, kind, display_name) VALUES ($1,$2,'user','Carol')`,
		carol, other); err != nil {
		t.Fatalf("create carol: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			`DELETE FROM external_refs WHERE org_id=$1`,
			`DELETE FROM outbox WHERE org_id=$1`,
			`DELETE FROM grants WHERE org_id=$1`,
			`DELETE FROM document_versions WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM task_fields WHERE document_id IN (SELECT id FROM documents WHERE org_id=$1)`,
			`DELETE FROM documents WHERE org_id=$1`,
			`DELETE FROM principals WHERE org_id=$1`,
			`DELETE FROM orgs WHERE id=$1`,
		} {
			_, _ = f.pools.Web.Exec(ctx, q, other)
		}
	})
	carolSubjects, err := f.az.SubjectsFor(ctx, other, carol)
	if err != nil {
		t.Fatalf("subjects: %v", err)
	}

	// Straight back in through the generic importer. No bespoke reader: the
	// same path that runs every time somebody leaves another tool.
	tasks, err := importer.Parse(importer.Generic, strings.NewReader(csvBody))
	if err != nil {
		t.Fatalf("re-import parse: %v.\nAn export its own product cannot read "+
			"is a file rather than an exit.", err)
	}
	res, err := importer.Run(ctx, f.docs, other, carolSubjects, importer.Generic, tasks,
		importer.Options{Kind: docs.KindWorkOrder})
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if res.Created != len(wanted) || res.Failed != 0 {
		t.Fatalf("re-imported %d of %d rows with %d failures: %v",
			res.Created, len(wanted), res.Failed, res.Problems)
	}

	// And what came back is what went out: the titles, and the states.
	rows, err := f.pools.Web.Query(ctx, `
		SELECT d.title, t.status FROM documents d
		JOIN task_fields t ON t.document_id = d.id
		WHERE d.org_id = $1 AND d.deleted_at IS NULL`, other)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var title, status string
		if err := rows.Scan(&title, &status); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[title] = status
	}
	for title, status := range wanted {
		if got[title] != status {
			t.Errorf("%q came back as %q, want %q.\n"+
				"An export that loses state on the way back is a backup nobody "+
				"has restored.", title, got[title], status)
		}
	}

	// And back into the instance it came FROM, which is the case the empty
	// one does not cover: the export writes each document's own id, so a
	// re-import there has to recognise them rather than doubling everything.
	home, err := importer.Run(ctx, f.docs, f.org, alice, importer.Generic, tasks,
		importer.Options{Kind: docs.KindWorkOrder})
	if err != nil {
		t.Fatalf("re-import at home: %v", err)
	}
	if home.Created != 0 {
		t.Errorf("re-importing an export into the instance it came from created "+
			"%d duplicates, want 0.\n"+
			"The id column carries our own ids, and external_refs has no entry "+
			"for documents that were never imported from anywhere.", home.Created)
	}

	// Twice, because a re-import is an import and the same rule applies.
	again, err := importer.Run(ctx, f.docs, other, carolSubjects, importer.Generic, tasks,
		importer.Options{Kind: docs.KindWorkOrder})
	if err != nil {
		t.Fatalf("second re-import: %v", err)
	}
	if again.Created != 0 {
		t.Errorf("re-importing the same export twice created %d more", again.Created)
	}
}

// An export contains what the caller can see and nothing else.
func TestAnExportIsPermissionFiltered(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	if _, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindWorkOrder, Title: "Only Alice can see this",
		Task: &docs.TaskFields{Status: docs.StatusTodo},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/export?format=csv", nil)
	req.Header.Set("Authorization", "Bearer "+f.token(t, f.bob, api.ScopeRead))
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "Only Alice") {
		t.Error("Bob exported a row he was never granted.\n" +
			"A token that can dump rows its owner cannot read is the " +
			"privileged back door this package exists to not have.")
	}
}

// -----------------------------------------------------------------------------
// Webhooks
// -----------------------------------------------------------------------------

// A signature has to be checkable by a receiver, and an old one has to stop
// being accepted.
func TestASignatureIsVerifiableAndCannotBeReplayed(t *testing.T) {
	body := []byte(`{"event":"created","entity_id":"abc"}`)
	secret := "whsec_test"
	now := time.Now()

	header := api.Sign(secret, now, body)
	if !api.Verify(secret, header, body, now) {
		t.Fatal("a fresh signature did not verify")
	}
	if api.Verify("whsec_wrong", header, body, now) {
		t.Error("a signature verified against the wrong secret")
	}
	if api.Verify(secret, header, []byte(`{"event":"deleted"}`), now) {
		t.Error("a signature verified against a different body, so anybody who " +
			"captured one delivery can claim anything")
	}
	// Six minutes later. The timestamp is inside the signed material, so it
	// cannot be edited to make an old body fresh.
	if api.Verify(secret, header, body, now.Add(6*time.Minute)) {
		t.Error("an old signature still verified, so a captured body is useful " +
			"forever")
	}
	forged := strings.Replace(header, "t=", "t=9999999999,x=", 1)
	if api.Verify(secret, forged, body, now) {
		t.Error("a rewritten timestamp verified")
	}
}

func TestAWebhookNeedsAnHTTPURLAndGetsAGeneratedSecret(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	if _, err := f.svc.SaveWebhook(ctx, f.org, f.alice, api.Webhook{
		URL: "not-a-url",
	}); !errors.Is(err, api.ErrInvalid) {
		t.Errorf("a webhook with no scheme was accepted: %v", err)
	}

	h, err := f.svc.SaveWebhook(ctx, f.org, f.alice, api.Webhook{
		URL: "https://example.com/hearth", Events: []string{"created"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.HasPrefix(h.Secret, "whsec_") || len(h.Secret) < 20 {
		t.Errorf("the signing secret is %q, and a secret somebody chose is a "+
			"secret somebody reused", h.Secret)
	}
	list, err := f.svc.Webhooks(ctx, f.org)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].URL != "https://example.com/hearth" {
		t.Errorf("listed %+v", list)
	}
}

// Closing a task through the API closes its open subtasks, and the answer names
// them, so a caller is told what else moved.
func TestPatchingATaskDoneClosesItsSubtasks(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	parent, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTask, Title: "Ignition", Task: &docs.TaskFields{Status: docs.StatusTodo}})
	if err != nil {
		t.Fatal(err)
	}
	kid, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTask, Title: "Alice", ParentID: &parent.ID, Task: &docs.TaskFields{Status: docs.StatusInProgress}})
	if err != nil {
		t.Fatal(err)
	}
	token := f.token(t, f.alice, api.ScopeWrite)
	code, out, raw := f.call(t, token, http.MethodPatch, "/v1/documents/"+parent.ID, map[string]any{"status": "done"})
	if code != http.StatusOK {
		t.Fatalf("PATCH answered %d: %s", code, raw)
	}
	closed, _ := out["closed_subtasks"].([]any)
	if len(closed) != 1 || closed[0] != kid.ID {
		t.Fatalf("the answer should name the closed subtask: %s", raw)
	}
	got, err := f.docs.Get(ctx, f.org, alice, kid.ID)
	if err != nil || got.Task.Status != docs.StatusDone {
		t.Fatalf("the subtask is not done: %v %+v", err, got)
	}
}

func TestAdminTimeRemovalOverTheAPIIsSuperAdminOnlyDryFirstAndAudited(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)
	e, err := timelog.New(f.pools.Web, f.az).LogOwn(ctx, f.org, alice, "", 20, time.Now().Add(-time.Hour), "api removal")
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"ids": []string{e.ID}, "reason": "api proof"}
	aliceTok, bobTok := f.token(t, f.alice, api.ScopeWrite), f.token(t, f.bob, api.ScopeWrite)

	if code, _, raw := f.call(t, aliceTok, http.MethodPost, "/v1/time/remove", body); code != http.StatusForbidden {
		t.Fatalf("a person who is not a super admin got %d: %s", code, raw)
	}
	if _, err := f.pools.Web.Exec(ctx, `INSERT INTO capability_grants (org_id, principal_id, capability, granted_by)
		VALUES ($1,$2,'super_admin',$2)`, f.org, f.bob); err != nil {
		t.Fatal(err)
	}
	code, out, raw := f.call(t, bobTok, http.MethodPost, "/v1/time/remove", body)
	if code != http.StatusOK || out["apply"] != false || out["removed"] != float64(0) {
		t.Fatalf("dry run: %d %s", code, raw)
	}
	var n int
	_ = f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE entity_id=$1 AND action='time.remove'`, e.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("a dry run wrote %d audit rows", n)
	}
	body["apply"] = true
	if code, out, raw = f.call(t, bobTok, http.MethodPost, "/v1/time/remove", body); code != http.StatusOK || out["removed"] != float64(1) {
		t.Fatalf("apply: %d %s", code, raw)
	}
	_ = f.pools.Web.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE entity_id=$1 AND action='time.remove' AND actor_id=$2`, e.ID, f.bob).Scan(&n)
	if n != 1 {
		t.Fatalf("want one audit row by Bob, got %d", n)
	}
	readOnly := f.token(t, f.bob, api.ScopeRead)
	if code, _, _ := f.call(t, readOnly, http.MethodPost, "/v1/time/remove", body); code != http.StatusForbidden {
		t.Fatalf("a read token removed time: %d", code)
	}
}

// Any status on a task through the API moves its open subtasks, and the answer
// says to what; a subtask's own change is answered with nothing moved.
func TestPatchingATaskStatusMovesItsOpenSubtasks(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	alice := f.subj(t, f.alice)

	parent, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTask, Title: "Ignition", Task: &docs.TaskFields{Status: docs.StatusTodo}})
	if err != nil {
		t.Fatal(err)
	}
	kid, err := f.docs.Create(ctx, f.org, alice, docs.CreateParams{
		Kind: docs.KindTask, Title: "Alice", ParentID: &parent.ID, Task: &docs.TaskFields{Status: docs.StatusTodo}})
	if err != nil {
		t.Fatal(err)
	}
	token := f.token(t, f.alice, api.ScopeWrite)
	code, out, raw := f.call(t, token, http.MethodPatch, "/v1/documents/"+parent.ID, map[string]any{"status": "in_progress"})
	if code != http.StatusOK {
		t.Fatalf("PATCH answered %d: %s", code, raw)
	}
	moved, _ := out["cascaded_subtasks"].([]any)
	if len(moved) != 1 || moved[0] != kid.ID || out["cascaded_to"] != "in_progress" {
		t.Fatalf("the answer should name the subtask and where it went: %s", raw)
	}
	if _, closed := out["closed_subtasks"]; closed {
		t.Fatalf("an in progress move is not a close: %s", raw)
	}
	if got, err := f.docs.Get(ctx, f.org, alice, kid.ID); err != nil || got.Task.Status != docs.StatusInProgress {
		t.Fatalf("the subtask is not in progress: %v %+v", err, got)
	}

	// The subtask's own change leaves the parent alone.
	code, out, raw = f.call(t, token, http.MethodPatch, "/v1/documents/"+kid.ID, map[string]any{"status": "cancelled"})
	if code != http.StatusOK || out["cascaded_subtasks"] != nil {
		t.Fatalf("a subtask's change should move nothing: %d %s", code, raw)
	}
	if got, err := f.docs.Get(ctx, f.org, alice, parent.ID); err != nil || got.Task.Status != docs.StatusInProgress {
		t.Fatalf("the parent changed with its subtask: %v %+v", err, got)
	}
}

// A sync can send a create twice and never make two documents: the pair (source, external_id) is remembered on the first
// create, and every repeat answers 200 with the same document and Idempotent-Replay. A different source or id is a
// different document, none of it changes what a create without an external_id does, and a pair whose document was deleted
// makes a new one instead of answering with a ghost.
func TestDocumentCreateIsSafeToSendTwice(t *testing.T) {
	f := setup(t)
	tok := f.token(t, f.alice, api.ScopeWrite)
	create := func(body map[string]any) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/documents", bytes.NewReader(mustJSON(t, body)))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		id, _ := out["id"].(string)
		return rec.Code, id + "|" + rec.Header().Get("Idempotent-Replay")
	}
	count := func() int {
		var n int
		if err := f.pools.Web.QueryRow(context.Background(), `SELECT count(*) FROM documents WHERE org_id=$1 AND title='Sync me'`, f.org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	task := map[string]any{"kind": "task", "title": "Sync me", "external_id": "T-1", "source": "thoth"}
	code, first := create(task)
	if code != http.StatusCreated || strings.HasSuffix(first, "|true") {
		t.Fatalf("first create: %d %s", code, first)
	}
	code, again := create(task)
	if code != http.StatusOK || again != first+"true" {
		t.Fatalf("repeat: %d %s, want 200 and the same document with the replay header (first %s)", code, again, first)
	}
	if count() != 1 {
		t.Fatalf("%d documents after a repeat, want 1", count())
	}
	// Another source with the same id, and another id, are other documents; no source means "api".
	for _, other := range []map[string]any{
		{"kind": "task", "title": "Sync me", "external_id": "T-1", "source": "github"},
		{"kind": "task", "title": "Sync me", "external_id": "T-2", "source": "thoth"},
		{"kind": "task", "title": "Sync me", "external_id": "T-1"},
	} {
		if code, _ := create(other); code != http.StatusCreated {
			t.Fatalf("%v: %d", other, code)
		}
	}
	// Without an external_id every create makes a document, as before.
	create(map[string]any{"kind": "task", "title": "Sync me"})
	create(map[string]any{"kind": "task", "title": "Sync me"})
	if count() != 6 {
		t.Fatalf("%d documents, want 6", count())
	}
	// The document was deleted: the pair is forgotten and the next create makes it again.
	firstID := strings.SplitN(first, "|", 2)[0]
	if err := f.docs.Delete(context.Background(), f.org, f.subj(t, f.alice), firstID); err != nil {
		t.Fatal(err)
	}
	code, remade := create(task)
	if code != http.StatusCreated || strings.SplitN(remade, "|", 2)[0] == firstID {
		t.Fatalf("a deleted document's pair answered %d %s, want a new document", code, remade)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
