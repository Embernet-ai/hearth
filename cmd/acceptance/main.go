// Command acceptance drives a running Hearth the way a person does, over HTTP,
// against a real deployment.
//
// It is a shipped binary, versioned with the application, and it runs after
// every deploy. That is deliberate: the predecessor carried this same list as a
// checklist in a runbook for months and never completed it, because a checklist
// only runs when somebody remembers.
//
// The step that matters is upload a file AND DOWNLOAD IT AGAIN, comparing
// SHA-256. Uploads report success whether or not the download path works, so an
// upload-only check proves nothing. That exact gap shipped in the predecessor:
// every upload succeeded and every download was dead, and nothing noticed.
//
//	acceptance -base https://hearth-next.example.com -email you@example.com -password ...
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type result struct {
	name string
	ok   bool
	note string
}

type runner struct {
	base    string
	client  *http.Client
	results []result
	// The owner's credentials, kept so a check can open a second, completely
	// fresh browser as the same person.
	ownerEmail    string
	ownerPassword string
	// keep leaves the created work order behind, for when a failure needs to be
	// looked at rather than tidied away.
	keep bool
	// inboundKey lets the suite post to the inbound mail endpoint the way the
	// estate's MTA does. Empty means that feature is off on this deployment,
	// and the checks that need it say so rather than failing.
	inboundKey string
}

// asksForData makes every request ask for a page's data, which is what a page is: an address answers a browser with the
// interface and answers this header with the page's data, from the same handler, behind the same checks.
//
// The suite's checks were written against markup (a data-* hook on a row, a field on a form, a heading), and the markup is
// now drawn in the browser. So when a response is a page's data and a renderer is given (-render), the body handed to the
// checks is that page drawn by the interface's own views and shell (ui/src/ssr.ts): the same markup the browser would have,
// from the same data. A request carrying X-Acceptance-Raw keeps the data as it came.
type asksForData struct{ next http.RoundTripper }

var renderer string // the path of ui/dist-ssr/render.mjs, from -render

func (a asksForData) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Accept") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("Accept", "application/json")
	}
	resp, err := a.next.RoundTrip(req)
	if err != nil || renderer == "" || req.Header.Get("X-Acceptance-Raw") != "" ||
		resp.Header.Get("X-Hearth-Page-Data") != "1" || resp.Request.Method == http.MethodHead {
		return resp, err
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("node", renderer)
	cmd.Stdin = bytes.NewReader(data)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("drawing %s: %v: %s", req.URL.Path, err, stderr.String())
	}
	html := commentRe.ReplaceAll(out.Bytes(), nil)
	resp.Body = io.NopCloser(bytes.NewReader(html))
	resp.ContentLength = int64(len(html))
	resp.Header.Set("Content-Type", "text/html; charset=utf-8")
	resp.Header.Del("Content-Length")
	return resp, nil
}

// commentRe is the markers the renderer leaves between blocks; a browser's DOM has none of them in what a person reads.
var commentRe = regexp.MustCompile(`<!--.*?-->`)

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "base URL of the deployment")
	email := flag.String("email", "", "account email")
	password := flag.String("password", "", "account password")
	org := flag.String("org", "Acceptance", "organisation name, used only on first run")
	name := flag.String("name", "Acceptance Runner", "display name, used only on first run")
	flag.StringVar(&renderer, "render", "", "path of ui/dist-ssr/render.mjs: the interface drawn on the server, so the checks read the markup the browser would draw")
	timeout := flag.Duration("timeout", 60*time.Second, "per-request timeout")
	keep := flag.Bool("keep", false, "leave the created work order behind instead of deleting it")
	inboundKey := flag.String("inbound-key", "", "the deployment's HEARTH_INBOUND_KEY, for the inbound mail checks")
	flag.Parse()

	if *email == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "acceptance: -email and -password are required")
		os.Exit(2)
	}

	jar, _ := cookiejar.New(nil)
	r := &runner{
		keep: *keep,
		base: strings.TrimSuffix(*base, "/"),
		client: &http.Client{
			Jar:       jar,
			Timeout:   *timeout,
			Transport: asksForData{http.DefaultTransport},
			// Follow redirects but stop at 10, so a redirect loop fails the run
			// instead of hanging it.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}

	r.ownerEmail, r.ownerPassword = *email, *password
	r.inboundKey = *inboundKey
	r.run(*org, *name, *email, *password)
	os.Exit(r.report())
}

func (r *runner) run(org, name, email, password string) {
	// 0. The deployment is alive, and is what we think it is.
	rev := r.checkHealth()

	// 1. Sign in. First run claims the installation instead.
	if !r.signIn(email, password) {
		r.setup(org, name, email, password)
	}

	// 2. Create a work order.
	docID := r.createWorkOrder()
	if docID == "" {
		return
	}

	// 3. Read it back.
	r.readBack(docID)

	// 4. Upload a file AND DOWNLOAD IT AGAIN. The one that matters.
	r.fileRoundTrip(docID)

	// 5. Move it through its lifecycle.
	r.updateStatus(docID)

	// 6. A stale edit must be rejected, not silently win.
	r.staleEditRejected(docID)

	// 7. Views: filters that compose, a board grouped by a custom field, and a
	// saved view that opens for somebody else without showing them anything
	// they have no grant for.
	r.views()

	// 8. Background work: events reach the queue, and the dead letter path is
	// proved by watching a job fail rather than by trusting that it would.
	r.backgroundWork()

	// 9. Conversation: a channel that grants what is attached to it, a mention
	// that lands where it pointed, and a comment that arrives live.
	r.conversation()

	// 10. Documents: a rendered body, a restorable history, a working template.
	r.documentsBehaveLikeDocuments()

	// 10b. The dashboard's numbers are the records they say they are.
	r.theDashboardNumbersAreRecords()
	r.iconsNeedNoSignIn()

	// 11. Time: a timer that is server-side, a roll-up onto the asset, and a
	// workload that excludes work nobody is going to do.
	r.timeAndWorkload()

	// 12. Automations: once per event, one cycle, and a dry run that is dry.
	r.automations()

	// 13. Notifications: exactly one, never your own, and stopping when told.
	r.notifications()

	// 14. Search: findable quickly, invisible to somebody with no grant, and
	// rebuildable from Postgres alone.
	r.searchWorks()

	// 15. Preventive maintenance: one successor and never a pile, a meter that
	// moves the date, and a compliance view that matches the record.
	r.preventiveMaintenance()

	// 16. The plant tree: a failure at the bottom visible at the top, a cycle
	// refused rather than hung, and a label that scans to the right machine.
	r.plantTree()

	// 17. Contacts, companies, and parts: a person findable and grantable like
	// anything else, and a repair that costs what it actually cost.
	r.crmAndParts()

	// 18. Email and calls: work arriving the way it actually arrives, once per
	// message however many times the relay tries, and a reply that threads.
	r.inboundIsClosedWithoutTheKey()
	r.emailAndCalls()

	// 19. Agents, the protocol they reach the product through, and a canvas
	// whose cards are live entities rather than a picture.
	r.agentsAndCanvas()

	// 20. The public API, the importers, and the export that makes the product
	// something somebody can leave.
	r.apiAndExport()

	// 21. The plant floor: an installable application, a queue that survives no
	// signal, a conflict that asks rather than overwriting, and a photograph
	// that arrives as the photograph that was taken.
	r.offlineAndMobile()

	// 22. Tenancy and operations: a deletion nobody can hide, a team that gives
	// and takes access away on the next request, a directory that can remove
	// somebody, something to watch, and a budget one address cannot exceed.
	r.governance()

	// 23. Take away what it brought, so running this against the real deployment
	// does not silt it up with test data.
	if !r.keep {
		r.cleanUp(docID)
	}

	// 24. Signing out must actually revoke.
	r.signOutRevokes()

	// 25. And an anonymous caller must reach nothing.
	r.anonymousIsDenied(docID)

	if rev != "" {
		fmt.Printf("\nagainst revision %s\n", rev)
	}
}

func (r *runner) checkHealth() string {
	resp, err := r.client.Get(r.base + "/readyz")
	if err != nil {
		r.fail("deployment is reachable", err.Error())
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var h struct {
		Status string `json:"status"`
		Build  struct {
			Revision string `json:"revision"`
			Profile  string `json:"profile"`
		} `json:"build"`
		Checks map[string]string `json:"checks"`
	}
	_ = json.Unmarshal(body, &h)

	r.check("deployment is ready", resp.StatusCode == 200 && h.Status == "ok",
		fmt.Sprintf("status=%s checks=%v", h.Status, h.Checks))

	// A development build must never be what is deployed. Checking the running
	// artifact beats grepping the source, because the source is not what ships.
	r.check("deployed binary is a release build", h.Build.Profile == "release",
		"profile="+h.Build.Profile)
	return h.Build.Revision
}

func (r *runner) setup(org, name, email, password string) {
	resp, err := r.client.PostForm(r.base+"/setup", url.Values{
		"org": {org}, "name": {name}, "email": {email}, "password": {password},
	})
	if err != nil {
		r.fail("first-run setup", err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	r.check("first-run setup created the owner",
		resp.StatusCode == 200 && !strings.Contains(string(body), "Set up Hearth"),
		fmt.Sprintf("HTTP %d", resp.StatusCode))
}

func (r *runner) signIn(email, password string) bool {
	resp, err := r.client.PostForm(r.base+"/login", url.Values{
		"email": {email}, "password": {password},
	})
	if err != nil {
		r.fail("sign in", err.Error())
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	ok := resp.StatusCode == 200 && !strings.Contains(string(body), "invalid email or password")
	if ok {
		r.check("sign in", true, email)
	}
	return ok
}

var docIDRe = regexp.MustCompile(`/documents/([0-9a-f-]{36})`)

func (r *runner) createWorkOrder() string {
	resp, err := r.client.PostForm(r.base+"/documents", url.Values{
		"kind":  {"work_order"},
		"title": {"Acceptance: Line 3 conveyor faulting"},
		"body":  {"Raised by the acceptance run."},
	})
	if err != nil {
		r.fail("create a work order", err.Error())
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// The client followed the redirect, so the final URL is the document.
	id := ""
	if m := docIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		id = m[1]
	}
	r.check("create a work order", id != "" && resp.StatusCode == 200,
		fmt.Sprintf("HTTP %d id=%s", resp.StatusCode, id))
	_ = body
	return id
}

func (r *runner) readBack(docID string) {
	body := r.get("/documents/" + docID)
	r.check("read the work order back",
		strings.Contains(body, "Acceptance: Line 3 conveyor faulting") &&
			strings.Contains(body, "Raised by the acceptance run."),
		"title and body both present")
}

// fileRoundTrip is the reason this binary exists.
func (r *runner) fileRoundTrip(docID string) {
	// Non-trivial and random, so a cached or truncated response cannot pass.
	original := make([]byte, 2<<20) // 2 MiB
	if _, err := rand.Read(original); err != nil {
		r.fail("generate test content", err.Error())
		return
	}
	want := sha256.Sum256(original)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "acceptance.bin")
	if err != nil {
		r.fail("build upload", err.Error())
		return
	}
	if _, err := part.Write(original); err != nil {
		r.fail("build upload", err.Error())
		return
	}
	_ = mw.Close()

	resp, err := r.client.Post(r.base+"/documents/"+docID+"/files", mw.FormDataContentType(), &buf)
	if err != nil {
		r.fail("upload a file", err.Error())
		return
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r.check("upload a file", resp.StatusCode == 200, fmt.Sprintf("HTTP %d", resp.StatusCode))

	// Find the attachment link the page now renders.
	fileRe := regexp.MustCompile(`/documents/` + docID + `/files/([0-9a-f-]{36})`)
	m := fileRe.FindStringSubmatch(string(page))
	if len(m) != 2 {
		r.fail("the uploaded file appears on the document", "no attachment link rendered")
		return
	}
	r.check("the uploaded file appears on the document", true, "attachment listed")

	// AND DOWNLOAD IT AGAIN.
	dl, err := r.client.Get(r.base + "/documents/" + docID + "/files/" + m[1])
	if err != nil {
		r.fail("download the file again", err.Error())
		return
	}
	defer dl.Body.Close()
	got, err := io.ReadAll(dl.Body)
	if err != nil {
		r.fail("download the file again", err.Error())
		return
	}

	r.check("download the file again", dl.StatusCode == 200 && len(got) == len(original),
		fmt.Sprintf("HTTP %d, %d bytes", dl.StatusCode, len(got)))

	gotSum := sha256.Sum256(got)
	r.check("SHA-256 matches what was uploaded", gotSum == want,
		fmt.Sprintf("up=%s down=%s", hex.EncodeToString(want[:8]), hex.EncodeToString(gotSum[:8])))

	// The server reports the digest it recorded at upload, so a caller can
	// verify without trusting the transfer.
	r.check("server reports the stored digest",
		dl.Header.Get("X-Content-SHA256") == hex.EncodeToString(want[:]),
		dl.Header.Get("X-Content-SHA256"))
}

func (r *runner) updateStatus(docID string) {
	body := r.get("/documents/" + docID)
	version := "1"
	if m := regexp.MustCompile(`name="version" value="(\d+)"`).FindStringSubmatch(body); len(m) == 2 {
		version = m[1]
	}
	resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/status",
		url.Values{"status": {"in_progress"}, "version": {version}})
	if err != nil {
		r.fail("move the work order to in_progress", err.Error())
		return
	}
	defer resp.Body.Close()
	after := r.get("/documents/" + docID)
	r.check("move the work order to in_progress", strings.Contains(after, "in_progress"),
		"status reflected on the page")
}

func (r *runner) staleEditRejected(docID string) {
	// Version 1 is definitely stale by now.
	resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/status",
		url.Values{"status": {"done"}, "version": {"1"}})
	if err != nil {
		r.fail("a stale edit is rejected", err.Error())
		return
	}
	defer resp.Body.Close()
	r.check("a stale edit is rejected rather than silently winning",
		resp.StatusCode == http.StatusConflict, fmt.Sprintf("HTTP %d, want 409", resp.StatusCode))
}

// cleanUp removes what this run created.
//
// A suite that is safe to run against the real deployment has to be safe to run
// repeatedly, and without this every run leaves a work order behind for good.
// Three of them had already piled up in production before this existed, which
// is how it was noticed: a status filter that should have shown one row showed
// four.
func (r *runner) cleanUp(docID string) {
	resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
	if err != nil {
		r.fail("clean up after itself", err.Error())
		return
	}
	defer resp.Body.Close()

	// Verify it is actually gone rather than trusting the redirect, because a
	// delete that silently does nothing looks exactly like one that worked.
	after, err := r.client.Get(r.base + "/documents/" + docID)
	if err != nil {
		r.fail("clean up after itself", err.Error())
		return
	}
	defer after.Body.Close()
	r.check("clean up after itself", after.StatusCode == http.StatusNotFound,
		fmt.Sprintf("HTTP %d, want 404", after.StatusCode))
}

// -----------------------------------------------------------------------------
// Views
// -----------------------------------------------------------------------------

// secondAccount is the colleague this suite shares a view with.
//
// A fixed address, reused between runs, like the owner account the suite
// already signs in as. Creating a fresh one each time would leave a trail of
// people in the assignee picker, and there is no way to delete a principal
// without taking the history that references it.
const (
	secondEmail    = "acceptance-second@hearth.invalid"
	secondPassword = "a second long enough password"
)

// views exercises the whole of phase one against a live deployment.
func (r *runner) views() {
	// Three work orders with known statuses and priorities, so the count a
	// filtered list reports can be checked against a number this suite knows
	// rather than against the same query asked twice.
	made := []string{
		r.createFiltered("Acceptance: filter todo urgent", "todo", "3"),
		r.createFiltered("Acceptance: filter todo low", "todo", "0"),
		r.createFiltered("Acceptance: filter done urgent", "done", "3"),
	}
	for _, id := range made {
		if id == "" {
			r.fail("set up the view checks", "a work order could not be created")
			return
		}
	}
	defer func() {
		if r.keep {
			return
		}
		for _, id := range made {
			_, _ = r.client.PostForm(r.base+"/documents/"+id+"/delete", url.Values{})
		}
	}()

	r.filtersCompose()
	r.boardGroupsByProperty()
	r.savedViewOpensForSomebodyElse()
}

// createFiltered raises a work order with a known status and priority.
func (r *runner) createFiltered(title, status, priority string) string {
	resp, err := r.client.PostForm(r.base+"/documents", url.Values{
		"kind": {"work_order"}, "title": {title}, "priority": {priority},
	})
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	m := docIDRe.FindStringSubmatch(resp.Request.URL.Path)
	if len(m) != 2 {
		return ""
	}
	id := m[1]

	if status != "todo" {
		body := r.get("/documents/" + id)
		version := "1"
		if v := versionRe.FindStringSubmatch(body); len(v) == 2 {
			version = v[1]
		}
		resp, err := r.client.PostForm(r.base+"/documents/"+id+"/status",
			url.Values{"status": {status}, "version": {version}})
		if err == nil {
			resp.Body.Close()
		}
	}
	return id
}

var versionRe = regexp.MustCompile(`name="version" value="(\d+)"`)

// countRe reads the "12 of 40" badge a list renders beside its heading. It is
// the count the server compiled, not one the page added up, which is the point:
// a count assembled from the rendered rows could never disagree with them.
var countRe = regexp.MustCompile(`(?s)<h1>[^<]*</h1>\s*<span class="muted small">\s*(?:(\d+) of )?(\d+)`)

func listCount(body string) (shown, total int, ok bool) {
	m := countRe.FindStringSubmatch(body)
	if len(m) != 3 {
		return 0, 0, false
	}
	total, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, 0, false
	}
	shown = total
	if m[1] != "" {
		if shown, err = strconv.Atoi(m[1]); err != nil {
			return 0, 0, false
		}
	}
	return shown, total, true
}

// filtersCompose is the check that two conditions narrow rather than replace.
func (r *runner) filtersCompose() {
	one := r.get("/work-orders?f=" + url.QueryEscape("status:is:todo"))
	shownOne, _, okOne := listCount(one)

	both := r.get("/work-orders?f=" + url.QueryEscape("status:is:todo") +
		"&f=" + url.QueryEscape("priority:is:3"))
	shownBoth, totalBoth, okBoth := listCount(both)

	r.check("a filtered list reports a count", okOne && okBoth,
		fmt.Sprintf("todo=%d todo+urgent=%d", shownOne, shownBoth))
	if !okOne || !okBoth {
		return
	}

	// The second filter must remove the low priority one this run created.
	r.check("two filters compose rather than replace", shownBoth < shownOne,
		fmt.Sprintf("%d rows with both, %d with one", shownBoth, shownOne))

	// And the count has to describe the rows underneath it. The count is
	// compiled from the same WHERE as the list, so a disagreement here means
	// one of the two is answering a different question.
	rendered := strings.Count(both, `<tr data-doc="`)
	r.check("the count matches the rows it is counting",
		shownBoth == totalBoth && shownBoth == rendered,
		fmt.Sprintf("count says %d, %d rows rendered", shownBoth, rendered))

	// The two this run created with status todo must both be reachable through
	// the single filter, and only the urgent one through the pair.
	r.check("the composed filter keeps the right row and drops the other",
		strings.Contains(both, "Acceptance: filter todo urgent") &&
			!strings.Contains(both, "Acceptance: filter todo low") &&
			!strings.Contains(both, "Acceptance: filter done urgent"),
		"only the todo urgent work order survived both filters")
}

// boardGroupsByProperty is the phase's headline claim: a board can group by a
// field nobody anticipated, because a property is a real field rather than text
// crammed into a description.
func (r *runner) boardGroupsByProperty() {
	// Assets seed criticality on first view, so open one to make the field
	// exist before grouping by it.
	assetID := r.createAsset("Acceptance: grouped asset")
	if assetID == "" {
		r.fail("a board groups by a custom select property", "could not create an asset")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+assetID+"/delete", url.Values{})
		}
	}()

	page := r.get("/documents/" + assetID)
	m := regexp.MustCompile(`name="prop:([0-9a-f-]{36})"[^>]*>\s*<option value="">Not set`).
		FindStringSubmatch(page)
	if len(m) != 2 {
		// Fall back to any property field on the page, then confirm by name.
		m = regexp.MustCompile(`id="prop-criticality" name="prop:([0-9a-f-]{36})"`).
			FindStringSubmatch(page)
	}
	if len(m) != 2 {
		r.fail("a board groups by a custom select property",
			"no criticality field rendered on the asset")
		return
	}
	resp, err := r.client.PostForm(r.base+"/documents/"+assetID+"/properties",
		url.Values{"prop:" + m[1]: {"safety"}})
	if err != nil {
		r.fail("a board groups by a custom select property", err.Error())
		return
	}
	resp.Body.Close()

	board := r.get("/assets?view=board&group=" + url.QueryEscape("prop.criticality"))
	// Every declared option becomes a column, including the empty ones,
	// because an empty column is where a card gets dragged to.
	r.check("a board groups by a custom select property",
		strings.Contains(board, `data-value="safety"`) &&
			strings.Contains(board, `data-value="low"`) &&
			strings.Contains(board, `data-group="prop.criticality"`),
		"columns rendered for the criticality options")

	r.check("the card lands in the column its property says",
		strings.Contains(board, "Acceptance: grouped asset"),
		"the asset appears on the board")
}

func (r *runner) createAsset(title string) string {
	resp, err := r.client.PostForm(r.base+"/documents", url.Values{
		"kind": {"asset"}, "title": {title},
	})
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if m := docIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		return m[1]
	}
	return ""
}

var viewIDRe = regexp.MustCompile(`/views/([0-9a-f-]{36})`)

// savedViewOpensForSomebodyElse is the sharing claim and the permission claim
// in one run: the second person opens the same view, and sees nothing they were
// not granted.
func (r *runner) savedViewOpensForSomebodyElse() {
	resp, err := r.client.PostForm(r.base+"/views", url.Values{
		"from":   {"/work-orders?f=" + url.QueryEscape("status:is:todo")},
		"name":   {"Acceptance shared"},
		"shared": {"on"},
	})
	if err != nil {
		r.fail("save a view", err.Error())
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	_ = body

	viewID := ""
	if m := viewIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		viewID = m[1]
	}
	r.check("save a view", viewID != "", "id="+viewID)
	if viewID == "" {
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/views/"+viewID+"/delete", url.Values{})
		}
	}()

	mine := r.get("/views/" + viewID)
	_, ownerTotal, ok := listCount(mine)
	r.check("a saved view opens for its owner",
		ok && strings.Contains(mine, "Acceptance: filter todo urgent"),
		fmt.Sprintf("%d rows", ownerTotal))

	// The colleague. Created once and reused, because a principal cannot be
	// deleted without taking the history that references it.
	r.ensureSecondAccount()

	other, err := r.signInAs(secondEmail, secondPassword)
	if err != nil {
		r.fail("a second person can sign in", err.Error())
		return
	}
	r.check("a second person can sign in", true, secondEmail)
	if other.heldAtAccount {
		// Only the run that created the account sees this; later runs find the
		// password already chosen.
		r.check("a handed-over password is held at the account page until it is changed",
			true, "the first page after sign in was /account")
	}

	theirs, status := other.get("/views/" + viewID)
	r.check("a shared view opens for a second person",
		status == 200 && strings.Contains(theirs, "Acceptance shared"),
		fmt.Sprintf("HTTP %d", status))

	_, theirTotal, okTheirs := listCount(theirs)
	// The rows are theirs, not the owner's. Same question, their own answer.
	r.check("a shared view shows no row the second person was not granted",
		okTheirs && theirTotal == 0 &&
			!strings.Contains(theirs, "Acceptance: filter todo urgent"),
		fmt.Sprintf("%d rows for them, %d for the owner", theirTotal, ownerTotal))

	// And a private view stays private, reported as missing rather than
	// forbidden so an id cannot be confirmed by asking.
	priv, err := r.client.PostForm(r.base+"/views", url.Values{
		"from": {"/work-orders"}, "name": {"Acceptance private"},
	})
	if err == nil {
		priv.Body.Close()
		if m := viewIDRe.FindStringSubmatch(priv.Request.URL.Path); len(m) == 2 {
			_, code := other.get("/views/" + m[1])
			r.check("a private view is not reachable by id", code == http.StatusNotFound,
				fmt.Sprintf("HTTP %d, want 404", code))
			if !r.keep {
				_, _ = r.client.PostForm(r.base+"/views/"+m[1]+"/delete", url.Values{})
			}
		}
	}
}

// ensureSecondAccount adds the colleague if this deployment does not have them
// yet. An address that already exists is the expected case on every run after
// the first, and is not a failure.
func (r *runner) ensureSecondAccount() {
	resp, err := r.client.PostForm(r.base+"/people", url.Values{
		"name": {"Acceptance Second"}, "email": {secondEmail}, "password": {secondPassword},
	})
	if err != nil {
		return
	}
	resp.Body.Close()
}

// client is a second browser, with its own cookie jar.
type client struct {
	base string
	http *http.Client
	// heldAtAccount is true when signing in landed on the account page because
	// the password was one somebody else chose, and the client then chose it
	// again the way a real colleague has to.
	heldAtAccount bool
}

func (c *client) get(path string) (string, int) {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		return "", 0
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode
}

func (c *client) post(path string, form url.Values) (*http.Response, error) {
	return c.http.PostForm(c.base+path, form)
}

// stream opens a server-sent event connection and reports the NAME of each
// event that arrives.
//
// Reading the stream is the only honest way to check a live update. Polling the
// page afterwards passes whether or not anything was ever pushed, which is
// precisely the question.
func (c *client) stream(path string) (<-chan string, func(), error) {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, func() {}, err
	}
	req.Header.Set("Accept", "text/event-stream")

	// No client timeout on this one: a stream that is working stays open, and
	// the caller's select is what bounds the wait.
	streaming := &http.Client{Jar: c.http.Jar}
	resp, err := streaming.Do(req)
	if err != nil {
		return nil, func() {}, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, func() {}, fmt.Errorf("event stream returned HTTP %d", resp.StatusCode)
	}

	events := make(chan string, 8)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			// The build event is sent on connect to every subscriber and says
			// nothing about this check.
			if name, ok := strings.CutPrefix(line, "event: "); ok && name != "build" {
				select {
				case events <- name:
				default:
				}
			}
		}
	}()
	return events, func() { resp.Body.Close() }, nil
}

func (r *runner) signInAs(email, password string) (*client, error) {
	jar, _ := cookiejar.New(nil)
	c := &client{base: r.base, http: &http.Client{Transport: asksForData{http.DefaultTransport}, Jar: jar, Timeout: 60 * time.Second}}
	resp, err := c.http.PostForm(r.base+"/login", url.Values{
		"email": {email}, "password": {password},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "invalid email or password") {
		return nil, fmt.Errorf("sign in refused for %s", email)
	}
	// An account on a password somebody else chose is held at its account page
	// until the person picks their own, so a colleague signs in and does that
	// first, exactly as a real one must.
	home, err := c.http.Get(r.base + "/")
	if err != nil {
		return nil, err
	}
	home.Body.Close()
	if home.Request.URL.Path == "/account" {
		c.heldAtAccount = true
		change, err := c.http.PostForm(r.base+"/account/password", url.Values{
			"current": {password}, "next": {password},
		})
		if err != nil {
			return nil, err
		}
		change.Body.Close()
	}
	return c, nil
}

// -----------------------------------------------------------------------------
// Conversation
// -----------------------------------------------------------------------------

var channelIDRe = regexp.MustCompile(`/channels/([0-9a-f-]{36})`)

// conversation is phase four: a channel that grants what is attached to it, a
// mention that lands on the thing it named, and a comment that arrives in
// somebody else's browser without them pressing anything.
func (r *runner) conversation() {
	resp, err := r.client.PostForm(r.base+"/channels", url.Values{
		"name":       {"Acceptance " + nonce()[:12]},
		"topic":      {"raised by the acceptance run"},
		"visibility": {"public"},
	})
	if err != nil {
		r.fail("create a channel", err.Error())
		return
	}
	resp.Body.Close()
	channelID := ""
	if m := channelIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		channelID = m[1]
	}
	r.check("create a channel", channelID != "", "id="+channelID)
	if channelID == "" {
		return
	}

	docID := r.createFiltered("Acceptance: attached to a channel", "todo", "1")
	if docID == "" {
		r.fail("attach a document to a channel", "could not create the work order")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()

	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/channel",
		url.Values{"channel": {channelID}}); err == nil {
		resp.Body.Close()
	}
	page := r.get("/channels/" + channelID)
	r.check("attach a document to a channel",
		strings.Contains(page, "Acceptance: attached to a channel"),
		"the channel lists what it grants")

	r.membershipIsAccess(channelID, docID)
	r.mentionLandsOnTheThingItNamed(channelID, docID)
	r.commentArrivesLive(channelID, docID)

	if !r.keep {
		// Leaving is not deleting, and a channel has no delete: it archives,
		// which is a phase sixteen surface. The one this run made stays, named
		// after a nonce so two runs never collide.
		_, _ = r.client.PostForm(r.base+"/channels/"+channelID+"/leave", url.Values{})
	}
}

// membershipIsAccess is the claim the whole phase rests on.
func (r *runner) membershipIsAccess(channelID, docID string) {
	other, err := r.signInAs(secondEmail, secondPassword)
	if err != nil {
		r.fail("joining a channel reveals what is attached to it", err.Error())
		return
	}

	// Before joining, the document is not theirs.
	_, before := other.get("/documents/" + docID)
	r.check("a document attached to a channel is hidden from non-members",
		before == http.StatusNotFound, fmt.Sprintf("HTTP %d, want 404", before))

	if resp, err := other.post("/channels/"+channelID+"/join", url.Values{}); err == nil {
		resp.Body.Close()
	}
	_, after := other.get("/documents/" + docID)
	r.check("joining a channel reveals what is attached to it",
		after == http.StatusOK, fmt.Sprintf("HTTP %d, want 200", after))

	if resp, err := other.post("/channels/"+channelID+"/leave", url.Values{}); err == nil {
		resp.Body.Close()
	}
	_, gone := other.get("/documents/" + docID)
	r.check("leaving a channel hides them again",
		gone == http.StatusNotFound, fmt.Sprintf("HTTP %d, want 404", gone))
}

// mentionLandsOnTheThingItNamed posts a message referring to a work order and
// then reads the work order to see whether it knows.
func (r *runner) mentionLandsOnTheThingItNamed(channelID, docID string) {
	marker := nonce()
	body := "Handover: /documents/" + docID + " still open. " + marker
	resp, err := r.client.PostForm(r.base+"/channels/"+channelID+"/messages",
		url.Values{"body": {body}})
	if err != nil {
		r.fail("a mention appears on the mentioned entity", err.Error())
		return
	}
	resp.Body.Close()

	doc := r.get("/documents/" + docID)
	r.check("a mention appears on the mentioned entity",
		strings.Contains(doc, "Mentioned in") && strings.Contains(doc, marker),
		"the work order lists the conversation that referenced it")
}

// commentArrivesLive opens a second browser's event stream, posts a comment as
// the first, and waits for the second to be told.
//
// The stream is read rather than the page polled, deliberately: polling would
// pass whether or not anything was ever pushed, which is the entire question.
func (r *runner) commentArrivesLive(channelID, docID string) {
	other, err := r.signInAs(secondEmail, secondPassword)
	if err != nil {
		r.fail("a comment arrives live in a second session", err.Error())
		return
	}

	// Back into the channel, because the previous check had them leave it and
	// a subscriber with no grant on the document is correctly told nothing.
	// The subscription is authorised server-side from the caller's own subject
	// set, so this is not a formality: without the grant, the topic is refused
	// and the stream stays silent, which is the behaviour that check exists to
	// confirm and this one would otherwise trip over.
	if resp, err := other.post("/channels/"+channelID+"/join", url.Values{}); err == nil {
		resp.Body.Close()
	}
	if _, code := other.get("/documents/" + docID); code != http.StatusOK {
		r.fail("a comment arrives live in a second session",
			fmt.Sprintf("the second account cannot see the document (HTTP %d)", code))
		return
	}

	events, stop, err := other.stream("/events?doc=" + docID)
	if err != nil {
		r.fail("a comment arrives live in a second session", err.Error())
		return
	}
	defer stop()

	// Give the subscription a moment to be registered before the event that
	// has to reach it. Without this the post can win the race and the check
	// fails for a reason that has nothing to do with the feature.
	time.Sleep(500 * time.Millisecond)

	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/comments",
		url.Values{"body": {"Acceptance comment " + nonce()}}); err == nil {
		resp.Body.Close()
	}

	select {
	case name := <-events:
		r.check("a comment arrives live in a second session", name == "comment",
			"the second browser was told: "+name)
	case <-time.After(10 * time.Second):
		r.fail("a comment arrives live in a second session",
			"nothing was pushed within ten seconds")
	}
}

// -----------------------------------------------------------------------------
// Time
// -----------------------------------------------------------------------------

// timeAndWorkload is phase eight.
//
// The timer's survival across a restart is proved in the integration test,
// which discards every pool and rebuilds the service. What is worth checking
// over HTTP is the property that makes that possible: the timer is server-side
// state keyed on the person, so a browser that has never seen it still finds
// it running.
func (r *runner) timeAndWorkload() {
	asset := r.createAsset("Acceptance: conveyor with a history")
	if asset == "" {
		r.fail("actual minutes roll up to the linked asset", "could not create the asset")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+asset+"/delete", url.Values{})
		}
	}()

	// Two jobs on the asset and one off it, so the roll-up has something to
	// exclude as well as something to add.
	var jobs []string
	for i, spec := range []struct {
		title   string
		minutes string
		linked  bool
	}{
		{"Acceptance: bearing", "90", true},
		{"Acceptance: belt", "45", true},
		{"Acceptance: unrelated", "500", false},
	} {
		id := r.createFiltered(spec.title, "todo", "1")
		if id == "" {
			r.fail("actual minutes roll up to the linked asset", "could not create a work order")
			return
		}
		jobs = append(jobs, id)
		if spec.linked {
			if resp, err := r.client.PostForm(r.base+"/documents/"+id+"/link",
				url.Values{"target": {asset}}); err == nil {
				resp.Body.Close()
			}
		}
		if resp, err := r.client.PostForm(r.base+"/documents/"+id+"/time",
			url.Values{"minutes": {spec.minutes}, "note": {fmt.Sprintf("run %d", i)}}); err == nil {
			resp.Body.Close()
		}
	}
	defer func() {
		if r.keep {
			return
		}
		for _, id := range jobs {
			_, _ = r.client.PostForm(r.base+"/documents/"+id+"/delete", url.Values{})
		}
	}()

	// The estimate column that has been unused since the substrate landed.
	if resp, err := r.client.PostForm(r.base+"/documents/"+jobs[0]+"/estimate",
		url.Values{"estimate": {"60"}}); err == nil {
		resp.Body.Close()
	}
	first := r.get("/documents/" + jobs[0])
	r.check("estimate against actual is reported",
		strings.Contains(first, "Estimated 1h") && strings.Contains(first, "+50%"),
		"ninety minutes against a sixty minute estimate reads as fifty percent over")

	page := r.get("/documents/" + asset)
	// 90 + 45, and not the 500 on the unlinked job.
	r.check("actual minutes roll up to the linked asset",
		strings.Contains(page, `data-asset-minutes="135"`),
		"135 minutes across two linked work orders, and the unlinked one excluded")

	r.aRefusedTimeEntryStaysOnTheDocument()
	r.timerIsServerSide(jobs[0])
	r.workloadIgnoresCancelledWork()
}

// timerIsServerSide starts a timer and finds it from a browser that has never
// seen it.
func (r *runner) timerIsServerSide(docID string) {
	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/timer",
		url.Values{"note": {"acceptance"}}); err == nil {
		resp.Body.Close()
	}

	// A second timer must be refused, or the same hour lands on two jobs.
	resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/timer", url.Values{})
	if err == nil {
		resp.Body.Close()
		r.check("only one timer runs at once",
			resp.StatusCode == http.StatusConflict,
			fmt.Sprintf("HTTP %d, want 409", resp.StatusCode))
	}

	// A completely fresh browser: new cookie jar, new session, nothing carried
	// over. The timer is still there because it is a row rather than something
	// held in the process.
	fresh, err := r.signInAs(r.ownerEmail, r.ownerPassword)
	if err != nil {
		r.fail("a running timer is server-side and resumes", err.Error())
		return
	}
	body, _ := fresh.get("/documents")
	// Inside the timer block rather than anywhere on the page: every row of a
	// document list links to a document, so a bare id search would pass with
	// no timer rendered at all.
	block := between(body, `class="timer"`, "</form>")
	r.check("a running timer is server-side and resumes",
		block != "" && strings.Contains(block, "/documents/"+docID),
		"a browser that had never seen the timer found it running on the right job")

	if resp, err := r.client.PostForm(r.base+"/timer/stop", url.Values{}); err == nil {
		resp.Body.Close()
	}
	after := r.get("/documents")
	r.check("stopping the timer clears it everywhere",
		!strings.Contains(after, `class="timer"`), "no timer on the page")
}

// workloadIgnoresCancelledWork is the check that keeps a planner honest.
func (r *runner) workloadIgnoresCancelledWork() {
	principal := r.secondPrincipalID()
	if principal == "" {
		r.fail("workload never counts a cancelled work order", "no second account")
		return
	}
	// A generous week, so the percentages are small and readable.
	if resp, err := r.client.PostForm(r.base+"/people/"+principal+"/capacity",
		url.Values{"hours": {"40"}}); err == nil {
		resp.Body.Close()
	}

	// Due today, so it is inside the page's default window (Monday to Monday)
	// on any day of the week. It used to be two days out, which is next week
	// from Friday on, and this check failed every Friday, Saturday and Sunday.
	due := time.Now().Format("2006-01-02")
	var made []string
	for _, spec := range []struct {
		title    string
		estimate string
		cancel   bool
	}{
		{"Acceptance: load open", "120", false},
		{"Acceptance: load cancelled", "900", true},
	} {
		resp, err := r.client.PostForm(r.base+"/documents", url.Values{
			"kind": {"work_order"}, "title": {spec.title},
			"assignee": {principal}, "due": {due},
		})
		if err != nil {
			continue
		}
		resp.Body.Close()
		id := ""
		if m := docIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
			id = m[1]
		}
		if id == "" {
			continue
		}
		made = append(made, id)
		if resp, err := r.client.PostForm(r.base+"/documents/"+id+"/estimate",
			url.Values{"estimate": {spec.estimate}}); err == nil {
			resp.Body.Close()
		}
		if spec.cancel {
			page := r.get("/documents/" + id)
			version := "1"
			if m := versionRe.FindStringSubmatch(page); len(m) == 2 {
				version = m[1]
			}
			if resp, err := r.client.PostForm(r.base+"/documents/"+id+"/status",
				url.Values{"status": {"cancelled"}, "version": {version}}); err == nil {
				resp.Body.Close()
			}
		}
	}
	defer func() {
		if r.keep {
			return
		}
		for _, id := range made {
			_, _ = r.client.PostForm(r.base+"/documents/"+id+"/delete", url.Values{})
		}
	}()

	page := r.get("/workload")
	row := rowFor(page, principal)
	r.check("workload never counts a cancelled work order",
		strings.Contains(row, "2h") && !strings.Contains(row, "17h"),
		"the open two hours are counted and the cancelled fifteen are not")
	r.check("workload shows capacity and a load bar",
		strings.Contains(row, "loadbar") && strings.Contains(row, "40h"),
		"the week and the bar are both on the row")
}

// between returns the text from the first marker to the next end marker, or
// empty when the opening marker is not there at all.
func between(page, from, to string) string {
	start := strings.Index(page, from)
	if start < 0 {
		return ""
	}
	rest := page[start:]
	end := strings.Index(rest, to)
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// rowFor pulls one person's row out of the workload table, so a check reads
// that person's numbers rather than anybody else's.
func rowFor(page, principalID string) string {
	start := strings.Index(page, `data-load="`+principalID+`"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(page[start:], "</tr>")
	if end < 0 {
		return page[start:]
	}
	return page[start : start+end]
}

// -----------------------------------------------------------------------------
// Automations
// -----------------------------------------------------------------------------

var ruleIDRe = regexp.MustCompile(`data-rule="([0-9a-f-]{36})"`)

// automations is phase seven: once per event, one cycle when two rules chase
// each other, and a dry run that changes nothing.
func (r *runner) automations() {
	// Two rules that trigger each other. Left unguarded this is the failure
	// mode of every rules engine ever written.
	a := r.createRule(url.Values{
		"name":  {"Acceptance A: in progress becomes blocked"},
		"event": {"status_changed"}, "kind": {"work_order"},
		"cond_field": {"status"}, "cond_value": {"in_progress"},
		"action": {"set_field"}, "action_field": {"status"}, "action_value": {"blocked"},
	})
	b := r.createRule(url.Values{
		"name":  {"Acceptance B: blocked becomes in progress"},
		"event": {"status_changed"}, "kind": {"work_order"},
		"cond_field": {"status"}, "cond_value": {"blocked"},
		"action": {"set_field"}, "action_field": {"status"}, "action_value": {"in_progress"},
	})
	if a == "" || b == "" {
		r.fail("two rules that trigger each other stop after one cycle",
			"could not create the rules")
		return
	}
	defer func() {
		if r.keep {
			return
		}
		for _, id := range []string{a, b} {
			resp, err := r.client.PostForm(r.base+"/rules/"+id+"/delete", url.Values{})
			if err == nil {
				resp.Body.Close()
			}
		}
	}()

	// A rule arrives switched off, which is what makes a dry run possible
	// before it can do anything.
	page := r.get("/rules")
	r.check("a rule arrives switched off",
		strings.Count(page, ">off<") >= 2, "both new rules are off")

	r.dryRunChangesNothing(a)

	for _, id := range []string{a, b} {
		if resp, err := r.client.PostForm(r.base+"/rules/"+id+"/enabled",
			url.Values{"on": {"1"}}); err == nil {
			resp.Body.Close()
		}
	}

	docID := r.createFiltered("Acceptance: rule ping pong", "todo", "1")
	if docID == "" {
		r.fail("two rules that trigger each other stop after one cycle", "could not create")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()

	body := r.get("/documents/" + docID)
	version := "1"
	if m := versionRe.FindStringSubmatch(body); len(m) == 2 {
		version = m[1]
	}
	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/status",
		url.Values{"status": {"in_progress"}, "version": {version}}); err == nil {
		resp.Body.Close()
	}

	// The queue going quiet IS the check. An unguarded pair never settles.
	settled := r.waitForQuiet(30 * time.Second)
	after := r.get("/documents/" + docID)
	r.check("two rules that trigger each other stop after one cycle",
		settled && strings.Contains(after, "blocked"),
		"the queue went quiet and the status settled")

	// Once per event, not twice: exactly one rule fired.
	runs := r.get("/rules")
	real := strings.Count(runs, `data-matched="true" data-dry="false"`)
	r.check("a rule fires once per matching event", real == 1,
		fmt.Sprintf("%d rules matched that change", real))
}

func (r *runner) createRule(form url.Values) string {
	resp, err := r.client.PostForm(r.base+"/rules", form)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// The newest rule is whichever id is not already known, so the page is
	// searched for the name it was given.
	for _, m := range ruleIDRe.FindAllStringSubmatch(string(body), -1) {
		row := string(body)
		idx := strings.Index(row, m[1])
		if idx >= 0 && strings.Contains(row[idx:min(idx+400, len(row))], form.Get("name")) {
			return m[1]
		}
	}
	return ""
}

// dryRunChangesNothing replays a rule against real history and checks that it
// reported what it would do and did none of it.
func (r *runner) dryRunChangesNothing(ruleID string) {
	before := r.get("/rules")
	beforeReal := strings.Count(before, `data-matched="true" data-dry="false"`)

	if resp, err := r.client.PostForm(r.base+"/rules/"+ruleID+"/dryrun",
		url.Values{}); err == nil {
		resp.Body.Close()
	}
	after := r.get("/rules?rule=" + ruleID)

	r.check("a dry run reports what it would have done",
		strings.Contains(after, `data-dry="true"`) && strings.Contains(after, "would "),
		"the run log says what it would have done and marks it a rehearsal")
	// A dry run must never produce a real run, and the page distinguishes them.
	afterReal := strings.Count(after, `data-matched="true" data-dry="false"`)
	r.check("a dry run changes nothing", afterReal == beforeReal,
		fmt.Sprintf("%d real firings before, %d after", beforeReal, afterReal))
}

// -----------------------------------------------------------------------------
// Documents
// -----------------------------------------------------------------------------

// documentsBehaveLikeDocuments is phase six: a rendered body that cannot
// execute anything, a history that can be restored, and a template that
// carries its fields across.
func (r *runner) documentsBehaveLikeDocuments() {
	marker := nonce()
	body := strings.Join([]string{
		"# Restart procedure",
		"",
		"Isolate the drive **before** removing the guard.",
		"",
		// The hostile half. It has to come back as visible text, and it has to
		// not be a tag.
		"<script>alert(1)</script>",
		"",
		"Marker " + marker,
	}, "\n")

	resp, err := r.client.PostForm(r.base+"/documents", url.Values{
		"kind": {"doc"}, "title": {"Acceptance: rendered runbook"}, "body": {body},
	})
	if err != nil {
		r.fail("a heading renders as a heading", err.Error())
		return
	}
	resp.Body.Close()
	docID := ""
	if m := docIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		docID = m[1]
	}
	if docID == "" {
		r.fail("a heading renders as a heading", "could not create the document")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()

	page := r.get("/documents/" + docID)
	r.check("a heading renders as a heading",
		strings.Contains(page, "<h1>Restart procedure</h1>") &&
			strings.Contains(page, "<strong>before</strong>"),
		"markdown reached the page as markup")

	// The other half, and the one that matters. The escaped form must be
	// present and the live tag must not, anywhere on the page.
	r.check("a script tag does not execute",
		strings.Contains(page, "&lt;script&gt;") &&
			!strings.Contains(page, "<script>alert(1)</script>"),
		"the tag is shown as text rather than run")

	r.historyCanBeRestored(docID, marker)
	r.aTemplateCarriesItsFields()
}

// historyCanBeRestored edits, checks the diff, and puts the old text back.
func (r *runner) historyCanBeRestored(docID, marker string) {
	page := r.get("/documents/" + docID)
	version := "1"
	if m := versionRe.FindStringSubmatch(page); len(m) == 2 {
		version = m[1]
	}
	if resp, err := r.client.PostForm(r.base+"/documents/"+docID,
		url.Values{"body": {"Replaced entirely."}, "version": {version}}); err == nil {
		resp.Body.Close()
	}

	history := r.get("/documents/" + docID + "/history?v=2")
	r.check("an old version can be viewed",
		strings.Contains(history, "Version 1 to 2") || strings.Contains(history, "Restore"),
		"the history page lists the versions")
	r.check("the diff shows what moved",
		strings.Contains(history, `class="dl del"`) || strings.Contains(history, `class="dl add"`),
		"added and removed lines are marked")

	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/restore/1",
		url.Values{}); err == nil {
		resp.Body.Close()
	}
	after := r.get("/documents/" + docID)
	// Restoring writes a NEW version rather than rewinding, so the text is
	// back and the history is longer.
	r.check("an old version can be restored",
		strings.Contains(after, marker) && strings.Contains(after, "Version 3"),
		"the original text is back as a new version")
}

// aTemplateCarriesItsFields is the check that matters for templates: not that
// the body copies, which is obvious, but that the custom fields do.
func (r *runner) aTemplateCarriesItsFields() {
	// Opening the templates page seeds the field that says what a template
	// produces, the same way opening an asset seeds the asset fields.
	r.get("/templates")

	resp, err := r.client.PostForm(r.base+"/documents", url.Values{
		"kind": {"template"}, "title": {"Acceptance: quarterly service"},
		"body": {"Isolate, inspect, regrease."},
	})
	if err != nil {
		r.fail("a template produces a document with its properties set", err.Error())
		return
	}
	resp.Body.Close()
	tmplID := ""
	if m := docIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		tmplID = m[1]
	}
	if tmplID == "" {
		r.fail("a template produces a document with its properties set", "could not create it")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+tmplID+"/delete", url.Values{})
		}
	}()

	// Set the field that says what it makes, and a step.
	page := r.get("/documents/" + tmplID)
	creates := regexp.MustCompile(`id="prop-template_kind" name="prop:([0-9a-f-]{36})"`).
		FindStringSubmatch(page)
	if len(creates) != 2 {
		r.fail("a template produces a document with its properties set",
			"the template has no Creates field")
		return
	}
	if resp, err := r.client.PostForm(r.base+"/documents/"+tmplID+"/properties",
		url.Values{"prop:" + creates[1]: {"work_order"}}); err == nil {
		resp.Body.Close()
	}
	if resp, err := r.client.PostForm(r.base+"/documents/"+tmplID+"/checklist",
		url.Values{"text": {"Lock off at the panel"}}); err == nil {
		resp.Body.Close()
	}

	resp, err = r.client.PostForm(r.base+"/templates/"+tmplID+"/use",
		url.Values{"title": {"Acceptance: quarterly service, Conveyor 3"}})
	if err != nil {
		r.fail("a template produces a document with its properties set", err.Error())
		return
	}
	resp.Body.Close()
	madeID := ""
	if m := docIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		madeID = m[1]
	}
	r.check("a template produces a document", madeID != "" && madeID != tmplID,
		"id="+madeID)
	if madeID == "" {
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+madeID+"/delete", url.Values{})
		}
	}()

	made := r.get("/documents/" + madeID)
	r.check("a template produces a document with its properties set",
		strings.Contains(made, "Isolate, inspect, regrease.") &&
			strings.Contains(made, "Lock off at the panel") &&
			strings.Contains(made, "work_order"),
		"the body, the checklist, and the kind all came across")
	// And the steps arrive unticked, or the work appears already done.
	r.check("a copied checklist arrives unticked",
		!strings.Contains(made, `class="ticked"`),
		"nothing was marked complete before anybody started")
}

// -----------------------------------------------------------------------------
// Notifications
// -----------------------------------------------------------------------------

var notificationRe = regexp.MustCompile(`data-notification="([^"]+)"`)

// notifications is phase five, over HTTP. The checks are about restraint:
// exactly one, never your own, and stopping when told.
func (r *runner) notifications() {
	other, err := r.signInAs(secondEmail, secondPassword)
	if err != nil {
		r.fail("being assigned produces exactly one notification", err.Error())
		return
	}
	// Start from a clean inbox, so a previous run cannot make this pass or fail.
	if resp, err := other.post("/inbox/read", url.Values{}); err == nil {
		resp.Body.Close()
	}

	principal := r.secondPrincipalID()
	if principal == "" {
		r.fail("being assigned produces exactly one notification",
			"could not find the second account")
		return
	}

	docID := r.createFiltered("Acceptance: assigned to somebody else", "todo", "2")
	if docID == "" {
		r.fail("being assigned produces exactly one notification", "could not create")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()

	body := r.get("/documents/" + docID)
	version := "1"
	if m := versionRe.FindStringSubmatch(body); len(m) == 2 {
		version = m[1]
	}
	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/status",
		url.Values{"assignee": {principal}, "version": {version}}); err == nil {
		resp.Body.Close()
	}

	count := r.waitForInbox(other, 20*time.Second, 1)
	r.check("being assigned produces exactly one notification", count == 1,
		fmt.Sprintf("%d notifications", count))

	// The owner did all of that, so the owner hears nothing about it.
	mine := len(notificationRe.FindAllString(r.get("/inbox"), -1))
	r.check("your own change never notifies you", mine == 0,
		fmt.Sprintf("%d notifications for the person who made the change", mine))

	r.unwatchingStops(other, docID)
}

// secondPrincipalID reads the colleague's id off the people page, which is the
// only place the interface exposes one.
func (r *runner) secondPrincipalID() string {
	page := r.get("/people")
	// Each row links to that person's own page, where every control about them
	// lives now, so the id is read from the link rather than a row button.
	re := regexp.MustCompile(`href="/people/([0-9a-f-]{36})"`)
	rows := strings.Split(page, "<tr>")
	for _, row := range rows {
		if !strings.Contains(row, secondEmail) {
			continue
		}
		if m := re.FindStringSubmatch(row); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

// unwatchingStops is the check that the off switch works, and works at once.
func (r *runner) unwatchingStops(other *client, docID string) {
	// Being assigned started them watching, so this turns that off.
	if resp, err := other.post("/documents/"+docID+"/watch", url.Values{}); err == nil {
		resp.Body.Close()
	}
	if resp, err := other.post("/inbox/read", url.Values{}); err == nil {
		resp.Body.Close()
	}

	body := r.get("/documents/" + docID)
	version := "1"
	if m := versionRe.FindStringSubmatch(body); len(m) == 2 {
		version = m[1]
	}
	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/status",
		url.Values{"status": {"in_progress"}, "version": {version}}); err == nil {
		resp.Body.Close()
	}

	// Wait for the queue to settle rather than for a notification, because the
	// thing being checked is that none arrives.
	r.waitForQuiet(20 * time.Second)
	after := len(notificationRe.FindAllString(pageOf(other, "/inbox"), -1))
	r.check("unwatching stops delivery immediately", after == 0,
		fmt.Sprintf("%d notifications after unwatching", after))
}

func pageOf(c *client, path string) string {
	body, _ := c.get(path)
	return body
}

// waitForInbox polls somebody's unread list until it reaches a count.
func (r *runner) waitForInbox(c *client, limit time.Duration, want int) int {
	deadline := time.Now().Add(limit)
	last := 0
	for {
		last = len(notificationRe.FindAllString(pageOf(c, "/inbox"), -1))
		if last >= want || time.Now().After(deadline) {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// -----------------------------------------------------------------------------
// Search
// -----------------------------------------------------------------------------

var hitRe = regexp.MustCompile(`data-hit="([^"]+)"`)

// nonce is a word that exists nowhere else, so a hit can only be the thing this
// run just created. Without it, a leftover row from a previous run makes the
// check pass while the indexer is broken.
func nonce() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "zzyzx"
	}
	return "zzyzx" + hex.EncodeToString(b)
}

// searchWorks is phase three, over HTTP.
func (r *runner) searchWorks() {
	bodyWord := nonce()
	fileWord := nonce()

	docID := ""
	resp, err := r.client.PostForm(r.base+"/documents", url.Values{
		"kind":  {"work_order"},
		"title": {"Acceptance: searchable work order"},
		"body":  {"Motor current spiking. Distinctive marker " + bodyWord + " for the search check."},
	})
	if err != nil {
		r.fail("a word typed into a body is findable", err.Error())
		return
	}
	resp.Body.Close()
	if m := docIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		docID = m[1]
	}
	if docID == "" {
		r.fail("a word typed into a body is findable", "could not create the work order")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()

	// Five seconds is the budget the plan sets, and it is the honest one: a
	// person who saves a work order and then searches for it expects to find
	// it before they have finished typing the query.
	started := time.Now()
	found := r.waitForHits(5*time.Second, bodyWord, 1)
	r.check("a word typed into a body is findable within five seconds", found,
		fmt.Sprintf("took %s", time.Since(started).Round(10*time.Millisecond)))

	r.attachmentTextIsSearchable(docID, fileWord)
	r.searchRespectsGrants(bodyWord)
	r.backfillReproducesTheSameResults(bodyWord)
}

// attachmentTextIsSearchable uploads a file and finds it by its contents.
func (r *runner) attachmentTextIsSearchable(docID, word string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "acceptance-notes.txt")
	if err != nil {
		r.fail("an attachment is findable by its contents", err.Error())
		return
	}
	fmt.Fprintf(part, "Torque the retaining bolts to 45 Nm. Marker %s.\n", word)
	_ = mw.Close()

	resp, err := r.client.Post(r.base+"/documents/"+docID+"/files", mw.FormDataContentType(), &buf)
	if err != nil {
		r.fail("an attachment is findable by its contents", err.Error())
		return
	}
	resp.Body.Close()

	// Longer than the body budget, deliberately. Reading a file is a separate
	// job from indexing it, and the plan asks for the text to arrive rather
	// than for it to arrive instantly.
	found := r.waitForHits(20*time.Second, word, 1)
	r.check("an attachment is findable by its contents", found,
		"the text inside the upload was extracted and indexed")
}

// searchRespectsGrants is the disclosure check. Everything else about search is
// about finding things; this is the one about not finding them.
func (r *runner) searchRespectsGrants(word string) {
	other, err := r.signInAs(secondEmail, secondPassword)
	if err != nil {
		r.fail("search shows no result the caller has no grant for", err.Error())
		return
	}
	body, code := other.get("/search?q=" + url.QueryEscape(word))
	hits := hitRe.FindAllString(body, -1)
	r.check("search shows no result the caller has no grant for",
		code == 200 && len(hits) == 0,
		fmt.Sprintf("HTTP %d, %d hits for a document they cannot see", code, len(hits)))
}

// backfillReproducesTheSameResults rebuilds the index and asks the same
// question again. An index that cannot be rebuilt is a liability, and one that
// comes back different is worse than one that cannot.
func (r *runner) backfillReproducesTheSameResults(word string) {
	before := r.hitsFor(word)
	if len(before) == 0 {
		r.fail("a full backfill reproduces the same result set", "nothing to rebuild")
		return
	}

	resp, err := r.client.PostForm(r.base+"/ops/search/backfill", url.Values{})
	if err != nil {
		r.fail("a full backfill reproduces the same result set", err.Error())
		return
	}
	resp.Body.Close()

	// Wait for the queue to go quiet, then ask again.
	//
	// A far longer budget than anywhere else in this suite, and for a reason
	// that is about the operation rather than about the machine. Every other
	// wait here is for one event to be handled. A backfill re-indexes every
	// document and re-extracts every file, so its cost is the size of the whole
	// corpus, and the worker's throughput is bounded by its poll interval. A
	// fixed thirty seconds is therefore not a slow machine problem, it is a
	// budget that any instance with real data in it outgrows: this started
	// failing about one run in three once the local corpus reached a hundred
	// and twelve rows and files, and it would fail every time on a site with a
	// year of work in it.
	drained := r.waitForQuiet(5 * time.Minute)
	after := r.hitsFor(word)

	same := drained && len(after) == len(before)
	if same {
		want := map[string]bool{}
		for _, h := range before {
			want[h] = true
		}
		for _, h := range after {
			if !want[h] {
				same = false
			}
		}
	}
	r.check("a full backfill reproduces the same result set", same,
		fmt.Sprintf("%d hits before, %d after", len(before), len(after)))
}

// hitsFor returns the identities of everything a search finds.
func (r *runner) hitsFor(word string) []string {
	return hitRe.FindAllString(r.get("/search?q="+url.QueryEscape(word)), -1)
}

// waitForHits polls until a search finds at least n results.
func (r *runner) waitForHits(limit time.Duration, word string, n int) bool {
	deadline := time.Now().Add(limit)
	for {
		if len(r.hitsFor(word)) >= n {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// -----------------------------------------------------------------------------
// Background work
// -----------------------------------------------------------------------------

var deadRe = regexp.MustCompile(`/ops/jobs/dead/(\d+)/discard`)

// opsStat reads one number off the operations page.
func opsStat(body, label string) (int, bool) {
	re := regexp.MustCompile(`(?s)<dt>` + regexp.QuoteMeta(label) + `</dt>\s*<dd>\s*([0-9]+)\s*</dd>`)
	m := re.FindStringSubmatch(body)
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// backgroundWork proves the queue and the outbox on the deployment that is
// actually running, rather than only on a database on somebody's laptop.
func (r *runner) backgroundWork() {
	page := r.get("/ops/jobs")
	if !strings.Contains(page, "Background work") {
		r.fail("the operations page is reachable", "the owner could not open /ops/jobs")
		return
	}
	r.check("the operations page is reachable", true, "queue depth, dead letters, and routes")

	// Every write in this run has published an outbox event. The relay turns
	// them into jobs and marks them published, so the backlog has to reach zero
	// on its own. A number that never falls is the clearest possible sign that
	// the relay has stopped.
	drained := r.waitForOps(20*time.Second, func(body string) bool {
		n, ok := opsStat(body, "Unpublished events")
		return ok && n == 0
	})
	r.check("the outbox drains on its own", drained,
		"unpublished events reached zero without anybody asking")

	r.deadLetterPathWorks()
}

// deadLetterPathWorks runs the self test: a job that fails on purpose, twice,
// and then has to appear in the dead letter table.
//
// This is the house rule applied to the one mechanism whose entire job is
// handling failure. A retry path that has never been watched failing is a
// retry path nobody has any reason to believe in.
func (r *runner) deadLetterPathWorks() {
	before := r.get("/ops/jobs")
	beforeDead, _ := opsStat(before, "Dead")

	resp, err := r.client.PostForm(r.base+"/ops/jobs/selftest", url.Values{})
	if err != nil {
		r.fail("a job that fails is retried and then lands in the dead letter table", err.Error())
		return
	}
	resp.Body.Close()

	var final string
	landed := r.waitForOps(30*time.Second, func(body string) bool {
		final = body
		n, ok := opsStat(body, "Dead")
		return ok && n > beforeDead
	})
	r.check("a job that fails is retried and then lands in the dead letter table", landed,
		fmt.Sprintf("dead letters went from %d up", beforeDead))
	if !landed {
		return
	}

	// It has to say WHY, or the table records that something failed and never
	// what, which is the same as recording nothing.
	r.check("the dead letter says what went wrong",
		strings.Contains(final, "this job fails on purpose"),
		"the failure reason is on the page")

	// Two attempts, because the self test asks for two. A retry path that gives
	// up on the first failure is not a retry path.
	r.check("the failed job was retried before it gave up",
		regexp.MustCompile(`<td><code>selftest\.fail</code></td>\s*<td>2</td>`).MatchString(final),
		"two attempts recorded")

	// Clean up after itself, like everything else here.
	if r.keep {
		return
	}
	if m := deadRe.FindStringSubmatch(final); len(m) == 2 {
		if resp, err := r.client.PostForm(r.base+"/ops/jobs/dead/"+m[1]+"/discard", url.Values{}); err == nil {
			resp.Body.Close()
		}
		after := r.get("/ops/jobs")
		afterDead, ok := opsStat(after, "Dead")
		r.check("the self test cleans up after itself", ok && afterDead == beforeDead,
			fmt.Sprintf("dead letters back to %d", afterDead))
	}
}

// waitForQuiet waits until there is no work left anywhere.
//
// Three numbers, not two. An unpublished outbox event is work that has not
// become a job yet, so a queue with zero ready and zero in flight can still
// have everything ahead of it. Checking only the two produced a flake that
// looked like a rule failing to fire and was a check asking too early.
func (r *runner) waitForQuiet(limit time.Duration) bool {
	return r.waitForOps(limit, func(page string) bool {
		ready, ok1 := opsStat(page, "Ready")
		flight, ok2 := opsStat(page, "In flight")
		unpublished, ok3 := opsStat(page, "Unpublished events")
		return ok1 && ok2 && ok3 && ready == 0 && flight == 0 && unpublished == 0
	})
}

// waitForOps polls the operations page until a condition holds.
//
// Polling rather than sleeping a fixed time: a fixed sleep is either flaky on a
// loaded cluster or slow on an idle one, and usually manages both.
func (r *runner) waitForOps(limit time.Duration, cond func(body string) bool) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond(r.get("/ops/jobs")) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func (r *runner) signOutRevokes() {
	if _, err := r.client.PostForm(r.base+"/logout", url.Values{}); err != nil {
		r.fail("sign out", err.Error())
		return
	}
	resp, err := r.client.Get(r.base + "/documents")
	if err != nil {
		r.fail("sign out revokes the session", err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// Redirected to the login page, so the final body is the login form.
	r.check("sign out revokes the session",
		strings.Contains(string(body), "Sign in"), "landed back on the login page")
}

func (r *runner) anonymousIsDenied(docID string) {
	// A brand new client, no cookies at all.
	anon := &http.Client{Transport: asksForData{http.DefaultTransport}, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := anon.Get(r.base + "/documents/" + docID)
	if err != nil {
		r.fail("an anonymous caller reaches nothing", err.Error())
		return
	}
	defer resp.Body.Close()
	r.check("an anonymous caller reaches nothing",
		resp.StatusCode == http.StatusSeeOther, fmt.Sprintf("HTTP %d, want 303", resp.StatusCode))
}

func (r *runner) get(path string) string {
	resp, err := r.client.Get(r.base + path)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func (r *runner) check(name string, ok bool, note string) {
	r.results = append(r.results, result{name: name, ok: ok, note: note})
}

func (r *runner) fail(name, note string) { r.check(name, false, note) }

func (r *runner) report() int {
	failed := 0
	fmt.Println()
	for _, res := range r.results {
		mark := "PASS"
		if !res.ok {
			mark, failed = "FAIL", failed+1
		}
		fmt.Printf("  %-4s  %-52s %s\n", mark, res.name, res.note)
	}
	fmt.Printf("\n%d checks, %d failed\n", len(r.results), failed)
	if failed > 0 {
		return 1
	}
	return 0
}

// -----------------------------------------------------------------------------
// Preventive maintenance
// -----------------------------------------------------------------------------

var scheduleIDRe = regexp.MustCompile(`data-schedule="([0-9a-f-]{36})"`)

// preventiveMaintenance is the phase that decides whether this is a maintenance
// system or a task list with a calendar bolted on.
//
// Every check here is one failure mode: the pile. A crew who arrive to find
// four identical jobs on one pump stop reading the list, and nothing shipped
// afterwards brings them back.
func (r *runner) preventiveMaintenance() {
	asset := r.createAsset("Acceptance: pump with a schedule")
	if asset == "" {
		r.fail("a schedule raises exactly one work order", "could not create the asset")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+asset+"/delete", url.Values{})
		}
	}()

	// Daily, with no notice period, so it is due the moment it exists.
	resp, err := r.client.PostForm(r.base+"/maintenance", url.Values{
		"asset": {asset}, "name": {"Acceptance: daily check"},
		"every_days": {"1"}, "lead_days": {"0"},
	})
	if err != nil {
		r.fail("a schedule raises exactly one work order", err.Error())
		return
	}
	resp.Body.Close()

	page := r.get("/documents/" + asset)
	m := scheduleIDRe.FindStringSubmatch(page)
	if len(m) != 2 {
		r.fail("a schedule raises exactly one work order", "the schedule did not appear on the asset")
		return
	}
	scheduleID := m[1]
	r.check("a schedule appears on the asset that carries it",
		strings.Contains(page, "Acceptance: daily check") && strings.Contains(page, "daily"),
		"named, with its interval in words")
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/maintenance/"+scheduleID+"/delete", url.Values{})
		}
	}()

	// Raise it, twice. The second must produce nothing.
	first := r.raiseSchedule(scheduleID)
	if first == "" {
		r.fail("a schedule raises exactly one work order", "nothing was raised")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+first+"/delete", url.Values{})
		}
	}()
	second := r.raiseSchedule(scheduleID)
	r.check("a schedule raises exactly one work order",
		second == "" || second == first,
		"pressing raise twice produced one job, because one occurrence can be open at a time")

	job := r.get("/documents/" + first)
	r.check("the generated work order is linked back to its asset",
		strings.Contains(job, "/documents/"+asset),
		"anybody looking at the job can find the machine")

	onAsset := r.get("/documents/" + asset)
	r.check("an open job is shown on the schedule rather than a second raise button",
		strings.Contains(between(onAsset, `data-schedule="`+scheduleID, "</li>"), "open now"),
		"the schedule says which job is outstanding")

	r.completionSchedulesTheNext(asset, scheduleID, first)
	r.meterReadingMovesTheDate(asset)
	r.complianceIsAnswerable()
	r.deletingTheAssetStopsTheWork(asset, scheduleID)
}

// raiseSchedule presses the button and returns the work order it landed on.
func (r *runner) raiseSchedule(scheduleID string) string {
	resp, err := r.client.PostForm(r.base+"/maintenance/"+scheduleID+"/raise", url.Values{})
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if m := docIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		return m[1]
	}
	return ""
}

// completionSchedulesTheNext is the half that makes the series real: a job
// being finished is what moves the schedule on.
func (r *runner) completionSchedulesTheNext(asset, scheduleID, workOrder string) {
	if resp, err := r.client.PostForm(r.base+"/documents/"+workOrder+"/status",
		url.Values{"status": {"done"}}); err == nil {
		resp.Body.Close()
	}
	// The successor is scheduled by a job, so wait for the queue rather than
	// racing it.
	if !r.waitForQuiet(30 * time.Second) {
		r.fail("completing the job closes its occurrence", "the queue never went quiet")
		return
	}

	page := r.get("/documents/" + asset)
	row := between(page, `data-schedule="`+scheduleID, "</li>")
	r.check("completing the job closes its occurrence",
		row != "" && !strings.Contains(row, "open now"),
		"nothing is outstanding, so the next one can come round")
	r.check("the schedule moves forward from the completion",
		row != "" && !strings.Contains(row, "overdue"),
		"a job finished today is not immediately due again, which is what "+
			"counting from the due date would do")

	history := between(page, "Maintenance history", "</ul>")
	r.check("the maintenance history records what was done",
		strings.Contains(history, "on time"),
		"the occurrence is kept whatever happens to the work order afterwards")
}

// meterReadingMovesTheDate: "every 500 run hours" is not a date until somebody
// reads the gauge.
func (r *runner) meterReadingMovesTheDate(asset string) {
	resp, err := r.client.PostForm(r.base+"/maintenance", url.Values{
		"asset": {asset}, "name": {"Acceptance: 500 hour service"},
		"every_days": {"3650"}, "meter_key": {"run_hours"}, "every_units": {"500"},
		"lead_days": {"7"},
	})
	if err != nil {
		r.fail("a meter reading is recorded against the asset", err.Error())
		return
	}
	resp.Body.Close()

	for _, v := range []string{"120", "260"} {
		if resp, err := r.client.PostForm(r.base+"/documents/"+asset+"/readings",
			url.Values{"key": {"run_hours"}, "value": {v}, "note": {"acceptance"}}); err == nil {
			resp.Body.Close()
		}
	}

	page := r.get("/documents/" + asset)
	readings := between(page, "data-readings", "</ul>")
	r.check("a meter reading is recorded against the asset",
		strings.Contains(readings, `data-reading="run_hours"`) && strings.Contains(readings, "260"),
		"the gauge value is on the machine, with who read it and when")

	// The schedule offers the meter it has readings for, rather than asking
	// somebody to remember the spelling.
	r.check("the asset offers the meters it already has",
		strings.Contains(page, `<option value="run_hours">`),
		"the meter list is built from what has been read")
}

// deletingTheAssetStopsTheWork: a schedule must not outlive the machine.
//
// Deleting a document is a soft delete, so nothing cascades and the schedule is
// still sitting there. If it were still swept, a press scrapped in March would
// still be producing work orders in June.
func (r *runner) deletingTheAssetStopsTheWork(asset, scheduleID string) {
	if resp, err := r.client.PostForm(r.base+"/documents/"+asset+"/delete",
		url.Values{}); err == nil {
		resp.Body.Close()
	}
	page := r.get("/maintenance")
	r.check("deleting an asset takes its schedules with it",
		!strings.Contains(page, `data-schedule="`+scheduleID+`"`),
		"a scrapped machine stops producing work orders")
}

// complianceIsAnswerable is the report an auditor asks for.
func (r *runner) complianceIsAnswerable() {
	page := r.get("/maintenance")
	summary := between(page, "data-compliance", "</div>")
	r.check("compliance reports what was due, done, and missed",
		strings.Contains(summary, "data-due=") &&
			strings.Contains(summary, "data-done-on-time=") &&
			strings.Contains(summary, "data-late=") &&
			strings.Contains(summary, "data-missed="),
		"four numbers, counted from occurrences rather than from work orders")

	due, ok1 := attrInt(summary, "data-due")
	onTime, ok2 := attrInt(summary, "data-done-on-time")
	late, ok3 := attrInt(summary, "data-late")
	missed, ok4 := attrInt(summary, "data-missed")
	open, ok5 := attrInt(summary, "data-open")
	r.check("the compliance numbers add up to what was due",
		ok1 && ok2 && ok3 && ok4 && ok5 && onTime+late+missed+open == due,
		fmt.Sprintf("%d on time + %d late + %d missed + %d open = %d due",
			onTime, late, missed, open, due))

	pct, okp := attrInt(summary, "data-compliance-percent")
	settled := onTime + late + missed
	want := 100
	if settled > 0 {
		want = (onTime + late) * 100 / settled
	}
	r.check("the compliance percentage matches its own rows",
		okp && pct == want,
		fmt.Sprintf("%d%% completed, computed from the rows on the page", pct))
}

// attrInt reads one integer attribute out of a fragment.
//
// Anchored at the attribute rather than searched for with a regex over the
// rest of the fragment. An unanchored search reads the NEXT attribute's number
// when this one holds something unexpected, which is how a check reports a
// plausible wrong answer instead of failing.
func attrInt(fragment, name string) (int, bool) {
	i := strings.Index(fragment, name+`="`)
	if i < 0 {
		return 0, false
	}
	rest := fragment[i+len(name)+2:]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:end])
	return n, err == nil
}

// -----------------------------------------------------------------------------
// The plant tree
// -----------------------------------------------------------------------------

var spaceIDRe = regexp.MustCompile(`data-space="([0-9a-f-]{36})"`)

// plantTree proves the three claims the phase is judged on: an open work order
// counts on every ancestor of its asset, a cycle is refused rather than hung,
// and scanning an asset code lands on that asset.
func (r *runner) plantTree() {
	// Four levels, because a rollup that only works one deep is not a rollup.
	site := r.createAsset("Acceptance: north site")
	line := r.createAsset("Acceptance: line 3")
	machine := r.createAsset("Acceptance: conveyor")
	bearing := r.createAsset("Acceptance: bearing")
	if site == "" || line == "" || machine == "" || bearing == "" {
		r.fail("an open work order counts on every ancestor of its asset",
			"could not create the assets")
		return
	}
	defer func() {
		if r.keep {
			return
		}
		for _, id := range []string{bearing, machine, line, site} {
			_, _ = r.client.PostForm(r.base+"/documents/"+id+"/delete", url.Values{})
		}
	}()

	r.placeAsset(line, site, "")
	r.placeAsset(machine, line, "")
	r.placeAsset(bearing, machine, "ACC-BRG-1")

	// One open job at the very bottom.
	job := r.createFiltered("Acceptance: bearing is rumbling", "todo", "2")
	if job == "" {
		r.fail("an open work order counts on every ancestor of its asset",
			"could not create the work order")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+job+"/delete", url.Values{})
		}
	}()
	if resp, err := r.client.PostForm(r.base+"/documents/"+job+"/link",
		url.Values{"target": {bearing}}); err == nil {
		resp.Body.Close()
	}

	page := r.get("/plant")
	counted := 0
	for _, id := range []string{site, line, machine, bearing} {
		row := between(page, `data-asset="`+id+`"`, "</tr>")
		if n, ok := attrInt(row, "data-open-rolled"); ok && n >= 1 {
			counted++
		}
	}
	r.check("an open work order counts on every ancestor of its asset",
		counted == 4,
		fmt.Sprintf("%d of the 4 levels report it, and the failure is four levels "+
			"below the one somebody is asking about", counted))

	// The tree is drawn as a tree, not a flat list.
	r.check("the plant is drawn as a tree",
		strings.Contains(between(page, `data-asset="`+bearing+`"`, "</tr>"), `data-depth="3"`),
		"the bearing sits three levels under the site")

	r.cycleIsRefused(site, bearing)
	r.scanningLandsOnTheAsset(bearing, "ACC-BRG-1")
	r.downtimeRollsUp(line, machine)
	r.spacesHoldWork(job)
}

// placeAsset puts one asset under another and optionally gives it a code.
func (r *runner) placeAsset(id, parent, code string) {
	resp, err := r.client.PostForm(r.base+"/documents/"+id+"/asset",
		url.Values{"parent": {parent}, "code": {code}})
	if err == nil {
		resp.Body.Close()
	}
}

// cycleIsRefused is the check that matters most, because the failure it
// prevents is not a wrong number, it is a request that never returns.
func (r *runner) cycleIsRefused(site, bearing string) {
	resp, err := r.client.PostForm(r.base+"/documents/"+site+"/asset",
		url.Values{"parent": {bearing}})
	if err != nil {
		r.fail("a cycle in the tree is refused rather than hanging the query", err.Error())
		return
	}
	defer resp.Body.Close()
	r.check("a cycle in the tree is refused rather than hanging the query",
		resp.StatusCode == http.StatusBadRequest,
		fmt.Sprintf("HTTP %d, want 400: putting the site inside its own bearing",
			resp.StatusCode))

	// And the tree still answers afterwards, in reasonable time. A guard that
	// refuses the write but leaves the page hung has done half a job.
	start := time.Now()
	page := r.get("/plant")
	r.check("the tree still answers after a cycle was attempted",
		strings.Contains(page, `data-asset="`+site+`"`) && time.Since(start) < 10*time.Second,
		fmt.Sprintf("rendered in %s", time.Since(start).Round(time.Millisecond)))
}

// scanningLandsOnTheAsset is the whole reason for putting a label on a machine.
func (r *runner) scanningLandsOnTheAsset(assetID, code string) {
	resp, err := r.client.Get(r.base + "/a/" + code)
	if err != nil {
		r.fail("scanning an asset code lands on that asset", err.Error())
		return
	}
	defer resp.Body.Close()
	r.check("scanning an asset code lands on that asset",
		strings.HasSuffix(resp.Request.URL.Path, "/documents/"+assetID),
		"the code redirected to the machine it is stuck to, at "+resp.Request.URL.Path)

	// Lower case too, because somebody will type it off an oily label.
	lower, err := r.client.Get(r.base + "/a/" + strings.ToLower(code))
	if err == nil {
		lower.Body.Close()
		r.check("a code typed in lower case still finds the machine",
			strings.HasSuffix(lower.Request.URL.Path, "/documents/"+assetID),
			"case does not matter, because a person reads the label as often as a scanner")
	}

	// A code that does not exist is a clean refusal rather than a redirect to
	// nowhere or a page that says which machines do exist.
	missing, err := r.client.Get(r.base + "/a/ACC-NOPE-9999")
	if err == nil {
		missing.Body.Close()
		r.check("an unknown code is refused rather than guessed at",
			missing.StatusCode == http.StatusNotFound,
			fmt.Sprintf("HTTP %d, want 404", missing.StatusCode))
	}

	// And the printable label carries a real symbol.
	label := r.get("/documents/" + assetID + "/label")
	// the symbol is the one inside the label's own box: the side panel above it carries the Hearth mark, which is also an svg
	svg := between(between(label, `class="label-symbol"`, "</div>"), "<svg", "</svg>")
	r.check("the label carries a scannable symbol",
		strings.Contains(label, code) &&
			strings.Contains(svg, "<rect") && strings.Count(svg, "<rect") > 10,
		fmt.Sprintf("%d rectangles and the code in text underneath",
			strings.Count(svg, "<rect")))
	r.check("the label says the address it scans to",
		strings.Contains(label, "/a/"+code),
		"printed under the symbol, so an unreadable label can still be typed")
}

// downtimeRollsUp: the hours a line was not running is the number a replacement
// argument is actually made of.
func (r *runner) downtimeRollsUp(line, machine string) {
	from := time.Now().Add(-3 * time.Hour).Format("2006-01-02T15:04")
	to := time.Now().Add(-1 * time.Hour).Format("2006-01-02T15:04")
	if resp, err := r.client.PostForm(r.base+"/documents/"+machine+"/downtime",
		url.Values{"action": {"log"}, "from": {from}, "to": {to},
			"note": {"acceptance"}}); err == nil {
		resp.Body.Close()
	}

	page := r.get("/plant")
	row := between(page, `data-asset="`+line+`"`, "</tr>")
	got, ok := attrInt(row, "data-downtime-rolled")
	r.check("downtime on a machine counts against the line above it",
		ok && got >= 115 && got <= 125,
		fmt.Sprintf("%d minutes rolled up to the line, want about 120", got))

	// Planned downtime must not be counted as a breakdown, or the annual
	// shutdown makes the best-maintained machine on site look like the worst.
	pFrom := time.Now().Add(-9 * time.Hour).Format("2006-01-02T15:04")
	pTo := time.Now().Add(-5 * time.Hour).Format("2006-01-02T15:04")
	if resp, err := r.client.PostForm(r.base+"/documents/"+machine+"/downtime",
		url.Values{"action": {"log"}, "from": {pFrom}, "to": {pTo},
			"planned": {"on"}, "note": {"acceptance shutdown"}}); err == nil {
		resp.Body.Close()
	}
	after, _ := attrInt(between(r.get("/plant"), `data-asset="`+line+`"`, "</tr>"),
		"data-downtime-rolled")
	r.check("planned downtime is not counted as a breakdown",
		after == got,
		fmt.Sprintf("still %d minutes after four hours of planned shutdown", after))

	// And a machine cannot be down twice at once, or one outage becomes two.
	if resp, err := r.client.PostForm(r.base+"/documents/"+machine+"/downtime",
		url.Values{"action": {"down"}}); err == nil {
		resp.Body.Close()
	}
	second, err := r.client.PostForm(r.base+"/documents/"+machine+"/downtime",
		url.Values{"action": {"down"}})
	if err == nil {
		second.Body.Close()
		r.check("a machine cannot be recorded as down twice at once",
			second.StatusCode == http.StatusConflict,
			fmt.Sprintf("HTTP %d, want 409: two overlapping records is one outage counted twice",
				second.StatusCode))
	}
	if resp, err := r.client.PostForm(r.base+"/documents/"+machine+"/downtime",
		url.Values{"action": {"up"}}); err == nil {
		resp.Body.Close()
	}
}

// spacesHoldWork spends a column the substrate has carried unused since the
// beginning.
func (r *runner) spacesHoldWork(docID string) {
	resp, err := r.client.PostForm(r.base+"/spaces",
		url.Values{"name": {"Acceptance: north building"}, "kind": {"area"}})
	if err != nil {
		r.fail("a document can be filed in a space", err.Error())
		return
	}
	resp.Body.Close()

	page := r.get("/spaces")
	m := spaceIDRe.FindStringSubmatch(page)
	if len(m) != 2 {
		r.fail("a document can be filed in a space", "the space did not appear")
		return
	}
	spaceID := m[1]
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/space", url.Values{})
			_, _ = r.client.PostForm(r.base+"/spaces/"+spaceID+"/delete", url.Values{})
		}
	}()

	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/space",
		url.Values{"space": {spaceID}}); err == nil {
		resp.Body.Close()
	}
	after := between(r.get("/spaces"), `data-space="`+spaceID+`"`, "</li>")
	r.check("a document can be filed in a space",
		strings.Contains(after, "1 document"),
		"space_id is finally written, after carrying no value since the substrate landed")

	// A space with work in it cannot be deleted, because the alternative is
	// either orphaning the documents or taking them with it.
	del, err := r.client.PostForm(r.base+"/spaces/"+spaceID+"/delete", url.Values{})
	if err == nil {
		del.Body.Close()
		r.check("a space holding work cannot be deleted by accident",
			del.StatusCode == http.StatusBadRequest,
			fmt.Sprintf("HTTP %d, want 400", del.StatusCode))
	}

	// And a space cannot contain itself.
	cycle, err := r.client.PostForm(r.base+"/spaces/"+spaceID+"/move",
		url.Values{"parent": {spaceID}})
	if err == nil {
		cycle.Body.Close()
		r.check("a space cannot be moved inside itself",
			cycle.StatusCode == http.StatusBadRequest,
			fmt.Sprintf("HTTP %d, want 400", cycle.StatusCode))
	}
}

// -----------------------------------------------------------------------------
// Contacts, companies, and parts
// -----------------------------------------------------------------------------

var (
	companyIDRe = regexp.MustCompile(`/companies/([0-9a-f-]{36})`)
	contactIDRe = regexp.MustCompile(`/contacts/([0-9a-f-]{36})`)
	partIDRe    = regexp.MustCompile(`data-part="([0-9a-f-]{36})"`)
)

// crmAndParts proves the two claims the phase is judged on: a contact is
// searchable and grantable like a document, and parts consumed appear in the
// work order cost.
func (r *runner) crmAndParts() {
	// Stamped, because a company's domain is unique per org and the fixed name
	// this used to carry made the whole section unrunnable after an interrupted
	// run: the previous attempt's cleanup never fired, the next one got a 409
	// from the unique index, and the failure read as a broken product rather
	// than as leftovers. Found against the live site, by killing a run halfway
	// and starting another.
	stamp := nonce()
	companyID := r.createCompany("Acceptance Bearings "+stamp,
		stamp+".acceptance-bearings.example", "vendor")
	if companyID == "" {
		r.fail("a contact is searchable and grantable like a document",
			"could not create the company")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/companies/"+companyID+"/delete", url.Values{})
		}
	}()

	// A name nothing else in the database will contain, so a hit is this
	// contact rather than a coincidence. The address is unique per org too, so
	// it carries the same stamp for the same reason the domain does.
	word := "quillfeather" + fmt.Sprint(time.Now().UnixNano()%100000)
	contactID := r.createContact(companyID, "Dana "+word,
		word+"@"+stamp+".acceptance-bearings.example", "Rewind supervisor")
	if contactID == "" {
		r.fail("a contact is searchable and grantable like a document",
			"could not create the contact")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/contacts/"+contactID+"/delete", url.Values{})
		}
	}()

	page := r.get("/contacts/" + contactID)
	r.check("a contact is a page of its own",
		strings.Contains(page, word) && strings.Contains(page, "Rewind supervisor"),
		"name, title, and the company it belongs to")

	r.contactIsFindableAndPrivate(contactID, word)
	r.aVendorLinksToTheMachinesTheyService(companyID)
	r.aNoteAboutTwoCompanies(companyID, stamp)
	r.partsAppearInTheCost(stamp)
	r.aCSVBringsInCompaniesContactsAndDeals(stamp)
	r.aCRMListGroupsAndKeepsItsViews(stamp)
	r.aCRMRecordCanBeStarred(stamp)
}

func (r *runner) createCompany(name, domain, kind string) string {
	resp, err := r.client.PostForm(r.base+"/companies", url.Values{
		"name": {name}, "domain": {domain}, "kind": {kind},
	})
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if m := companyIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		return m[1]
	}
	return ""
}

func (r *runner) createContact(companyID, name, email, title string) string {
	resp, err := r.client.PostForm(r.base+"/contacts", url.Values{
		"company": {companyID}, "name": {name}, "email": {email}, "title": {title},
	})
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if m := contactIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
		return m[1]
	}
	return ""
}

// contactIsFindableAndPrivate is the criterion, and it is two claims in one:
// the same search box finds a person, and the same grants keep them from
// somebody who has none.
func (r *runner) contactIsFindableAndPrivate(contactID, word string) {
	// Indexing goes through the queue, so wait for it rather than racing it.
	if !r.waitForQuiet(30 * time.Second) {
		r.fail("a contact is searchable and grantable like a document",
			"the queue never went quiet")
		return
	}
	body := r.get("/search?q=" + url.QueryEscape(word))
	r.check("a contact is searchable and grantable like a document",
		strings.Contains(body, "/contacts/"+contactID),
		"the same search box that finds a work order finds the person")

	other, err := r.signInAs(secondEmail, secondPassword)
	if err != nil {
		r.fail("a contact is hidden from somebody with no grant", err.Error())
		return
	}
	theirs, code := other.get("/search?q=" + url.QueryEscape(word))
	r.check("a contact is hidden from somebody with no grant",
		code == 200 && !strings.Contains(theirs, "/contacts/"+contactID),
		fmt.Sprintf("HTTP %d, and the contact is not in their results", code))

	direct, code := other.get("/contacts/" + contactID)
	r.check("a contact is not reachable by id without a grant",
		code == http.StatusNotFound,
		fmt.Sprintf("HTTP %d, want 404", code))
	_ = direct
}

// aVendorLinksToTheMachinesTheyService is the maintenance-shaped reason to have
// a CRM at all.
func (r *runner) aVendorLinksToTheMachinesTheyService(companyID string) {
	asset := r.createAsset("Acceptance: compressor they service")
	if asset == "" {
		r.fail("a vendor links to the machines they service", "could not create the asset")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+asset+"/delete", url.Values{})
		}
	}()

	if resp, err := r.client.PostForm(r.base+"/companies/"+companyID+"/link",
		url.Values{"target": {asset}}); err == nil {
		resp.Body.Close()
	}
	page := r.get("/companies/" + companyID)
	linked := between(page, "data-linked", "</ul>")
	r.check("a vendor links to the machines they service",
		strings.Contains(linked, "/documents/"+asset),
		"the compressor is on the vendor page, through the same links "+
			"everything else uses")

	// And the other way round, because it is one row.
	fromAsset := between(r.get("/documents/"+asset), "data-parties", "</ul>")
	r.check("the machine points back at the vendor",
		strings.Contains(fromAsset, "/companies/"+companyID),
		"one row in entity_links, and a relationship only visible from one "+
			"side is one people stop making")
}

// aNoteAboutTwoCompanies is the add-note form's "also about" field: one note,
// two companies, on both timelines, and a name that matches nothing refused.
func (r *runner) aNoteAboutTwoCompanies(companyID, stamp string) {
	other := r.createCompany("Acceptance Rewinds "+stamp, stamp+".acceptance-rewinds.example", "customer")
	if other == "" {
		r.fail("a note about two companies", "could not create the second company")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/companies/"+other+"/delete", url.Values{})
		}
	}()
	title := "Joint visit " + stamp
	post := func(also string) int {
		resp, err := r.client.PostForm(r.base+"/activities", url.Values{
			"target_type": {"company"}, "target_id": {companyID}, "kind": {"note"},
			"title": {title}, "body": {"met both"}, "also_company": {also},
		})
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("nothing-like-this-" + stamp + ".example"); code != http.StatusBadRequest {
		r.fail("a note about a company nobody has is refused", fmt.Sprintf("status %d", code))
	} else {
		r.check("a note about a company nobody has is refused",
			!strings.Contains(r.get("/companies/"+companyID), title), "nothing was written")
	}
	post(stamp + ".acceptance-rewinds.example")
	r.check("a note about two companies is on both timelines",
		strings.Contains(r.get("/companies/"+companyID), title) && strings.Contains(r.get("/companies/"+other), title),
		"one activity, two targets, found by its domain")
}

// partsAppearInTheCost is the second criterion.
//
// The part number is stamped for the same reason the company domain is: it is
// unique per org, so a fixed one makes this unrunnable after an interrupted run
// leaves the previous part behind.
func (r *runner) partsAppearInTheCost(stamp string) {
	sku := "ACC-6205-" + strings.ToUpper(stamp[len(stamp)-6:])
	// A part at a price nothing else uses, so the total is unambiguous.
	if resp, err := r.client.PostForm(r.base+"/parts", url.Values{
		"name": {"Acceptance bearing " + stamp}, "sku": {sku}, "cost": {"12.50"},
		"reorder": {"2"},
	}); err != nil {
		r.fail("parts consumed appear in the work order cost", err.Error())
		return
	} else {
		resp.Body.Close()
	}

	page := r.get("/parts")
	row := between(page, sku, "</tr>")
	partID := ""
	// The row is found by its part number; the id is on the tr above it.
	for _, m := range partIDRe.FindAllStringSubmatch(page, -1) {
		if strings.Contains(between(page, `data-part="`+m[1]+`"`, "</tr>"), sku) {
			partID = m[1]
		}
	}
	if partID == "" {
		r.fail("parts consumed appear in the work order cost", "the part did not appear")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/parts/"+partID+"/delete", url.Values{})
		}
	}()
	_ = row

	job := r.createFiltered("Acceptance: bearing change with parts", "todo", "1")
	if job == "" {
		r.fail("parts consumed appear in the work order cost", "could not create the job")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+job+"/delete", url.Values{})
		}
	}()

	if resp, err := r.client.PostForm(r.base+"/documents/"+job+"/parts",
		url.Values{"part": {partID}, "quantity": {"2"}, "action": {"consume"}}); err == nil {
		resp.Body.Close()
	}

	after := r.get("/documents/" + job)
	cents, ok := attrInt(after, "data-parts-cents")
	r.check("parts consumed appear in the work order cost",
		ok && cents == 2500,
		fmt.Sprintf("two bearings at 12.50 is %d cents on the job", cents))

	// Stock came off the shelf, and the reorder point noticed.
	parts := r.get("/parts")
	stock := between(parts, `data-part="`+partID+`"`, "</tr>")
	onHand, hadStock := attrInt(stock, "data-stock")
	r.check("fitting a part takes it off the shelf",
		hadStock && onHand == -2,
		fmt.Sprintf("%d on hand after fitting two that were never booked in, "+
			"which is what a shelf that has not had a stocktake looks like", onHand))

	// A correction is a movement with a reason on it, not a number that
	// changed with no explanation attached.
	if resp, err := r.client.PostForm(r.base+"/parts/"+partID+"/adjust",
		url.Values{"delta": {"10"}, "note": {"acceptance stocktake"}}); err == nil {
		resp.Body.Close()
	}
	corrected, _ := attrInt(between(r.get("/parts"), `data-part="`+partID+`"`, "</tr>"),
		"data-stock")
	r.check("a stocktake correction is recorded rather than typed over the count",
		corrected == 8,
		fmt.Sprintf("%d on hand after a correction of ten", corrected))

	// An unexplained correction is refused.
	blank, err := r.client.PostForm(r.base+"/parts/"+partID+"/adjust",
		url.Values{"delta": {"5"}, "note": {""}})
	if err == nil {
		blank.Body.Close()
		r.check("a correction with no explanation is refused",
			blank.StatusCode == http.StatusBadRequest,
			fmt.Sprintf("HTTP %d, want 400", blank.StatusCode))
	}

	// And the parts cost reaches the asset, which is where a replacement
	// decision is actually made.
	asset := r.createAsset("Acceptance: asset with parts cost")
	if asset == "" {
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+asset+"/delete", url.Values{})
		}
	}()
	if resp, err := r.client.PostForm(r.base+"/documents/"+job+"/link",
		url.Values{"target": {asset}}); err == nil {
		resp.Body.Close()
	}
	tree := r.get("/plant")
	assetRow := between(tree, `data-asset="`+asset+`"`, "</tr>")
	r.check("parts reach the asset roll-up alongside the labour",
		strings.Contains(assetRow, "25.00"),
		"the machine has cost 25.00 in parts, and an asset costed on hours "+
			"alone is wrong by the price of every bearing that went into it")
}

// -----------------------------------------------------------------------------
// Email, calls, and the outside world
// -----------------------------------------------------------------------------

// emailAndCalls proves the two claims the phase is judged on against a running
// deployment: an email creates one work order and no duplicate on retry, and a
// reply threads under the original.
//
// It posts to the inbound endpoint the way the estate's MTA does, which is the
// only way to test the path the work actually arrives by.
func (r *runner) emailAndCalls() {
	if r.inboundKey == "" {
		// Not a failure. A deployment with no inbound key has the feature
		// switched off on purpose, and reporting that as broken would train
		// somebody to ignore a red line.
		r.check("inbound mail is configured", true,
			"skipped: no -inbound-key given, so the endpoint is off")
		return
	}

	// An address nothing else will use, so a second run does not collide.
	stamp := fmt.Sprint(time.Now().UnixNano() % 1000000)
	addr := "acceptance-" + stamp + "@works.example"
	resp, err := r.client.PostForm(r.base+"/settings/mail", url.Values{
		"address": {addr}, "kind": {"work_order"},
	})
	if err != nil {
		r.fail("an email creates one work order and no duplicate on retry", err.Error())
		return
	}
	resp.Body.Close()

	page := r.get("/settings/mail")
	r.check("an inbound address can be configured",
		strings.Contains(page, addr),
		"one address per space, so where a message lands is a property of the "+
			"address it was sent to")

	msgID := "acc-" + stamp + "@example.com"
	subject := "Acceptance: line 3 is making a noise " + stamp
	raw := "From: Dana Whitfield <dana-" + stamp + "@example.com>\r\n" +
		"To: " + addr + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Message-Id: <" + msgID + ">\r\n" +
		"\r\n" +
		"It started this morning, on the drive end.\r\n"

	first, code := r.deliver(raw)
	r.check("an email is accepted by the inbound endpoint",
		code == http.StatusOK, fmt.Sprintf("HTTP %d: %s", code, strings.TrimSpace(first)))
	m := docIDRe.FindStringSubmatch("/documents/" + strings.TrimSpace(
		strings.TrimPrefix(strings.TrimSpace(first), "created ")))
	if len(m) != 2 {
		r.fail("an email creates one work order and no duplicate on retry",
			"the endpoint did not report an id: "+strings.TrimSpace(first))
		return
	}
	docID := m[1]

	// The relay retries. Three times, exactly as one does when an
	// acknowledgement is lost, and every one has to land on the SAME work
	// order rather than a new one.
	//
	// Measured from what the endpoint says rather than by counting search
	// hits. A search counts anything else in this phase carrying the same
	// stamp, and a document deleted a moment ago is still in the index until
	// the cluster refreshes, which made this flake on a later run.
	retried, sameJob := 0, 0
	for i := 0; i < 3; i++ {
		body, code := r.deliver(raw)
		if code == http.StatusOK && strings.Contains(body, "already filed") {
			retried++
		}
		if strings.Contains(body, docID) {
			sameJob++
		}
	}
	r.check("an email creates one work order and no duplicate on retry",
		retried == 3 && sameJob == 3,
		fmt.Sprintf("%d of 3 retries answered as duplicates and %d landed on "+
			"the same work order", retried, sameJob))
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()
	doc := r.get("/documents/" + docID)
	r.check("the work order says who sent it and what they said",
		strings.Contains(doc, "dana-"+stamp+"@example.com") &&
			strings.Contains(doc, "drive end"),
		"a work order that does not say who asked for it is one nobody can "+
			"ask about")

	r.replyThreadsUnderTheOriginal(docID, addr, msgID, stamp)
	r.callsAreLogged(docID)
}

// deliver posts a raw message the way the estate's MTA does.
func (r *runner) deliver(raw string) (string, int) {
	req, err := http.NewRequest(http.MethodPost, r.base+"/inbound/mail",
		strings.NewReader(raw))
	if err != nil {
		return err.Error(), 0
	}
	req.Header.Set("X-Hearth-Inbound-Key", r.inboundKey)
	req.Header.Set("Content-Type", "message/rfc822")
	resp, err := r.client.Do(req)
	if err != nil {
		return err.Error(), 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode
}

// replyThreadsUnderTheOriginal is the second criterion.
func (r *runner) replyThreadsUnderTheOriginal(docID, addr, origID, stamp string) {
	// Our reply out.
	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/reply",
		url.Values{"body": {"On our way."}}); err == nil {
		resp.Body.Close()
	}
	page := r.get("/documents/" + docID)
	thread := between(page, "data-mail-thread", "</ul>")
	r.check("a reply is recorded on the work order",
		strings.Count(thread, "<li>") == 2,
		"the message that arrived and the answer that went back, both on the job")

	// Their answer comes back quoting nothing useful in In-Reply-To, which is
	// what a phone does, but with References intact.
	backID := "acc-back-" + stamp + "@example.com"
	back := "From: Dana Whitfield <dana-" + stamp + "@example.com>\r\n" +
		"To: " + addr + "\r\n" +
		"Subject: Re: Acceptance: line 3 is making a noise " + stamp + "\r\n" +
		"Message-Id: <" + backID + ">\r\n" +
		"In-Reply-To: <nonsense-" + stamp + "@elsewhere.example>\r\n" +
		"References: <acc-" + stamp + "@example.com>\r\n" +
		"\r\n" +
		"Thanks, we will leave it running until you get here.\r\n"

	body, code := r.deliver(back)
	r.check("a reply threads under the original rather than opening a second job",
		code == http.StatusOK && strings.Contains(body, docID),
		fmt.Sprintf("HTTP %d: %s", code, strings.TrimSpace(body)))

	after := r.get("/documents/" + docID)
	r.check("the answer lands on the same work order",
		strings.Count(between(after, "data-mail-thread", "</ul>"), "<li>") == 3,
		"three messages on one job, rather than two jobs with one message each")

	// And the whole conversation is on the timeline, so the next person on
	// shift can read it without opening a mail client.
	r.check("the conversation is on the timeline too",
		strings.Contains(after, "we will leave it running"),
		"a reply nobody else can see is a reply the next person sends again")
}

// callsAreLogged is the other half of how work arrives.
func (r *runner) callsAreLogged(docID string) {
	if resp, err := r.client.PostForm(r.base+"/calls", url.Values{
		"document": {docID}, "direction": {"in"}, "number": {"01827 555019"},
		"outcome": {"spoke"}, "minutes": {"4"},
		"note": {"Acceptance: they rang about the noise"},
		"back": {"/documents/" + docID},
	}); err != nil {
		r.fail("a call is logged against the work it was about", err.Error())
		return
	} else {
		resp.Body.Close()
	}

	page := r.get("/documents/" + docID)
	log := between(page, "data-calls", "</ul>")
	r.check("a call is logged against the work it was about",
		strings.Contains(log, "01827 555019") && strings.Contains(log, "spoke"),
		"half of everything a maintenance team is asked to do arrives by phone "+
			"and none of it is anywhere afterwards")

	// A call that never happened cannot be logged.
	bad, err := r.client.PostForm(r.base+"/calls", url.Values{
		"document": {docID}, "direction": {"sideways"}, "number": {"1"},
	})
	if err == nil {
		bad.Body.Close()
		r.check("a call that goes neither in nor out is refused",
			bad.StatusCode == http.StatusBadRequest,
			fmt.Sprintf("HTTP %d, want 400", bad.StatusCode))
	}
}

// inboundIsClosedWithoutTheKey is the check that matters most about the one
// endpoint a machine can reach.
func (r *runner) inboundIsClosedWithoutTheKey() {
	raw := "From: spam@example.com\r\nTo: anything@works.example\r\n" +
		"Subject: Free bearings\r\nMessage-Id: <spam@example.com>\r\n\r\nBuy now.\r\n"

	// No key at all.
	anon := &http.Client{Transport: asksForData{http.DefaultTransport}, Timeout: 30 * time.Second}
	resp, err := anon.Post(r.base+"/inbound/mail", "message/rfc822", strings.NewReader(raw))
	if err != nil {
		r.fail("the inbound endpoint refuses a message with no key", err.Error())
		return
	}
	resp.Body.Close()
	r.check("the inbound endpoint refuses a message with no key",
		resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound,
		fmt.Sprintf("HTTP %d: an inbound path that is open files spam as work",
			resp.StatusCode))

	// And a wrong one.
	req, _ := http.NewRequest(http.MethodPost, r.base+"/inbound/mail", strings.NewReader(raw))
	req.Header.Set("X-Hearth-Inbound-Key", "not-the-key")
	wrong, err := anon.Do(req)
	if err == nil {
		wrong.Body.Close()
		r.check("the inbound endpoint refuses a wrong key",
			wrong.StatusCode == http.StatusUnauthorized || wrong.StatusCode == http.StatusNotFound,
			fmt.Sprintf("HTTP %d", wrong.StatusCode))
	}
}

// -----------------------------------------------------------------------------
// Agents, the connector, and canvas
// -----------------------------------------------------------------------------

var (
	agentIDRe  = regexp.MustCompile(`data-agent="([0-9a-f-]{36})"`)
	canvasIDRe = regexp.MustCompile(`/canvas/([0-9a-f-]{36})`)
	nodeIDRe   = regexp.MustCompile(`data-node="([0-9a-f-]{36})"`)
	tokenRe    = regexp.MustCompile(`hearth_agent_[A-Za-z0-9_-]+`)
)

// agentsAndCanvas proves the two claims the phase is judged on against a
// running deployment, speaking the protocol over HTTP the way a client does.
func (r *runner) agentsAndCanvas() {
	stamp := fmt.Sprint(time.Now().UnixNano() % 1000000)

	// A channel for its output, because an agent shares what it makes the way
	// it is shared itself and one in no channel raises work only it can see.
	channelID := ""
	if resp, err := r.client.PostForm(r.base+"/channels", url.Values{
		"name": {"acceptance-agents-" + stamp}, "visibility": {"private"},
	}); err == nil {
		resp.Body.Close()
		if m := channelIDRe.FindStringSubmatch(resp.Request.URL.Path); len(m) == 2 {
			channelID = m[1]
		}
	}

	// An agent, and a token that can read and write.
	if resp, err := r.client.PostForm(r.base+"/agents", url.Values{
		"name": {"Acceptance agent " + stamp}, "handle": {"acc-" + stamp},
		"channel": {channelID},
	}); err != nil {
		r.fail("an agent cannot read a document it has no grant for", err.Error())
		return
	} else {
		resp.Body.Close()
	}

	page := r.get("/agents")
	agentID := ""
	for _, m := range agentIDRe.FindAllStringSubmatch(page, -1) {
		if strings.Contains(between(page, `data-agent="`+m[1]+`"`, "</tr>"), stamp) {
			agentID = m[1]
		}
	}
	if agentID == "" {
		r.fail("an agent cannot read a document it has no grant for",
			"the agent did not appear")
		return
	}
	r.check("an agent is a principal like anybody else",
		strings.Contains(page, "acc-"+stamp),
		"it has a handle, so it can be mentioned and it appears in history")
	row := between(page, `data-agent="`+agentID+`"`, "</tr>")
	joined, _ := attrInt(row, "data-channels")
	r.check("an agent in no channel is told that its output goes nowhere",
		joined == 1,
		fmt.Sprintf("in %d channel: an agent shares what it makes the way it "+
			"is shared itself, and the page says so rather than leaving "+
			"somebody to find out next Tuesday", joined))

	token := r.mintAgentToken(agentID, "acceptance", []string{"read", "write"})
	if token == "" {
		r.fail("an agent cannot read a document it has no grant for",
			"no token was issued")
		return
	}

	// A document the agent was never granted.
	secret := r.createFiltered("Acceptance: private to the owner "+stamp, "todo", "1")
	if secret == "" {
		r.fail("an agent cannot read a document it has no grant for",
			"could not create the document")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+secret+"/delete", url.Values{})
		}
	}()

	r.protocolHandshakeWorks(token)
	r.agentCannotReadWhatItWasNotGranted(token, secret, stamp)
	r.agentWritesAreAttributed(token, agentID, stamp)
	r.canvasCardsAreLive(secret)
}

func (r *runner) mintAgentToken(agentID, name string, scopes []string) string {
	form := url.Values{"agent": {agentID}, "name": {name}}
	for _, s := range scopes {
		form.Add("scopes", s)
	}
	resp, err := r.client.PostForm(r.base+"/agents/tokens", form)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// Shown once, on the page the mint redirects to.
	if m := tokenRe.FindString(string(body)); m != "" {
		return m
	}
	return tokenRe.FindString(r.get("/agents"))
}

// rpc speaks the protocol over HTTP, exactly as a client does.
func (r *runner) rpc(token, method string, params map[string]any) (map[string]any, int) {
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0
	}
	req, err := http.NewRequest(http.MethodPost, r.base+"/mcp", strings.NewReader(string(body)))
	if err != nil {
		return nil, 0
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	// What every streamable-HTTP connector client sends, and what the official SDK
	// Hearth serves the connector with requires.
	req.Header.Set("Accept", "application/json, text/event-stream")

	// A brand new client with no cookies, so nothing here can accidentally be
	// answered by the owner's session instead of the token.
	anon := &http.Client{Transport: asksForData{http.DefaultTransport}, Timeout: 30 * time.Second}
	resp, err := anon.Do(req)
	if err != nil {
		return nil, 0
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, resp.StatusCode
}

// toolText runs a tool and returns its text and whether it was an error.
func (r *runner) toolText(token, tool string, args map[string]any) (string, bool) {
	out, _ := r.rpc(token, "tools/call", map[string]any{
		"name": tool, "arguments": args,
	})
	result, _ := out["result"].(map[string]any)
	if result == nil {
		return "", true
	}
	isErr, _ := result["isError"].(bool)
	var b strings.Builder
	if content, ok := result["content"].([]any); ok {
		for _, part := range content {
			if m, ok := part.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
	}
	return b.String(), isErr
}

func (r *runner) protocolHandshakeWorks(token string) {
	out, code := r.rpc(token, "initialize", map[string]any{"protocolVersion": "2024-11-05"})
	result, _ := out["result"].(map[string]any)
	r.check("the protocol endpoint answers a handshake",
		code == http.StatusOK && result != nil && result["protocolVersion"] != nil,
		fmt.Sprintf("HTTP %d, and a version and capabilities came back", code))

	list, _ := r.rpc(token, "tools/list", nil)
	tools, _ := list["result"].(map[string]any)["tools"].([]any)
	r.check("the protocol endpoint offers its tools",
		len(tools) >= 3,
		fmt.Sprintf("%d tools: search, read, list, and the write ones this "+
			"token was given", len(tools)))

	// And no token at all reaches nothing.
	_, code = r.rpc("", "tools/list", nil)
	r.check("the protocol endpoint refuses a call with no token",
		code == http.StatusUnauthorized,
		fmt.Sprintf("HTTP %d, want 401", code))
	_, code = r.rpc("hearth_agent_not-a-real-token", "tools/list", nil)
	r.check("the protocol endpoint refuses a wrong token",
		code == http.StatusUnauthorized,
		fmt.Sprintf("HTTP %d, want 401", code))
}

// agentCannotReadWhatItWasNotGranted is the criterion.
func (r *runner) agentCannotReadWhatItWasNotGranted(token, secret, stamp string) {
	out, isErr := r.toolText(token, "read", map[string]any{"id": secret})
	r.check("an agent cannot read a document it has no grant for",
		isErr && !strings.Contains(out, "private to the owner"),
		"the same authz call a person's request hits said no, and the refusal "+
			"did not leak the contents")

	// And through search, which is the indirect route people forget.
	if !r.waitForQuiet(30 * time.Second) {
		r.fail("an agent cannot find it by searching either", "the queue never went quiet")
		return
	}
	found, _ := r.toolText(token, "search", map[string]any{"query": stamp})
	r.check("an agent cannot find it by searching either",
		!strings.Contains(found, secret),
		"the permission filter is inside the query rather than applied after it")

	// The owner can see it, so the check above is not passing because search
	// is broken.
	mine := r.hitsFor(stamp)
	r.check("the owner can still find it, so the refusal is a permission and not a fault",
		len(mine) >= 1, fmt.Sprintf("%d hits for the owner", len(mine)))
}

// agentWritesAreAttributed is the other criterion.
func (r *runner) agentWritesAreAttributed(token, agentID, stamp string) {
	out, isErr := r.toolText(token, "create", map[string]any{
		"kind": "work_order", "title": "Acceptance: raised by an agent " + stamp,
	})
	if isErr {
		r.fail("every agent write is attributed to that agent", out)
		return
	}
	created := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(out), "Created "), ".")
	if !docIDRe.MatchString("/documents/" + created) {
		r.fail("every agent write is attributed to that agent,",
			"the agent did not report an id: "+out)
		return
	}
	defer func() {
		if r.keep {
			return
		}
		// This one cannot be deleted, and that is the design rather than a
		// gap. An agent keeps admin over what it created and grants its
		// channels edit, so everybody else, the owner included, has edit and
		// deleting needs admin. The protocol has no delete tool either, on
		// purpose: an agent that can delete is one bad call away from
		// clearing a board.
		//
		// So the refusal is asserted rather than swallowed, and the work order
		// is cancelled instead, which edit is enough for. It leaves the run
		// with a closed row nobody's list shows rather than an open one, and
		// the residue is stated rather than silent. Found against the live
		// site, where a discarded error had been quietly leaking one work
		// order per run.
		resp, err := r.client.PostForm(r.base+"/documents/"+created+"/delete", url.Values{})
		if err != nil {
			r.fail("an agent keeps admin over what it created", err.Error())
			return
		}
		code := resp.StatusCode
		resp.Body.Close()
		r.check("an agent keeps admin over what it created",
			code == http.StatusForbidden || code == http.StatusNotFound,
			fmt.Sprintf("HTTP %d deleting it as somebody with edit: an agent's "+
				"work is not everybody's to remove", code))

		version := r.versionOf(created)
		if resp, err := r.client.PostForm(r.base+"/documents/"+created+"/status",
			url.Values{"status": {"cancelled"}, "version": {version}}); err == nil {
			resp.Body.Close()
		}
		r.check("what an agent leaves behind is closed rather than open",
			strings.Contains(r.get("/documents/"+created), `value="cancelled" selected`),
			"a run that cannot delete its own agent's work order has to at least "+
				"take it off somebody's list")
	}()

	// The owner reads the history and finds the agent, not a service account
	// and not the person who minted the token.
	page := r.get("/documents/" + created + "/history")
	r.check("every agent write is attributed to that agent",
		strings.Contains(page, short36(agentID)) ||
			strings.Contains(page, "Acceptance agent "+stamp),
		"the version history names the agent that made it")

	// And it can then read back what it created, because creating something
	// grants it.
	back, isErr := r.toolText(token, "read", map[string]any{"id": created})
	r.check("an agent can read back what it created",
		!isErr && strings.Contains(back, stamp),
		"creating something and being able to see it are the same operation")

	// Suspending the agent stops the token that is already in the script.
	if resp, err := r.client.PostForm(r.base+"/agents/"+agentID+"/status",
		url.Values{"status": {"suspended"}}); err == nil {
		resp.Body.Close()
	}
	_, code := r.rpc(token, "tools/list", nil)
	r.check("suspending an agent stops the token it already issued",
		code == http.StatusUnauthorized,
		fmt.Sprintf("HTTP %d: the stop button works now rather than on the next "+
			"restart", code))
	if resp, err := r.client.PostForm(r.base+"/agents/"+agentID+"/status",
		url.Values{"status": {"active"}}); err == nil {
		resp.Body.Close()
	}
}

// canvasCardsAreLive is the claim that makes a canvas worth having rather than
// a picture.
func (r *runner) canvasCardsAreLive(docID string) {
	resp, err := r.client.PostForm(r.base+"/canvas", url.Values{
		"name": {"Acceptance: line layout"},
	})
	if err != nil {
		r.fail("a canvas card is a live entity rather than a picture", err.Error())
		return
	}
	resp.Body.Close()
	m := canvasIDRe.FindStringSubmatch(resp.Request.URL.Path)
	if len(m) != 2 {
		r.fail("a canvas card is a live entity rather than a picture",
			"the canvas was not created")
		return
	}
	canvasID := m[1]
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/canvas/"+canvasID+"/delete", url.Values{})
		}
	}()

	if resp, err := r.client.PostForm(r.base+"/canvas/"+canvasID+"/nodes",
		url.Values{"entity": {docID}, "x": {"60"}, "y": {"80"}}); err == nil {
		resp.Body.Close()
	}

	page := r.get("/canvas/" + canvasID)
	r.check("a canvas card is a live entity rather than a picture",
		strings.Contains(page, "/documents/"+docID) && strings.Contains(page, "todo"),
		"the card links to the work order and shows the status it has right now")

	// Dropping the same thing on twice is one card, not two that drift apart.
	if resp, err := r.client.PostForm(r.base+"/canvas/"+canvasID+"/nodes",
		url.Values{"entity": {docID}, "x": {"200"}, "y": {"200"}}); err == nil {
		resp.Body.Close()
	}
	again := r.get("/canvas/" + canvasID)
	r.check("the same entity cannot be on one canvas twice",
		len(nodeIDRe.FindAllString(again, -1)) == 1,
		"two boxes for one machine drift apart and then the diagram has two "+
			"Conveyor 3s")

	// Moving it works with no scripting at all, which is the point of the
	// plain number fields.
	nodes := nodeIDRe.FindStringSubmatch(again)
	if len(nodes) == 2 {
		if resp, err := r.client.PostForm(
			r.base+"/canvas/"+canvasID+"/nodes/"+nodes[1],
			url.Values{"x": {"320"}, "y": {"140"}, "w": {"220"}, "h": {"120"}}); err == nil {
			resp.Body.Close()
		}
		moved := r.get("/canvas/" + canvasID)
		r.check("a card can be moved without scripting",
			strings.Contains(moved, "left:320px"),
			"the position fields post like any other form, because a plant "+
				"floor is where scripting is switched off by policy")
	}

	// And the status shown follows the entity rather than a copy of it.
	if resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/status",
		url.Values{"status": {"in_progress"}}); err == nil {
		resp.Body.Close()
	}
	after := r.get("/canvas/" + canvasID)
	r.check("a card follows the entity it points at",
		strings.Contains(after, "in_progress"),
		"the diagram reflects today rather than the day it was drawn")
}

// short36 is the first segment of a uuid, which is how ids are rendered.
func short36(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// -----------------------------------------------------------------------------
// The public API, webhooks, importers, and the export
// -----------------------------------------------------------------------------

var apiTokenRe = regexp.MustCompile(`hearth_api_[A-Za-z0-9_-]+`)

// apiAndExport proves the three claims the phase is judged on against a running
// deployment: the API refuses what the interface refuses, an importer is safe
// to run twice, and a full export re-imports.
func (r *runner) apiAndExport() {
	stamp := fmt.Sprint(time.Now().UnixNano() % 1000000)

	resp, err := r.client.PostForm(r.base+"/integrations/tokens", url.Values{
		"name": {"acceptance " + stamp}, "scopes": {"read", "write"},
	})
	if err != nil {
		r.fail("the API refuses what the interface refuses", err.Error())
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	token := apiTokenRe.FindString(string(body))
	if token == "" {
		token = apiTokenRe.FindString(r.get("/integrations"))
	}
	if token == "" {
		r.fail("the API refuses what the interface refuses", "no token was issued")
		return
	}
	r.check("an API token is shown once and acts as a person",
		strings.HasPrefix(token, "hearth_api_"),
		"there is nowhere to read it back from, which is the point")

	// Revoked at the end, always. These are working credentials that act as a
	// person, and a suite that mints two of them per run and walks away leaves
	// a pile of live keys on whatever it was pointed at. Found by counting what
	// was left on the real deployment after two runs: four.
	// A token this run must NOT touch, so the cleanup below is checked for what
	// it leaves alone rather than only for what it removes. Without a bystander
	// the check is vacuous on an instance that has no other tokens, which is
	// exactly the state that let an over-matching version of it pass.
	bystander := "acceptance bystander " + nonce()
	if resp, err := r.client.PostForm(r.base+"/integrations/tokens", url.Values{
		"name": {bystander}, "scopes": {"read"},
	}); err == nil {
		resp.Body.Close()
	}

	defer func() {
		if r.keep {
			return
		}
		page := r.get("/integrations")
		revoked := 0
		for _, m := range apiTokenIDRe.FindAllStringSubmatch(page, -1) {
			// This run's tokens only, matched inside the row the id anchors,
			// exactly the way the webhook cleanup below does it. An earlier
			// version of this searched a container the markup does not use, so
			// the match fell back to the whole page and it revoked every token
			// on the instance. On the local database that read as tidying up;
			// against a real deployment it would have revoked the owner's.
			if !strings.Contains(between(page, `data-api-token="`+m[1]+`"`, "</tr>"), stamp) {
				continue
			}
			if resp, err := r.client.PostForm(
				r.base+"/integrations/tokens/"+m[1]+"/revoke", url.Values{}); err == nil {
				resp.Body.Close()
				revoked++
			}
		}
		// Two, exactly: the owner's and the second account's.
		after := r.get("/integrations")
		r.check("the suite revokes the tokens it minted",
			revoked == 2 && !strings.Contains(after, stamp),
			fmt.Sprintf("revoked %d: a test that leaves working credentials "+
				"behind is a test nobody can run against production twice", revoked))
		// And nothing else. This is the half that matters: an earlier version
		// matched a container the markup does not use, fell back to the whole
		// page, and revoked every token on the instance.
		r.check("and it revokes nobody else's",
			strings.Contains(after, bystander),
			"a token this run did not create was revoked, which on a real "+
				"deployment is somebody's integration stopping at two in the morning")

		// The bystander goes too, by id, now that it has done its job.
		for _, m := range apiTokenIDRe.FindAllStringSubmatch(after, -1) {
			if strings.Contains(between(after, `data-api-token="`+m[1]+`"`, "</tr>"), bystander) {
				if resp, err := r.client.PostForm(
					r.base+"/integrations/tokens/"+m[1]+"/revoke", url.Values{}); err == nil {
					resp.Body.Close()
				}
			}
		}
	}()

	r.apiRefusesWhatTheInterfaceRefuses(token, stamp)
	r.importIsSafeToRunTwice(stamp)
	r.exportReimports(token, stamp)
	r.webhooksAreConfigurable(stamp)
}

// apiCall makes a request the way a client does, with no cookies at all.
func (r *runner) apiCall(token, method, path, body string) (int, string) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, r.base+"/api"+path, reader)
	if err != nil {
		return 0, err.Error()
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	// A brand new client, so nothing here is answered by the owner's session.
	anon := &http.Client{Transport: asksForData{http.DefaultTransport}, Timeout: 60 * time.Second}
	resp, err := anon.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func (r *runner) apiRefusesWhatTheInterfaceRefuses(token, stamp string) {
	// Something the owner can see and the second account cannot.
	private := r.createFiltered("Acceptance: API private "+stamp, "todo", "1")
	if private == "" {
		r.fail("the API refuses what the interface refuses", "could not create it")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+private+"/delete", url.Values{})
		}
	}()

	// The owner's token reads it, exactly as the owner's browser does.
	code, out := r.apiCall(token, http.MethodGet, "/v1/documents/"+private, "")
	r.check("the API answers for the caller it belongs to",
		code == http.StatusOK && strings.Contains(out, stamp),
		fmt.Sprintf("HTTP %d for the person the token acts as", code))

	// A token belonging to the second account does not, and neither does their
	// browser. The two have to agree.
	other, err := r.signInAs(secondEmail, secondPassword)
	if err != nil {
		r.fail("the API refuses what the interface refuses", err.Error())
		return
	}
	_, pageCode := other.get("/documents/" + private)

	theirID := r.secondPrincipalID()
	if theirID == "" {
		r.fail("the API refuses what the interface refuses", "no second account")
		return
	}
	resp, err := r.client.PostForm(r.base+"/integrations/tokens", url.Values{
		"name": {"acceptance second " + stamp}, "principal": {theirID},
		"scopes": {"read", "write"},
	})
	if err != nil {
		r.fail("the API refuses what the interface refuses", err.Error())
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	theirToken := apiTokenRe.FindString(string(body))
	if theirToken == "" {
		theirToken = apiTokenRe.FindString(r.get("/integrations"))
	}

	apiCode, _ := r.apiCall(theirToken, http.MethodGet, "/v1/documents/"+private, "")
	r.check("the API refuses what the interface refuses, for the same caller",
		pageCode == http.StatusNotFound && apiCode == http.StatusNotFound,
		fmt.Sprintf("the page said HTTP %d and the API said HTTP %d, and they "+
			"have to agree or the API is a way around the permission model",
			pageCode, apiCode))

	// And the write side, which is where a back door would actually be used.
	writeCode, _ := r.apiCall(theirToken, http.MethodPatch, "/v1/documents/"+private,
		`{"title":"Renamed by somebody else"}`)
	r.check("the API refuses a write the interface would refuse",
		writeCode != http.StatusOK,
		fmt.Sprintf("HTTP %d", writeCode))

	// No token at all reaches nothing.
	anonCode, _ := r.apiCall("", http.MethodGet, "/v1/documents", "")
	r.check("the API refuses a request with no token",
		anonCode == http.StatusUnauthorized,
		fmt.Sprintf("HTTP %d, want 401", anonCode))
}

func (r *runner) importIsSafeToRunTwice(stamp string) {
	csvBody := "id,title,state,priority\n" +
		"ACC-" + stamp + "-1,Acceptance import one " + stamp + ",open,high\n" +
		"ACC-" + stamp + "-2,Acceptance import two " + stamp + ",done,low\n"

	// Measured from the import's own report rather than by counting search
	// hits. A search counts whatever else this phase created with the same
	// stamp, and a document deleted a moment ago is still in the index until
	// the cluster refreshes, which made this flake on the third consecutive
	// run. The page already knows exactly what it did.
	run := func() (int, int, bool) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("source", "csv")
		_ = mw.WriteField("kind", "work_order")
		part, err := mw.CreateFormFile("file", "acceptance.csv")
		if err != nil {
			return 0, 0, false
		}
		_, _ = part.Write([]byte(csvBody))
		_ = mw.Close()

		resp, err := r.client.Post(r.base+"/import/clickup", mw.FormDataContentType(), &buf)
		if err != nil {
			return 0, 0, false
		}
		defer resp.Body.Close()
		page, _ := io.ReadAll(resp.Body)
		created, _ := attrInt(string(page), "data-created")
		updated, _ := attrInt(string(page), "data-updated")
		return created, updated, resp.StatusCode == http.StatusOK
	}

	created, _, ok := run()
	if !ok {
		r.fail("every importer is safe to run twice", "the first import failed")
		return
	}
	r.check("an import creates what the file contained",
		created == 2, fmt.Sprintf("%d created from a two row file", created))

	// The same file again, which is what happens the moment somebody realises
	// they picked the wrong kind.
	createdAgain, updatedAgain, ok := run()
	if !ok {
		r.fail("every importer is safe to run twice", "the second import failed")
		return
	}
	r.check("every importer is safe to run twice",
		createdAgain == 0 && updatedAgain == 2,
		fmt.Sprintf("the second run created %d and updated %d: an import is "+
			"never right the first time, and one that cannot be re-run means "+
			"starting again with a clean database", createdAgain, updatedAgain))

	if !r.keep {
		if !r.waitForQuiet(30 * time.Second) {
			return
		}
		for _, m := range docIDRe.FindAllStringSubmatch(
			r.get("/search?q="+url.QueryEscape(stamp)), -1) {
			_, _ = r.client.PostForm(r.base+"/documents/"+m[1]+"/delete", url.Values{})
		}
	}
}

// exportReimports is the criterion that decides whether the product can be
// left.
func (r *runner) exportReimports(token, stamp string) {
	code, body := r.apiCall(token, http.MethodGet, "/v1/export?format=csv&kind=work_order", "")
	r.check("a full export downloads",
		code == http.StatusOK && strings.Contains(body, "id,kind,title"),
		fmt.Sprintf("HTTP %d, %d bytes", code, len(body)))
	if code != http.StatusOK {
		return
	}

	// Straight back in through the generic importer, which is the same path
	// that runs every time somebody leaves another tool. Nothing new is
	// created, because everything in the file is already here under its own
	// id, which is exactly what a re-import into a populated instance should
	// do and the same property an empty one relies on.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("source", "csv")
	_ = mw.WriteField("kind", "work_order")
	part, err := mw.CreateFormFile("file", "hearth-export.csv")
	if err != nil {
		r.fail("a full export re-imports", err.Error())
		return
	}
	_, _ = part.Write([]byte(body))
	_ = mw.Close()

	before := r.countWorkOrders()
	resp, err := r.client.Post(r.base+"/import/clickup", mw.FormDataContentType(), &buf)
	if err != nil {
		r.fail("a full export re-imports", err.Error())
		return
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	after := r.countWorkOrders()

	r.check("a full export re-imports without duplicating anything",
		resp.StatusCode == http.StatusOK && after == before,
		fmt.Sprintf("%d work orders before and %d after: the export writes the "+
			"columns the importer reads, so the file goes back in", before, after))
	r.check("the re-import recognised every row it wrote",
		strings.Contains(string(page), "updated") || after == before,
		"an export its own product cannot re-import is a file rather than an exit")
}

func (r *runner) countWorkOrders() int {
	_, total, ok := listCount(r.get("/work-orders"))
	if !ok {
		return -1
	}
	return total
}

func (r *runner) webhooksAreConfigurable(stamp string) {
	target := "https://webhooks.invalid/acceptance-" + stamp
	if resp, err := r.client.PostForm(r.base+"/integrations/webhooks", url.Values{
		"url": {target}, "events": {"created"},
	}); err != nil {
		r.fail("a webhook can be configured and gets a signing secret", err.Error())
		return
	} else {
		resp.Body.Close()
	}

	page := r.get("/integrations")
	r.check("a webhook can be configured and gets a signing secret",
		strings.Contains(page, target) && strings.Contains(page, "whsec_"),
		"a URL is not a secret, so every body is signed with one that was "+
			"generated rather than chosen")

	// A URL with no scheme is refused rather than silently never delivering.
	bad, err := r.client.PostForm(r.base+"/integrations/webhooks",
		url.Values{"url": {"webhooks.invalid/nope"}})
	if err == nil {
		bad.Body.Close()
		r.check("a webhook URL with no scheme is refused",
			bad.StatusCode == http.StatusBadRequest,
			fmt.Sprintf("HTTP %d, want 400", bad.StatusCode))
	}

	// Clean up: this one points nowhere and would fill the dead letter table.
	if !r.keep {
		for _, m := range webhookIDRe.FindAllStringSubmatch(page, -1) {
			if strings.Contains(between(page, `data-webhook="`+m[1]+`"`, "</div>"), stamp) {
				_, _ = r.client.PostForm(
					r.base+"/integrations/webhooks/"+m[1]+"/delete", url.Values{})
			}
		}
	}
}

var webhookIDRe = regexp.MustCompile(`data-webhook="([0-9a-f-]{36})"`)

var apiTokenIDRe = regexp.MustCompile(`data-api-token="([0-9a-f-]{36})"`)

// -----------------------------------------------------------------------------
// The plant floor: an installable application, and work done with no signal
// -----------------------------------------------------------------------------

// syncOp is one queued action, in the shape the service worker sends.
//
// Declared here rather than imported from the server, deliberately. The suite
// is a client, and a client that shares a struct with the server cannot catch
// the server changing the wire format underneath it.
type syncOp struct {
	Key  string    `json:"key"`
	Path string    `json:"path"`
	Body string    `json:"body"`
	At   int64     `json:"at"`
	File *syncFile `json:"file,omitempty"`
}

type syncFile struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Digest string `json:"digest"`
	Data   []byte `json:"data"`
}

type syncResult struct {
	Key     string `json:"key"`
	Status  string `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Repeat  bool   `json:"repeat"`
}

// offlineAndMobile proves the three claims this phase is judged on: a status
// change made with no signal lands afterwards, a conflicting one asks instead
// of overwriting, and a photograph taken with no signal arrives as the
// photograph that was taken.
func (r *runner) offlineAndMobile() {
	stamp := nonce()

	r.theSiteIsInstallable()
	r.theNoSignalPageNeedsNoSession()

	docID := r.createFiltered("Acceptance: offline "+stamp, "todo", "1")
	if docID == "" {
		r.fail("a status change made with no signal lands afterwards",
			"could not create the work order")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()

	r.queuedWorkLandsWhenTheSignalReturns(docID)
	r.aConflictingQueuedEditAsksRatherThanOverwriting(stamp)
	r.aPhotographTakenWithNoSignalArrivesIntact(docID)
	r.theSameQueuedActionOnlyHappensOnce(docID)
	r.aPhotographCanBeTakenRatherThanFound(docID)
	r.aQueueCannotReachWhateverItLikes(stamp)
	r.aCodeCanBeTypedIn()
}

// sync posts a queue the way the service worker does.
func (r *runner) sync(ops ...syncOp) (int, map[string]syncResult) {
	body, err := json.Marshal(map[string]any{"ops": ops})
	if err != nil {
		return 0, nil
	}
	resp, err := r.client.Post(r.base+"/offline/sync", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	var answer struct {
		Results   []syncResult `json:"results"`
		Undecided int          `json:"undecided"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return resp.StatusCode, nil
	}
	out := make(map[string]syncResult, len(answer.Results))
	for _, res := range answer.Results {
		out[res.Key] = res
	}
	return resp.StatusCode, out
}

func (r *runner) versionOf(docID string) string {
	if m := versionRe.FindStringSubmatch(r.get("/documents/" + docID)); len(m) == 2 {
		return m[1]
	}
	return "0"
}

// theSiteIsInstallable checks the two files a phone needs before it can put
// this on a home screen and run it with the browser closed.
func (r *runner) theSiteIsInstallable() {
	resp, err := r.client.Get(r.base + "/manifest.webmanifest")
	if err != nil {
		r.fail("the site is installable", err.Error())
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var m struct {
		Name      string `json:"name"`
		StartURL  string `json:"start_url"`
		Display   string `json:"display"`
		Scope     string `json:"scope"`
		Icons     []struct{ Src, Purpose string }
		Shortcuts []struct{ Name, URL string }
	}
	if err := json.Unmarshal(body, &m); err != nil {
		r.fail("the site is installable", "the manifest is not JSON: "+err.Error())
		return
	}
	// standalone and a maskable icon are the two that decide whether it looks
	// like an application or like a bookmark, and a launcher that crops a
	// non-maskable icon takes the corners off it.
	maskable := false
	for _, icon := range m.Icons {
		if strings.Contains(icon.Purpose, "maskable") {
			maskable = true
		}
	}
	r.check("the site is installable",
		resp.StatusCode == http.StatusOK && m.Name != "" && m.Display == "standalone" &&
			m.Scope == "/" && m.StartURL != "" && len(m.Icons) > 0 && maskable,
		fmt.Sprintf("HTTP %d, display=%q scope=%q icons=%d maskable=%v",
			resp.StatusCode, m.Display, m.Scope, len(m.Icons), maskable))

	// The worker has to be served from the root, or its scope is the directory
	// it came from and it controls everything except every page.
	sw, err := r.client.Get(r.base + "/sw.js")
	if err != nil {
		r.fail("the service worker is served from the root", err.Error())
		return
	}
	code, _ := io.ReadAll(sw.Body)
	sw.Body.Close()
	ct := sw.Header.Get("Content-Type")
	r.check("the service worker is served from the root",
		sw.StatusCode == http.StatusOK &&
			(strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript")) &&
			bytes.Contains(code, []byte("hearth-outbox")),
		fmt.Sprintf("HTTP %d, %s, %d bytes", sw.StatusCode, ct, len(code)))

	// And it must not be cached like the revision-stamped assets are, because
	// the worker is how a new build reaches a phone at all. One that is cached
	// forever is a phone that never updates again.
	r.check("the service worker is not cached forever",
		!strings.Contains(strings.ToLower(sw.Header.Get("Cache-Control")), "immutable"),
		"Cache-Control: "+sw.Header.Get("Cache-Control"))

	// The build revision is stamped into it, so a deploy changes its bytes. A
	// browser only installs a worker whose bytes differ from the one it has.
	r.check("the worker carries the build it came from",
		!bytes.Contains(code, []byte("__BUILD__")) &&
			bytes.Contains(code, []byte("const BUILD =")),
		"the placeholder was not substituted, so every build ships an identical "+
			"worker and no phone ever picks up a new one")
}

// theNoSignalPageNeedsNoSession checks the page a browser lands on when it
// asked for something that is not cached and there is no connection.
//
// It has to render for a browser that cannot reach the server, which means it
// cannot be behind a redirect to a login page that also cannot be reached.
func (r *runner) theNoSignalPageNeedsNoSession() {
	anon := &http.Client{Transport: asksForData{http.DefaultTransport},
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := anon.Get(r.base + "/offline")
	if err != nil {
		r.fail("the no signal page needs no session", err.Error())
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r.check("the no signal page needs no session",
		resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte("No signal")),
		fmt.Sprintf("HTTP %d, %d bytes: a page that can only be reached by "+
			"signing in is no use to a phone that cannot reach anything",
			resp.StatusCode, len(body)))
}

// queuedWorkLandsWhenTheSignalReturns is the first criterion.
func (r *runner) queuedWorkLandsWhenTheSignalReturns(docID string) {
	version := r.versionOf(docID)
	// Half an hour ago, standing at the machine, which is the point.
	queued := time.Now().Add(-30 * time.Minute).UnixMilli()

	key := "acc-status-" + nonce()
	code, results := r.sync(syncOp{
		Key: key, At: queued, Path: "/documents/" + docID + "/status",
		Body: url.Values{"status": {"in_progress"}, "version": {version}}.Encode(),
	})
	res := results[key]
	r.check("a status change made with no signal lands afterwards",
		code == http.StatusOK && res.Status == "applied",
		fmt.Sprintf("HTTP %d, the queue said %q: %s", code, res.Status, res.Message))

	after := r.get("/documents/" + docID)
	r.check("the change is on the job, not just in the queue",
		strings.Contains(after, `value="in_progress" selected`),
		"a queue that reports success and changes nothing is the failure this "+
			"whole path exists to avoid")

	// And the outbox says how long the signal was out, because "did my morning
	// go in" is the question somebody asks after a shift in a basement.
	outbox := r.get("/outbox")
	r.check("the outbox records what came off a phone",
		strings.Contains(outbox, "a status change") &&
			strings.Contains(outbox, `data-op-status="applied"`),
		"the outbox does not show the action that just landed")
}

// aConflictingQueuedEditAsksRatherThanOverwriting is the second criterion, and
// the one that decides whether this feature can be trusted at all.
func (r *runner) aConflictingQueuedEditAsksRatherThanOverwriting(stamp string) {
	docID := r.createFiltered("Acceptance: offline conflict "+stamp, "todo", "1")
	if docID == "" {
		r.fail("a conflicting queued edit asks rather than overwriting",
			"could not create the work order")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()

	// What the phone was looking at when it went out of range.
	stale := r.versionOf(docID)

	// Meanwhile, somebody at a desk moves it on.
	resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/status",
		url.Values{"status": {"blocked"}, "version": {stale}})
	if err != nil {
		r.fail("a conflicting queued edit asks rather than overwriting", err.Error())
		return
	}
	resp.Body.Close()

	key := "acc-conflict-" + nonce()
	code, results := r.sync(syncOp{
		Key: key, At: time.Now().Add(-45 * time.Minute).UnixMilli(),
		Path: "/documents/" + docID + "/status",
		Body: url.Values{"status": {"done"}, "version": {stale}}.Encode(),
	})
	res := results[key]
	r.check("a conflicting queued edit asks rather than overwriting",
		code == http.StatusOK && res.Status == "conflict" && res.Code == http.StatusConflict,
		fmt.Sprintf("the queue said %q with HTTP %d: an edit made an hour ago "+
			"must not silently beat one made since", res.Status, res.Code))

	// The desk's change is still what the job says. This is the half that
	// matters: asking is only better than overwriting if it did not also
	// overwrite.
	page := r.get("/documents/" + docID)
	r.check("the conflicting edit did not overwrite anything",
		strings.Contains(page, `value="blocked" selected`) &&
			!strings.Contains(page, `value="done" selected`),
		"the queued edit won anyway, which is the one outcome that cannot be undone")

	// And it is a question on a page rather than a line in a log.
	outbox := r.get("/outbox")
	opID := ""
	if m := regexp.MustCompile(`/offline/([0-9a-f-]{36})/keep`).FindStringSubmatch(outbox); len(m) == 2 {
		opID = m[1]
	}
	r.check("the conflict waits for a person on the outbox",
		opID != "" && strings.Contains(outbox, "changed while you were out"),
		"a conflict nobody is shown is a conflict that was silently dropped")
	if opID == "" {
		return
	}

	// Somebody looks at both and says theirs is the true one.
	keep, err := r.client.PostForm(r.base+"/offline/"+opID+"/keep", url.Values{})
	if err != nil {
		r.fail("a person can settle the conflict either way", err.Error())
		return
	}
	keep.Body.Close()
	settled := r.get("/documents/" + docID)
	r.check("a person can settle the conflict either way",
		keep.StatusCode == http.StatusOK && strings.Contains(settled, `value="done" selected`),
		fmt.Sprintf("HTTP %d: resolving a conflict has to be possible, or the "+
			"only way out of one is to redo the work", keep.StatusCode))
	r.check("a settled conflict stops asking",
		!strings.Contains(r.get("/outbox"), "/offline/"+opID+"/keep"),
		"the question is still on the page after it was answered")
}

// aPhotographTakenWithNoSignalArrivesIntact is the third criterion.
func (r *runner) aPhotographTakenWithNoSignalArrivesIntact(docID string) {
	// Random and not small, so a truncated or cached response cannot pass.
	photo := make([]byte, 512<<10)
	if _, err := rand.Read(photo); err != nil {
		r.fail("a photograph taken with no signal arrives intact", err.Error())
		return
	}
	sum := sha256.Sum256(photo)
	digest := hex.EncodeToString(sum[:])

	key := "acc-photo-" + nonce()
	code, results := r.sync(syncOp{
		Key: key, At: time.Now().Add(-20 * time.Minute).UnixMilli(),
		Path: "/documents/" + docID + "/files",
		File: &syncFile{Name: "cracked-weld.jpg", Type: "image/jpeg",
			Digest: digest, Data: photo},
	})
	res := results[key]
	r.check("a photograph taken with no signal uploads",
		code == http.StatusOK && res.Status == "applied",
		fmt.Sprintf("HTTP %d, the queue said %q: %s", code, res.Status, res.Message))

	// And it is the photograph that was taken. Downloaded again and hashed,
	// because an upload reports success whether or not what landed is what
	// left.
	page := r.get("/documents/" + docID)
	fileRe := regexp.MustCompile(`/documents/` + docID + `/files/([0-9a-f-]{36})`)
	m := fileRe.FindStringSubmatch(page)
	if len(m) != 2 {
		r.fail("the photograph arrives with a matching digest",
			"no attachment appeared on the job")
		return
	}
	dl, err := r.client.Get(r.base + "/documents/" + docID + "/files/" + m[1])
	if err != nil {
		r.fail("the photograph arrives with a matching digest", err.Error())
		return
	}
	got, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	back := sha256.Sum256(got)
	r.check("the photograph arrives with a matching digest",
		hex.EncodeToString(back[:]) == digest,
		fmt.Sprintf("took %s, stored %s, %d bytes of %d",
			digest[:12], hex.EncodeToString(back[:12]), len(got), len(photo)))

	// The other half: a photograph that did NOT arrive intact is refused
	// rather than stored. A link that drops mid upload is exactly what
	// produces one, and an upload that returns success having stored half a
	// file is worse than one that failed.
	before := len(fileRe.FindAllString(r.get("/documents/"+docID), -1))
	bad := "acc-photo-bad-" + nonce()
	_, badResults := r.sync(syncOp{
		Key: bad, At: time.Now().UnixMilli(),
		Path: "/documents/" + docID + "/files",
		File: &syncFile{Name: "truncated.jpg", Type: "image/jpeg",
			Digest: digest, Data: photo[:len(photo)-64]},
	})
	after := len(fileRe.FindAllString(r.get("/documents/"+docID), -1))
	r.check("a photograph that did not arrive intact is refused",
		badResults[bad].Status == "rejected" && after == before,
		fmt.Sprintf("the queue said %q and the job went from %d attachments to %d",
			badResults[bad].Status, before, after))
}

// theSameQueuedActionOnlyHappensOnce is the property that makes any of this
// safe on a link that keeps dropping.
//
// Time, deliberately, because time is the one where doing it twice is silently
// wrong and nobody finds out until the invoice.
func (r *runner) theSameQueuedActionOnlyHappensOnce(docID string) {
	key := "acc-once-" + nonce()
	op := syncOp{
		Key: key, At: time.Now().Add(-2 * time.Hour).UnixMilli(),
		Path: "/documents/" + docID + "/time",
		Body: url.Values{"minutes": {"37"}, "note": {"acceptance, queued"}}.Encode(),
	}

	if _, first := r.sync(op); first[key].Status != "applied" {
		r.fail("the same queued action only ever happens once",
			"the first send did not land: "+first[key].Message)
		return
	}
	// The reply was lost, so the phone sends exactly the same thing again.
	_, second := r.sync(op)
	r.check("a repeated send reads the first answer back",
		second[key].Status == "applied" && second[key].Repeat,
		fmt.Sprintf("the second send said %q, repeat=%v",
			second[key].Status, second[key].Repeat))

	// Thirty-seven minutes, once. Counted off the job rather than off a search
	// index, because the question is what the record says.
	page := r.get("/documents/" + docID)
	logged := len(regexp.MustCompile(`acceptance, queued`).FindAllString(page, -1))
	r.check("the same queued action only ever happens once",
		logged == 1,
		fmt.Sprintf("%d entries for one queued action: a lost reply must not "+
			"log the same thirty-seven minutes twice", logged))
}

// aPhotographCanBeTakenRatherThanFound checks the control that opens the back
// camera instead of a file picker.
//
// One attribute, and without it somebody standing at a machine has to walk
// through a gallery to reach a camera they are already holding. It posts to
// the same address as any other attachment, which is what makes a photograph
// taken with no signal queue like everything else rather than needing a path
// of its own.
func (r *runner) aPhotographCanBeTakenRatherThanFound(docID string) {
	page := r.get("/documents/" + docID)
	form := between(page, "data-capture", "</form>")
	r.check("a photograph can be taken rather than found",
		strings.Contains(form, `capture="environment"`) &&
			strings.Contains(form, `accept="image/*"`),
		"the attachment control does not offer the camera, so a photograph at a "+
			"machine means going through a file picker")
	r.check("the camera posts to the same place as any other attachment",
		strings.Contains(page, `action="/documents/`+docID+`/files"`),
		"a camera on its own endpoint is a second upload path that does not queue")
}

// aQueueCannotReachWhateverItLikes is the security half.
//
// The batch endpoint replays into the site's own handlers, so the list of what
// it will replay is a boundary rather than a convenience. Without it, one
// request with no referer reaches every handler on the site.
func (r *runner) aQueueCannotReachWhateverItLikes(stamp string) {
	docID := r.createFiltered("Acceptance: offline refuses "+stamp, "todo", "1")
	if docID == "" {
		r.fail("the queue only replays what it is meant to", "could not create it")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()

	key := "acc-refuse-" + nonce()
	_, results := r.sync(syncOp{
		Key: key, At: time.Now().UnixMilli(),
		Path: "/documents/" + docID + "/delete",
	})
	_, code := (&client{base: r.base, http: r.client}).get("/documents/" + docID)
	r.check("the queue only replays what it is meant to",
		results[key].Status == "rejected" && code == http.StatusOK,
		fmt.Sprintf("the queue said %q and the document is HTTP %d: deleting is "+
			"not something to do blind, hours later, from a pocket",
			results[key].Status, code))
}

// aCodeCanBeTypedIn is the half of the scanner that works everywhere.
//
// The camera needs a browser with a barcode reader in it. The code is printed
// under the symbol precisely so it can be typed with cold hands, and that path
// goes through the server with no script running at all.
func (r *runner) aCodeCanBeTypedIn() {
	page := r.get("/scan")
	r.check("there is a scanner, and it can always be typed into",
		strings.Contains(page, "data-scan") && strings.Contains(page, `name="code"`),
		"the scan page does not offer a box")

	// A code that is not an asset lands on a refusal rather than a redirect
	// loop or a blank page.
	bare := &client{base: r.base, http: r.client}
	_, code := bare.get("/scan?code=" + url.QueryEscape("NOT-A-REAL-CODE-"+nonce()))
	r.check("typing a code goes somewhere honest",
		code == http.StatusNotFound,
		fmt.Sprintf("HTTP %d for a code that belongs to nothing", code))
}

// -----------------------------------------------------------------------------
// Tenancy, audit, and operations
// -----------------------------------------------------------------------------

var (
	teamIDRe  = regexp.MustCompile(`data-team="([0-9a-f-]{36})"`)
	scimReSre = regexp.MustCompile(`hearth_scim_[A-Za-z0-9_-]+`)
	scimIDRe  = regexp.MustCompile(`data-scim-token="([0-9a-f-]{36})"`)
)

// governance proves the three claims this phase is judged on, except the one
// that needs a second database: the restore drill is scripts/restore-drill.sh,
// which runs this whole suite against a copy restored from an archive.
func (r *runner) governance() {
	stamp := nonce()

	r.everyDeletionIsInTheLogWithItsActor(stamp)
	r.aTeamIsHowAccessIsGivenAndTakenAway(stamp)
	r.theDirectoryCanProvisionAndRevoke(stamp)
	r.theLogCannotBeRewritten()
	r.thereIsSomethingToWatch()
	r.thereIsARateLimit()
}

// everyDeletionIsInTheLogWithItsActor is one of the three criteria.
func (r *runner) everyDeletionIsInTheLogWithItsActor(stamp string) {
	docID := r.createFiltered("Acceptance: audited deletion "+stamp, "todo", "1")
	if docID == "" {
		r.fail("every deletion appears in the audit log with its actor",
			"could not create the work order")
		return
	}

	resp, err := r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
	if err != nil {
		r.fail("every deletion appears in the audit log with its actor", err.Error())
		return
	}
	resp.Body.Close()

	// Read straight back, with no wait. The row goes in inside the deletion's
	// own transaction, so if it is not there the moment the delete returns it
	// is not coming.
	//
	// Asked about the document rather than about the action, so this reads the
	// entry for this deletion rather than whichever deletion happens to be at
	// the top of a log that grows every run.
	page := r.get("/audit?entity=" + docID)
	row := between(page, `data-entry=`, "</tr>")
	me := r.myPrincipalID()

	r.check("every deletion appears in the audit log with its actor",
		row != "" && me != "" &&
			strings.Contains(row, `data-action="document.delete"`) &&
			strings.Contains(row, `data-actor="`+me+`"`),
		"a deletion with no actor on it is a deletion nobody can be asked about")
	r.check("the log says what the deleted thing was called",
		strings.Contains(row, "audited deletion "+stamp),
		"the title is the first question anybody asks about a deletion, and the "+
			"row it named is soft deleted rather than readable")
	r.check("the log records where it was done from",
		regexp.MustCompile(`\b(\d{1,3}\.){3}\d{1,3}\b|::1`).MatchString(row),
		"no address on the entry")

	// And a permission change, which is the other thing an auditor asks about
	// and the one that leaves no other trace: nothing about the document is
	// different afterwards.
	r.check("a permission change is in the log too",
		strings.Contains(r.get("/audit?action=grant.add"), "grant.add"),
		"granting somebody access left no record")
}

// theLogCannotBeRewritten is the property that makes the rest of it worth
// anything. Checked over HTTP by proving the page offers no way to, and in the
// database by internal/audit's own test watching the trigger refuse.
func (r *runner) theLogCannotBeRewritten() {
	page := r.get("/audit")
	r.check("the audit log is read only from the interface",
		!strings.Contains(page, `action="/audit/`) &&
			!strings.Contains(page, "/audit/delete"),
		"there is a form on the audit page that changes it")

	// Somebody who is not the owner cannot read it at all. It names what
	// everybody has done.
	other, err := r.signInAs(secondEmail, secondPassword)
	if err != nil {
		r.fail("the audit log is the owner's", err.Error())
		return
	}
	_, code := other.get("/audit")
	r.check("the audit log is the owner's",
		code == http.StatusForbidden || code == http.StatusNotFound,
		fmt.Sprintf("HTTP %d for somebody who is not the owner", code))
}

// aTeamIsHowAccessIsGivenAndTakenAway spends the team subject the grants have
// accepted since the substrate.
func (r *runner) aTeamIsHowAccessIsGivenAndTakenAway(stamp string) {
	r.ensureSecondAccount()
	theirID := r.secondPrincipalID()
	if theirID == "" {
		r.fail("a team grants what it was granted", "no second account")
		return
	}

	// Something the owner can see and the second account cannot.
	docID := r.createFiltered("Acceptance: team access "+stamp, "todo", "1")
	if docID == "" {
		r.fail("a team grants what it was granted", "could not create it")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()

	other, err := r.signInAs(secondEmail, secondPassword)
	if err != nil {
		r.fail("a team grants what it was granted", err.Error())
		return
	}
	if _, code := other.get("/documents/" + docID); code != http.StatusNotFound {
		r.fail("a team grants what it was granted",
			fmt.Sprintf("they could already see it, HTTP %d", code))
		return
	}

	if resp, err := r.client.PostForm(r.base+"/teams", url.Values{
		"name": {"Acceptance " + stamp}, "description": {"made by the suite"},
	}); err != nil {
		r.fail("a team grants what it was granted", err.Error())
		return
	} else {
		resp.Body.Close()
	}
	teams := r.get("/teams")
	teamID := ""
	for _, m := range teamIDRe.FindAllStringSubmatch(teams, -1) {
		if strings.Contains(between(teams, `data-team="`+m[1]+`"`, "</tr>"), stamp) {
			teamID = m[1]
		}
	}
	if teamID == "" {
		r.fail("a team grants what it was granted", "the team was not created")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/teams/"+teamID+"/delete", url.Values{})
		}
	}()

	// In the team, and still nothing: being in a team grants nothing until the
	// team is granted something.
	if resp, err := r.client.PostForm(r.base+"/teams/"+teamID+"/members",
		url.Values{"principal": {theirID}}); err == nil {
		resp.Body.Close()
	}
	_, code := other.get("/documents/" + docID)
	r.check("joining a team grants nothing on its own",
		code == http.StatusNotFound,
		fmt.Sprintf("HTTP %d: a team is a subject, not a role", code))

	// Now share it with the team.
	if resp, err := r.client.PostForm(r.base+"/teams/"+teamID+"/grant", url.Values{
		"document": {docID}, "level": {"edit"},
	}); err != nil {
		r.fail("a team grants what it was granted", err.Error())
		return
	} else {
		resp.Body.Close()
	}
	body, code := other.get("/documents/" + docID)
	r.check("a team grants what it was granted",
		code == http.StatusOK && strings.Contains(body, "team access "+stamp),
		fmt.Sprintf("HTTP %d for somebody in the team it was shared with", code))

	// And the half that matters on the day somebody moves department. No
	// signing out, no waiting: subjects are resolved on every request.
	if resp, err := r.client.PostForm(
		r.base+"/teams/"+teamID+"/members/"+theirID+"/delete", url.Values{}); err == nil {
		resp.Body.Close()
	}
	_, code = other.get("/documents/" + docID)
	r.check("leaving a team takes the access with it, on the next request",
		code == http.StatusNotFound,
		fmt.Sprintf("HTTP %d after being removed: a session lasts thirty days, "+
			"so revoked at the next sign-in is not revoked", code))

	// Every one of those is in the log.
	log := r.get("/audit?action=team.member.remove")
	r.check("team membership changes are audited",
		strings.Contains(log, "team.member.remove"),
		"taking somebody out of a team left no record of who did it")
}

// theDirectoryCanProvisionAndRevoke is the second criterion, driven the way
// Entra drives it.
func (r *runner) theDirectoryCanProvisionAndRevoke(stamp string) {
	resp, err := r.client.PostForm(r.base+"/integrations/provisioning",
		url.Values{"name": {"acceptance " + stamp}})
	if err != nil {
		r.fail("a directory can provision people", err.Error())
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	token := scimReSre.FindString(string(body))
	if token == "" {
		token = scimReSre.FindString(r.get("/integrations"))
	}
	r.check("a provisioning token is shown once",
		strings.HasPrefix(token, "hearth_scim_"),
		"there is nowhere to read it back from, which is the point")
	if token == "" {
		return
	}
	defer func() {
		if r.keep {
			return
		}
		page := r.get("/integrations")
		for _, m := range scimIDRe.FindAllStringSubmatch(page, -1) {
			if strings.Contains(between(page, `data-scim-token="`+m[1]+`"`, "</li>"), stamp) {
				_, _ = r.client.PostForm(
					r.base+"/integrations/provisioning/"+m[1]+"/revoke", url.Values{})
			}
		}
	}()

	address := "acceptance-" + stamp + "@hearth.invalid"
	code, created := r.scim(token, http.MethodPost, "/Users", `{
	  "schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
	  "externalId":"aad-`+stamp+`",
	  "userName":"`+address+`",
	  "displayName":"Acceptance Directory `+stamp+`",
	  "active":true}`)
	personID, _ := created["id"].(string)
	r.check("a directory can provision people",
		code == http.StatusCreated && personID != "",
		fmt.Sprintf("HTTP %d creating a user: %v", code, created["detail"]))
	if personID == "" {
		return
	}

	// The search a provisioning job makes before deciding to create or update.
	// A filter that matches nobody must return nobody rather than everybody,
	// which is how a job ends up reconciling a whole tenant.
	_, found := r.scim(token, http.MethodGet,
		"/Users?filter="+url.QueryEscape(`userName eq "`+address+`"`), "")
	_, none := r.scim(token, http.MethodGet,
		"/Users?filter="+url.QueryEscape(`userName eq "nobody-`+stamp+`@hearth.invalid"`), "")
	r.check("the directory can find one person without getting everybody",
		found["totalResults"] == float64(1) && none["totalResults"] == float64(0),
		fmt.Sprintf("a matching filter found %v and a matching-nothing filter found %v",
			found["totalResults"], none["totalResults"]))

	// A group, with them in it, granted something.
	code, group := r.scim(token, http.MethodPost, "/Groups", `{
	  "schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],
	  "externalId":"aad-group-`+stamp+`",
	  "displayName":"Acceptance Group `+stamp+`",
	  "members":[{"value":"`+personID+`"}]}`)
	groupID, _ := group["id"].(string)
	r.check("a directory group becomes a team",
		code == http.StatusCreated && groupID != "",
		fmt.Sprintf("HTTP %d: %v", code, group["detail"]))
	if groupID == "" {
		return
	}
	defer func() {
		if !r.keep {
			r.scim(token, http.MethodDelete, "/Groups/"+groupID, "")
		}
	}()

	// The group appears as a team, and says where it came from, so nobody
	// tries to edit it here and wonders why it changes back.
	page := r.get("/teams")
	r.check("a directory team says it comes from the directory",
		strings.Contains(page, "Acceptance Group "+stamp) &&
			strings.Contains(between(page, "Acceptance Group "+stamp, "</tr>"), "directory"),
		"a team the directory owns looks the same as one somebody made here")

	// Removing them from the group, in the shape Entra sends for a single
	// member change: a filtered path and no value at all.
	code, _ = r.scim(token, http.MethodPatch, "/Groups/"+groupID, `{
	  "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
	  "Operations":[{"op":"remove","path":"members[value eq \"`+personID+`\"]"}]}`)
	after := r.get("/teams/" + groupID)
	r.check("removing somebody from a directory group removes them here",
		code == http.StatusOK && !strings.Contains(after, `data-member="`+personID+`"`),
		fmt.Sprintf("HTTP %d, and they are still in the team", code))

	// Deactivating, which is the other half of somebody leaving.
	code, _ = r.scim(token, http.MethodPatch, "/Users/"+personID, `{
	  "Operations":[{"op":"replace","path":"active","value":"False"}]}`)
	// The whole row, not the text after the first mention of the id: that
	// mention is in the form at the end of the row and the status cell is
	// before it.
	their := personRow(r.get("/people"), personID)
	r.check("the directory can deactivate somebody",
		code == http.StatusOK && strings.Contains(their, "suspended"),
		fmt.Sprintf("HTTP %d, and the account is not suspended", code))

	// The provisioning token cannot read a work order. It acts as the
	// directory rather than as a person, and that boundary is the reason it
	// has its own table.
	req, err := http.NewRequest(http.MethodGet, r.base+"/api/v1/documents", nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+token)
		anon := &http.Client{Transport: asksForData{http.DefaultTransport}, Timeout: 30 * time.Second}
		if resp, err := anon.Do(req); err == nil {
			resp.Body.Close()
			r.check("a provisioning token cannot read anybody's work",
				resp.StatusCode == http.StatusUnauthorized,
				fmt.Sprintf("HTTP %d on the API with a directory token", resp.StatusCode))
		}
	}

	// And everything the directory did is in the log, attributed to nobody,
	// which is a fact rather than a gap.
	log := r.get("/audit?action=person.add")
	r.check("what the directory did is in the log",
		strings.Contains(log, "nobody, automatic"),
		"a change with no person behind it was attributed to somebody, or not "+
			"recorded at all")
}

// scim makes a request the way a provisioning job does, with no cookies.
func (r *runner) scim(token, method, path, body string) (int, map[string]any) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, r.base+"/scim/v2"+path, reader)
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/scim+json")

	anon := &http.Client{Transport: asksForData{http.DefaultTransport}, Timeout: 60 * time.Second}
	resp, err := anon.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

// thereIsSomethingToWatch checks the scrape a monitoring system reads.
func (r *runner) thereIsSomethingToWatch() {
	// No cookies. A metrics endpoint that only answers a signed-in browser is
	// one Prometheus cannot read.
	anon := &http.Client{Transport: asksForData{http.DefaultTransport}, Timeout: 30 * time.Second}
	resp, err := anon.Get(r.base + "/metrics")
	if err != nil {
		r.fail("there is a scrape a monitoring system can read", err.Error())
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	text := string(body)

	r.check("there is a scrape a monitoring system can read",
		resp.StatusCode == http.StatusOK &&
			strings.Contains(text, "# TYPE hearth_http_requests_total counter") &&
			strings.Contains(text, "hearth_jobs_ready"),
		fmt.Sprintf("HTTP %d, %d bytes", resp.StatusCode, len(body)))

	// The label set has to stay fixed. A counter labelled by path becomes a
	// hundred thousand series the first time somebody scans for URLs, and the
	// scrape then costs more than the traffic it is measuring.
	uuidInLabel := regexp.MustCompile(`\{[^}]*[0-9a-f]{8}-[0-9a-f]{4}-`)
	r.check("no metric carries an id in a label",
		!uuidInLabel.MatchString(text),
		"a series per work order is a scrape that costs more than the traffic")

	// And the operations page still answers, because a dashboard is not a
	// substitute for the page an operator opens when something is stuck.
	ops := r.get("/ops/jobs")
	r.check("the operations page is still there",
		strings.Contains(ops, "ready") || strings.Contains(ops, "Queue"),
		"the queue page did not render")
}

// thereIsARateLimit proves the budget exists by spending it.
//
// The sign-in form, because that is the one where one address making many
// attempts is the whole attack, and because failing to sign in leaves nothing
// behind to clean up.
func (r *runner) thereIsARateLimit() {
	// This is the one check that has to see the 429 itself.
	patience.off.Store(1)
	defer patience.off.Store(0)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: asksForData{http.DefaultTransport},
		Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	limited := false
	for i := 0; i < 40 && !limited; i++ {
		resp, err := client.PostForm(r.base+"/login", url.Values{
			"email":    {"nobody-" + nonce() + "@hearth.invalid"},
			"password": {"not the password"},
		})
		if err != nil {
			r.fail("one address cannot hammer the sign-in form", err.Error())
			return
		}
		retry := resp.Header.Get("Retry-After")
		code := resp.StatusCode
		resp.Body.Close()
		if code == http.StatusTooManyRequests {
			limited = true
			r.check("a refused request says when to come back",
				retry != "",
				"a 429 with no Retry-After makes a client retry immediately, "+
					"which makes it worse")
		}
	}
	r.check("one address cannot hammer the sign-in form", limited,
		"forty wrong passwords in a row from one address were all accepted for "+
			"processing, which is the whole of a credential stuffing run")

	// The budget is per endpoint, so spending the sign-in one does not lock
	// this suite out of the rest of the site.
	r.check("spending one budget does not lock out everything else",
		strings.Contains(r.get("/work-orders"), "Work orders"),
		"the work order list stopped answering after the sign-in limit was hit")
}

// personRow pulls one person's whole row off the people page.
func personRow(page, principalID string) string {
	for _, row := range strings.Split(page, "<tr>") {
		if strings.Contains(row, principalID) {
			return row
		}
	}
	return ""
}

// myPrincipalID is the caller's own id, off their account page.
//
// Not off the people page: the owner's row there carries no id, because the
// suspend button that would have held one is deliberately not drawn for them.
// The account page is where somebody goes to find out who they are, which is
// the right place for the id an audit entry names anyway.
func (r *runner) myPrincipalID() string {
	m := regexp.MustCompile(`data-principal="([0-9a-f-]{36})"`).
		FindStringSubmatch(r.get("/account"))
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// crmImport posts one CSV to the CRM importer and returns what the page says.
func (r *runner) crmImport(kind, mode, csvText string) string {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("kind", kind)
	_ = mw.WriteField("mode", mode)
	part, err := mw.CreateFormFile("file", kind+".csv")
	if err != nil {
		return ""
	}
	_, _ = part.Write([]byte(csvText))
	_ = mw.Close()
	resp, err := r.client.Post(r.base+"/crm/import", mw.FormDataContentType(), &buf)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// aCSVBringsInCompaniesContactsAndDeals is the generic import: a file from any
// other CRM, columns matched by name, checked before it is run, safe to run twice.
func (r *runner) aCSVBringsInCompaniesContactsAndDeals(stamp string) {
	alpha, beta := "Import Alpha "+stamp, "Import Beta "+stamp
	companies := "Segment,Company Name,Website\nx," + alpha + ",https://www.alpha-" + stamp + ".example/about\ny," + beta + ",\nz,,orphan-" + stamp + ".example\n"

	checked := r.crmImport("companies", "check", companies)
	r.check("checking a companies file creates nothing",
		strings.Contains(checked, `data-created="2"`) && strings.Contains(checked, `data-failed="1"`) &&
			strings.Contains(checked, "nothing was created") && !strings.Contains(r.get("/companies?q="+stamp), alpha),
		"2 would be created, 1 row named as failing, and the list has none of them")

	done := r.crmImport("companies", "import", companies)
	list := r.get("/companies?q=" + stamp)
	r.check("a companies file is imported and a bad row fails alone",
		strings.Contains(done, `data-created="2"`) && strings.Contains(done, "Line 4: needs a name") &&
			strings.Contains(list, alpha) && strings.Contains(list, beta),
		"two companies made, the line with no name reported, columns matched by header")

	again := r.crmImport("companies", "import", companies)
	r.check("running the same companies file again changes nothing",
		strings.Contains(again, `data-created="0"`) && strings.Contains(again, `data-skipped="2"`),
		"matched by website, then by name, and skipped")

	contacts := "First Name,Last Name,E-mail,Organization\nAda,Lovelace,ada." + stamp + "@import.example," + alpha + "\nBo,Bell,bo." + stamp + "@import.example,Brand New " + stamp + "\n"
	made := r.crmImport("contacts", "import", contacts)
	cl := r.get("/contacts?q=" + stamp)
	r.check("a contacts file is imported with their companies",
		strings.Contains(made, `data-created="2"`) && strings.Contains(made, "1 new companies made") &&
			strings.Contains(cl, "Ada Lovelace") && strings.Contains(cl, "Bo Bell") &&
			strings.Contains(r.get("/companies?q=Brand+New+"+stamp), "Brand New "+stamp),
		"first and last name joined, the known company reused, the new one made")
	r.check("running the same contacts file again changes nothing",
		strings.Contains(r.crmImport("contacts", "import", contacts), `data-skipped="2"`),
		"matched by email")

	deals := "Deal,Stage,Amount,Close Date,Account\nImport deal " + stamp + ",Proposal,\"$12,500\",2027-03-01," + alpha + "\nBad stage " + stamp + ",Haggling,5,,\n"
	dm := r.crmImport("deals", "import", deals)
	dl := r.get("/deals?q=" + stamp + "&view=table")
	r.check("a deals file is imported and an unknown stage fails alone",
		strings.Contains(dm, `data-created="1"`) && strings.Contains(dm, `data-failed="1"`) && strings.Contains(dm, "Proposal") &&
			strings.Contains(dl, "Import deal "+stamp) && !strings.Contains(dl, "Bad stage "+stamp),
		"stage by name, the amount with a dollar sign and comma, the company by name")
	r.check("running the same deals file again changes nothing",
		strings.Contains(r.crmImport("deals", "import", deals), `data-created="0"`),
		"same name at the same company is skipped")
}

var crmViewDeleteRe = regexp.MustCompile(`/crm/views/([0-9a-f-]{36})/delete`)

// aCRMListGroupsAndKeepsItsViews groups a list under headings with each heading's total across the list, and saves,
// reopens and removes a view of it.
func (r *runner) aCRMListGroupsAndKeepsItsViews(stamp string) {
	r.createCompany("Group Vendor A "+stamp, "", "vendor")
	r.createCompany("Group Vendor B "+stamp, "", "vendor")
	r.createCompany("Group Other "+stamp, "", "other")
	grouped := r.get("/companies?q=Group+&group=kind")
	heads := regexp.MustCompile(`data-group="([a-z]+)"`).FindAllStringSubmatch(grouped, -1)
	r.check("a CRM list is grouped under headings",
		strings.Contains(grouped, `data-group="vendor"`) && strings.Contains(grouped, `data-group="other"`) &&
			len(heads) >= 2 && strings.Index(grouped, `data-group="other"`) < strings.Index(grouped, `data-group="vendor"`),
		"one heading per kind, in order, above its rows")
	r.check("a heading carries its total across the whole list, not just the page",
		strings.Contains(grouped, "Group Vendor A "+stamp) && regexp.MustCompile(`vendor\s*<span[^>]*>\s*\d+`).MatchString(grouped),
		"the count beside the heading")

	name := "Grouped view " + stamp
	form := url.Values{"from": {"/companies?q=Group+&group=kind&page=2"}, "name": {name}, "shared": {"1"}}
	resp, err := r.client.PostForm(r.base+"/crm/views", form)
	body := ""
	if err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body = string(b)
	}
	r.check("a CRM list is saved as a view and opens on that list again",
		err == nil && strings.Contains(body, name) && strings.Contains(body, `data-group="vendor"`) && !strings.Contains(resp.Request.URL.RawQuery, "page="),
		"the grouping and the filter are kept, the page number is not")

	m := crmViewDeleteRe.FindStringSubmatch(body)
	if len(m) != 2 {
		r.fail("a saved CRM view can be removed", "the saved view has no remove form")
		return
	}
	del, err := r.client.PostForm(r.base+"/crm/views/"+m[1]+"/delete", url.Values{"from": {"/companies"}})
	after := ""
	if err == nil {
		b, _ := io.ReadAll(del.Body)
		del.Body.Close()
		after = string(b)
	}
	r.check("a saved CRM view can be removed", err == nil && !strings.Contains(after, name), "it is gone from the list's views")
}

// aCRMRecordCanBeStarred stars a company from its page, finds it on the favorites page, and removes the star.
func (r *runner) aCRMRecordCanBeStarred(stamp string) {
	id := r.createCompany("Starred Firm "+stamp, "", "vendor")
	if id == "" {
		r.fail("a company can be starred", "could not create the company")
		return
	}
	before := r.get("/companies/" + id)
	r.check("a company page offers to star it", strings.Contains(before, `data-favorite="off"`), "the Favorite button, unpressed")

	post := func(on string) string {
		resp, err := r.client.PostForm(r.base+"/crm/favorites", url.Values{"type": {"company"}, "id": {id}, "on": {on}, "back": {"/companies/" + id}})
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	starred := post("1")
	list := r.get("/crm/favorites")
	r.check("a starred company shows as one on its page and in the favorites list",
		strings.Contains(starred, `data-favorite="on"`) && strings.Contains(list, "Starred Firm "+stamp) && strings.Contains(list, "/companies/"+id),
		"pressed on the page, linked from the list")

	if other, err := r.signInAs(secondEmail, secondPassword); err == nil {
		body, _ := other.get("/crm/favorites")
		r.check("another person's favorites do not show it", !strings.Contains(body, "Starred Firm "+stamp), "a star is one person's own")
	}

	post("0")
	r.check("the star can be removed", !strings.Contains(r.get("/crm/favorites"), "Starred Firm "+stamp), "gone from the list")
}

// aRefusedTimeEntryStaysOnTheDocument is what a contractor sees when how long cannot be taken. They stay on the document, with the reason
// next to the field and what they typed kept, the form's own address is not a dead end, and the forms ask in hours and minutes so
// "is 2 hours or minutes?" cannot come up from them. The old single field still works for what was queued before and for the API.
func (r *runner) aRefusedTimeEntryStaysOnTheDocument() {
	docID := r.createFiltered("Acceptance: hours and minutes", "todo", "1")
	if docID == "" {
		r.fail("time is logged in hours and minutes", "could not create a work order")
		return
	}
	defer func() {
		if !r.keep {
			_, _ = r.client.PostForm(r.base+"/documents/"+docID+"/delete", url.Values{})
		}
	}()
	post := func(path string, v url.Values) (*http.Response, string) {
		resp, err := r.client.PostForm(r.base+path, v)
		if err != nil {
			return nil, ""
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	timePath := "/documents/" + docID + "/time"

	// the old single field: still read, still asks about "2", and the person stays on the document
	resp, body := post(timePath, url.Values{"minutes": {"2"}, "note": {"wired the panel"}, "on": {"2026-11-18"}})
	r.check("a refused time entry keeps the person on the document, with the reason beside the field",
		resp != nil && resp.StatusCode == http.StatusBadRequest && strings.Contains(body, "Is 2 hours or minutes?") &&
			strings.Contains(body, "data-time-form") && strings.Contains(body, `id="minutes-error"`) && strings.Contains(body, "Acceptance") &&
			strings.Contains(body, "wired the panel"),
		"HTTP 400, the document drawn again with the reason, the note kept")
	// the form's own address is not a page, and used to answer 404
	back, err := r.client.Get(r.base + timePath)
	if err == nil {
		back.Body.Close()
	}
	r.check("a form's address goes back to its page, not a 404",
		err == nil && back.StatusCode == http.StatusOK && back.Request.URL.Path == "/documents/"+docID,
		"GET on the form's address lands on the document")

	// hours and minutes: nothing to ask about, and the refusals say what to do
	resp, body = post(timePath, url.Values{"hours": {""}, "mins": {""}, "note": {"left blank"}})
	r.check("hours and minutes both blank is refused beside the fields, with the note kept",
		resp != nil && resp.StatusCode == http.StatusBadRequest && strings.Contains(body, "Enter how long it took") && strings.Contains(body, "left blank"),
		"a clear message on the document")
	resp, body = post(timePath, url.Values{"hours": {"0"}, "mins": {"0"}, "note": {"zero"}})
	r.check("0 hours 0 minutes is refused the same way",
		resp != nil && resp.StatusCode == http.StatusBadRequest && strings.Contains(body, "Enter how long it took"), "refused")
	resp, body = post(timePath, url.Values{"hours": {"1"}, "mins": {"75"}, "note": {"too many minutes"}})
	r.check("minutes over 59 are refused, and what was typed stays in the fields",
		resp != nil && resp.StatusCode == http.StatusBadRequest && strings.Contains(body, "Minutes go from 0 to 59") &&
			regexp.MustCompile(`name="hours"[^>]*value="1"`).MatchString(body) && regexp.MustCompile(`name="mins"[^>]*value="75"`).MatchString(body),
		"the message, hours 1 and minutes 75 still in the boxes")

	// 0h 45m, 2h 0m and 1h 30m are all taken, and add up
	for _, e := range []struct{ h, m, note string }{{"0", "45", "acc hm quarter"}, {"2", "0", "acc hm two"}, {"1", "30", "acc hm ninety"}} {
		resp, _ := post(timePath, url.Values{"hours": {e.h}, "mins": {e.m}, "note": {e.note}})
		if resp == nil || resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/documents/"+docID {
			r.fail("hours and minutes are taken as they stand: "+e.h+"h "+e.m+"m", "the entry was not accepted")
			return
		}
	}
	page := r.get("/documents/" + docID)
	r.check("0h 45m, 2h 0m and 1h 30m are taken and add up",
		strings.Contains(page, "acc hm quarter") && strings.Contains(page, "acc hm two") && strings.Contains(page, "acc hm ninety") &&
			strings.Contains(page, "4h 15m"),
		"three entries, 45 + 120 + 90 = 255 minutes, shown as 4h 15m")
	r.check("correcting an entry pre-fills both fields from the stored total",
		regexp.MustCompile(`name="hours"[^>]*value="1"`).MatchString(page) && regexp.MustCompile(`name="mins"[^>]*value="30"`).MatchString(page) &&
			regexp.MustCompile(`name="hours"[^>]*value="2"`).MatchString(page) && regexp.MustCompile(`name="mins"[^>]*value="45"`).MatchString(page),
		"an entry of 90 minutes reads 1 and 30, one of 45 reads 0 and 45")

	// the old single field still lands, as it does from an entry queued before this change, the API and hearthctl
	resp, _ = post(timePath, url.Values{"minutes": {"90"}, "note": {"acc old field"}})
	page = r.get("/documents/" + docID)
	r.check("the old single minutes field is still accepted",
		resp != nil && resp.StatusCode == http.StatusOK && strings.Contains(page, "acc old field") && strings.Contains(page, "5h 45m"),
		"90 minutes by the old field on top of 4h 15m reads 5h 45m")

	// the estimate form takes hours and minutes too, and blank clears it
	resp, _ = post("/documents/"+docID+"/estimate", url.Values{"estimate_hours": {"3"}, "estimate_mins": {"15"}})
	page = r.get("/documents/" + docID)
	r.check("an estimate is set in hours and minutes",
		resp != nil && resp.StatusCode == http.StatusOK && strings.Contains(page, "Estimated 3h 15m"), "3h 15m")
	post("/documents/"+docID+"/estimate", url.Values{"estimate_hours": {""}, "estimate_mins": {""}})
	r.check("blank hours and minutes clear the estimate", !strings.Contains(r.get("/documents/"+docID), "Estimated 3h 15m"), "cleared")
}
