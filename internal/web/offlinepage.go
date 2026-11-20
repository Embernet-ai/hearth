package web

// The offline path: what a phone with no signal sends when it gets one back.
//
// # Why a batch endpoint rather than the phone just retrying its requests
//
// Three reasons, and the first is the important one.
//
// Order. A status change and the note explaining it were made in that order at
// the machine, and replaying them as independent requests over a link that is
// coming and going puts the explanation before the change often enough to be
// confusing. One request, applied in order, cannot do that.
//
// Answers. A conflict has to reach a person as a question. Replaying a form
// post gets an HTML error page back, and telling a conflict from a refusal by
// reading a sentence out of rendered markup is the kind of thing that works
// until somebody rewords the sentence. The batch answers with data.
//
// Exactly once. A reply lost on a bad link makes a phone send again, and time
// logged twice is not caught by anybody until the invoice. The keys travel with
// the batch and the server settles them.
//
// # And why it replays through the real handlers
//
// Every operation is dispatched into the same mux a browser reaches, with the
// same session, so it hits the same validation, the same permissions, the same
// version check, and the same events. There is no second write path to keep in
// step with the first. That is the entire design, and it is why "a status
// change made offline lands after reconnecting" is a claim about the product
// rather than about a copy of it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/offline"
)

// syncRequest is what the service worker posts.
type syncRequest struct {
	Ops []syncOp `json:"ops"`
}

type syncOp struct {
	Key  string `json:"key"`
	Path string `json:"path"`
	Body string `json:"body"`
	// At is epoch milliseconds from the phone's own clock.
	At   int64     `json:"at"`
	File *syncFile `json:"file,omitempty"`
}

type syncFile struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Digest is the SHA-256 computed on the phone when the photograph was
	// taken, as lowercase hex.
	Digest string `json:"digest"`
	// Data decodes from base64 by the standard library's own convention for a
	// byte slice, so the wire format is one the browser can produce with no
	// helper of its own.
	Data []byte `json:"data"`
}

type syncResponse struct {
	Results []offline.Result `json:"results"`
	// Undecided is how many questions are now waiting for this person, so the
	// phone can put a number on the outbox without a second request.
	Undecided int `json:"undecided"`
}

// postOfflineSync replays a queue.
func (s *Server) postOfflineSync(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	var req syncRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, offline.MaxBody))
	if err := dec.Decode(&req); err != nil {
		s.syncError(w, http.StatusBadRequest, "that is not a queue: "+err.Error())
		return
	}
	if len(req.Ops) == 0 {
		s.writeJSON(w, http.StatusOK, syncResponse{Results: []offline.Result{}})
		return
	}
	if len(req.Ops) > offline.MaxOps {
		s.syncError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("%d operations in one batch, the limit is %d; send the rest in the next one",
				len(req.Ops), offline.MaxOps))
		return
	}

	results := make([]offline.Result, 0, len(req.Ops))
	// In the order the client sent them, NOT sorted by the timestamps they
	// carry. The phone's clock is the phone's, and a device whose time jumped
	// while it was in a pocket would otherwise reorder a morning's work. The
	// client stores its queue in the order the buttons were pressed, and that
	// order is the one that means something.
	for _, op := range req.Ops {
		results = append(results, s.replayOne(r, id, op))
	}

	n, err := s.offline.Count(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "count undecided", "error", err)
	}
	s.writeJSON(w, http.StatusOK, syncResponse{Results: results, Undecided: n})
}

func (s *Server) replayOne(r *http.Request, id *auth.Identity, op syncOp) offline.Result {
	o := offline.Op{
		Key: op.Key, Path: op.Path, Body: op.Body,
		At: time.UnixMilli(op.At),
	}
	if op.File != nil {
		o.File = &offline.File{
			Name: op.File.Name, ContentType: op.File.Type,
			Digest: op.File.Digest, Data: op.File.Data,
		}
	}

	recID, prior, err := s.offline.Claim(r.Context(), id.OrgID, id.PrincipalID, o)
	switch {
	case errors.Is(err, offline.ErrInvalid):
		// Refused before anything happened, and it will be refused the same way
		// tomorrow, so the client stops carrying it.
		return offline.Result{Key: op.Key, Status: offline.StatusRejected,
			Code: http.StatusBadRequest, Message: err.Error()}
	case err != nil:
		s.log.ErrorContext(r.Context(), "claim offline operation", "error", err)
		// Not settled. The client keeps it and tries again, which is the right
		// answer for a database that was briefly unavailable.
		return offline.Result{Key: op.Key, Status: offline.StatusPending,
			Code: http.StatusServiceUnavailable, Message: "could not take this in just now"}
	case prior != nil:
		// Seen before. Answer with what happened the first time rather than
		// doing it again.
		return offline.Result{Key: op.Key, Status: prior.Status, Code: prior.Code,
			Message: prior.Message, Repeat: true}
	}

	code, reason := s.dispatch(r, o)
	status := outcome(code)
	if err := s.offline.Finish(r.Context(), recID, status, code, reason); err != nil {
		s.log.ErrorContext(r.Context(), "record offline outcome", "error", err)
	}
	return offline.Result{Key: op.Key, Status: status, Code: code, Message: reason}
}

// outcome reads a status line the way the client has to act on it.
//
// Anything that worked is applied and the client forgets it. A conflict is a
// question and the client keeps it until a person answers. Everything else is
// a refusal that will be refused again, so carrying it forever helps nobody.
func outcome(code int) string {
	switch {
	case code >= 200 && code < 400:
		return offline.StatusApplied
	case code == http.StatusConflict:
		return offline.StatusConflict
	case code >= 500:
		// The server is having a bad time. Not the operation's fault, and not
		// settled, so the client keeps it.
		return offline.StatusUnknown
	default:
		return offline.StatusRejected
	}
}

// dispatch makes the request the browser would have made, into the same mux.
func (s *Server) dispatch(r *http.Request, op offline.Op) (int, string) {
	body, contentType, err := requestBody(op)
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}

	req := r.Clone(r.Context())
	req.Method = http.MethodPost
	req.URL = &url.URL{Path: op.Path}
	req.RequestURI = op.Path
	// The clone carries the sync request's parsed form. Left in place, every
	// handler below would read the batch's own fields instead of this
	// operation's, which is a bug that would look like the queue applying the
	// wrong thing to the wrong job.
	req.Form, req.PostForm, req.MultipartForm = nil, nil, nil
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = nil
	req.Header.Set("Content-Type", contentType)
	req.Header.Del("Content-Length")
	// HTMX headers on the batch would make every handler below answer with a
	// fragment meant to be swapped into a page that is not on screen.
	for name := range req.Header {
		if strings.HasPrefix(name, "Hx-") {
			req.Header.Del(name)
		}
	}
	// So a handler that cares can tell. Nothing does today, and the honest
	// reason to set it is that a request appearing in the log with no browser
	// behind it should say where it came from.
	req.Header.Set("X-Hearth-Replay", "1")

	rec := &recorder{code: http.StatusOK}
	s.authed.ServeHTTP(rec, req)
	return rec.code, rec.reason()
}

// requestBody builds what the handler will parse.
func requestBody(op offline.Op) ([]byte, string, error) {
	if op.File == nil {
		return []byte(op.Body), "application/x-www-form-urlencoded", nil
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	// The filename is quoted rather than escaped, so anything that would end
	// the quoted string early is taken out first. A header this builds itself
	// out of a name a phone chose is still a header built from input.
	h.Set("Content-Disposition",
		`form-data; name="file"; filename="`+safeFilename(op.File.Name)+`"`)
	h.Set("Content-Type", safeMediaType(op.File.ContentType))
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, "", fmt.Errorf("could not rebuild the upload: %w", err)
	}
	if _, err := part.Write(op.File.Data); err != nil {
		return nil, "", fmt.Errorf("could not rebuild the upload: %w", err)
	}
	// Whatever else came with the photograph travels too, so a caption typed
	// at the machine is not lost on the way through.
	if op.Body != "" {
		if values, err := url.ParseQuery(op.Body); err == nil {
			for name, vs := range values {
				if name == "file" {
					continue
				}
				for _, v := range vs {
					_ = mw.WriteField(name, v)
				}
			}
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", fmt.Errorf("could not rebuild the upload: %w", err)
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

// safeFilename keeps a name a person would recognise and nothing else.
func safeFilename(v string) string {
	v = strings.TrimSpace(v)
	// A path is not a filename. Both separators, because the phone may be
	// running either kind of system and this is not the place to find out.
	if i := strings.LastIndexAny(v, `/\`); i >= 0 {
		v = v[i+1:]
	}
	var b strings.Builder
	for _, r := range v {
		switch {
		case r < 0x20 || r == 0x7f, r == '"', r == '\\':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		// A photograph with no name is still a photograph.
		return "photo.jpg"
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// safeMediaType refuses anything that is not a plain type/subtype.
func safeMediaType(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	kind, sub, ok := strings.Cut(v, "/")
	if !ok || kind == "" || sub == "" {
		return "application/octet-stream"
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '/', r == '-', r == '+', r == '.':
		default:
			return "application/octet-stream"
		}
	}
	return v
}

// recorder is an http.ResponseWriter that keeps the answer instead of sending
// it. Written here rather than reached for from net/http/httptest, which is a
// testing package and does not belong in a running server.
type recorder struct {
	code    int
	header  http.Header
	body    bytes.Buffer
	written bool
}

func (rec *recorder) Header() http.Header {
	if rec.header == nil {
		rec.header = make(http.Header)
	}
	return rec.header
}

func (rec *recorder) WriteHeader(code int) {
	if rec.written {
		return
	}
	rec.code = code
	rec.written = true
}

func (rec *recorder) Write(p []byte) (int, error) {
	rec.written = true
	// Bounded. A handler that renders a whole page is answering a question this
	// caller only needs one line of, and buffering a hundred of them is a way
	// to turn a batch into a memory problem.
	if room := 4096 - rec.body.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		rec.body.Write(p[:room])
	}
	return len(p), nil
}

// reason is the sentence to show a person.
//
// The header first, because every failure path in this package sets it
// deliberately for exactly this caller. The rendered page is not read: pulling
// prose out of markup works until somebody changes the markup.
func (rec *recorder) reason() string {
	if rec.header != nil {
		if v := rec.header.Get("X-Hearth-Reason"); v != "" {
			return v
		}
	}
	switch {
	case rec.code == http.StatusConflict:
		return "somebody changed this while you were offline"
	case rec.code >= 400:
		return http.StatusText(rec.code)
	}
	return ""
}

// headerSafe makes a sentence fit in a header without changing what it says.
func headerSafe(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r > 0x7e:
			// Header values are bytes, and a curly quote in an error message is
			// not worth a mangled one. The message is in the page as well.
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if len(out) > 300 {
		out = out[:300]
	}
	return out
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error("write json", "error", err)
	}
}

func (s *Server) syncError(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, map[string]string{"error": msg})
}

// ---------------------------------------------------------------------------
// The outbox page
// ---------------------------------------------------------------------------

// getOutbox is where somebody answers what their phone could not.
//
// It is also the page the service worker serves when there is no signal at
// all, which is why it says what it says about being offline rather than
// assuming it is being read at a desk.
func (s *Server) getOutbox(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())

	undecided, err := s.offline.Undecided(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load outbox", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read the outbox")
		return
	}
	recent, err := s.offline.Recent(r.Context(), id.OrgID, id.PrincipalID, 25)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load recent", "error", err)
	}

	s.render(w, r, "outbox", page{
		Title: "Outbox", Heading: "Outbox",
		Outbox: undecided, OutboxRecent: recent,
		OutboxTitles: s.titlesFor(r, append(append([]*offline.Record{}, undecided...), recent...)),
	})
}

// titlesFor names the things these operations were about.
//
// Through the document service, one at a time, so a row somebody has since
// lost their grant to shows an id rather than a title. A list of what a person
// did is not a way to read the titles of documents they can no longer see.
func (s *Server) titlesFor(r *http.Request, recs []*offline.Record) map[string]string {
	id := auth.From(r.Context())
	out := make(map[string]string, len(recs))
	for _, rec := range recs {
		if rec.EntityID == nil || out[*rec.EntityID] != "" {
			continue
		}
		d, err := s.docs.Get(r.Context(), id.OrgID, id.Subjects, *rec.EntityID)
		if err != nil {
			continue
		}
		out[*rec.EntityID] = d.Title
	}
	return out
}

// postOfflineKeep applies a queued edit over the top of whatever changed.
//
// The version is stripped, which is the only way to resolve a conflict, and it
// is a decision rather than a default: somebody has looked at both and said
// theirs is the true one. That is the difference between this and the silent
// overwrite the version check exists to prevent.
func (s *Server) postOutboxKeep(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	rec, err := s.offline.Get(r.Context(), id.PrincipalID, r.PathValue("id"))
	if errors.Is(err, offline.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such queued action")
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "load offline op", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read it")
		return
	}
	if rec.Status != offline.StatusConflict && rec.Status != offline.StatusUnknown {
		s.fail(w, r, http.StatusBadRequest, "that one has already been settled")
		return
	}
	if rec.Body == "" {
		// A photograph. The bytes were never kept here, on purpose, so this is
		// not the place it can be sent from. The phone still has it.
		s.fail(w, r, http.StatusBadRequest,
			"the photograph is still on the phone that took it, and has to be sent from there")
		return
	}

	// Without the version, so the service takes the write.
	values, err := url.ParseQuery(rec.Body)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "that action cannot be read back")
		return
	}
	values.Del("version")

	code, reason := s.dispatch(r, offline.Op{Path: rec.Path, Body: values.Encode()})
	if code >= 400 {
		if err := s.offline.Finish(r.Context(), rec.ID, offline.StatusConflict, code, reason); err != nil {
			s.log.ErrorContext(r.Context(), "record decision", "error", err)
		}
		s.fail(w, r, code, "that still did not go through: "+reason)
		return
	}
	if err := s.offline.Decide(r.Context(), id.PrincipalID, rec.ID,
		offline.StatusResolved, "kept, over the version that was there"); err != nil {
		s.log.ErrorContext(r.Context(), "record decision", "error", err)
	}
	http.Redirect(w, r, "/outbox", http.StatusSeeOther)
}

// postOfflineDiscard throws a queued action away.
func (s *Server) postOutboxDiscard(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	err := s.offline.Decide(r.Context(), id.PrincipalID, r.PathValue("id"),
		offline.StatusDiscarded, "thrown away rather than applied")
	switch {
	case errors.Is(err, offline.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such queued action")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "discard offline op", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not discard it")
		return
	}
	http.Redirect(w, r, "/outbox", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// The installable part
// ---------------------------------------------------------------------------

// serviceWorker serves the worker from the root.
//
// From the root because a service worker's scope is the directory it was served
// from, and one served out of /static would control /static and nothing else,
// which is the whole site except every page. It is deliberately not the
// immutable, revision-stamped asset the rest of the front end is: the worker is
// how a new build reaches a phone, so a cached one is a phone that never
// updates again.
func (s *Server) serviceWorker(w http.ResponseWriter, r *http.Request) {
	body, err := staticFS.ReadFile("static/sw.js")
	if err != nil {
		http.Error(w, "no service worker", http.StatusInternalServerError)
		return
	}
	// The build revision is substituted in, so a deploy changes the worker's
	// bytes. A browser only installs a worker whose bytes differ from the one
	// it has, and without this a release would ship a new shell to a phone that
	// keeps serving the old one out of its cache.
	out := bytes.ReplaceAll(body, []byte("__BUILD__"), []byte(assetVersion()))

	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(out)
}

// manifest makes the site installable.
//
// Written here rather than kept as a file because two of its fields are the
// deployment's own: the name people will see under the icon on a phone, and
// where it starts. An installed application that opens somebody else's Hearth
// is a very confusing bug.
func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose,omitempty"`
	}
	type shortcut struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	m := map[string]any{
		"id":         "/",
		"name":       "Hearth",
		"short_name": "Hearth",
		"description": "Work orders, assets, and preventive maintenance, " +
			"including at the machine with no signal.",
		"start_url": "/work-orders",
		// standalone, because the point of installing it is a full screen on a
		// phone held in one hand. The browser chrome is a row of pixels that
		// nobody at a machine has ever needed.
		"display":          "standalone",
		"orientation":      "any",
		"background_color": "#0f1113",
		"theme_color":      "#0f1113",
		"scope":            "/",
		"icons": []icon{
			{Src: "/icon.svg", Sizes: "any", Type: "image/svg+xml", Purpose: "any"},
			{Src: "/icon-maskable.svg", Sizes: "any", Type: "image/svg+xml", Purpose: "maskable"},
			{Src: "/icon-192.png", Sizes: "192x192", Type: "image/png", Purpose: "any"},
			{Src: "/icon-512.png", Sizes: "512x512", Type: "image/png", Purpose: "any"},
			{Src: "/icon-maskable-512.png", Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
		},
		// The three things somebody opens the application to do, on the long
		// press. Scanning is first because it is the one that replaces typing a
		// code in with cold hands.
		"shortcuts": []shortcut{
			{Name: "Scan a code", URL: "/scan"},
			{Name: "Work orders", URL: "/work-orders"},
			{Name: "Outbox", URL: "/outbox"},
		},
	}
	w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(m); err != nil {
		s.log.Error("write manifest", "error", err)
	}
}

// getScan is the code reader.
//
// A page rather than a mode inside another page, because it is reached from
// the shortcut on a home screen with one thumb and has to be the whole screen
// when it opens.
func (s *Server) getScan(w http.ResponseWriter, r *http.Request) {
	// The typed box submits here when there is no script running, so the
	// keyboard half of this page works with the camera half turned off, on a
	// browser that has neither a barcode reader nor JavaScript enabled.
	if code := strings.TrimSpace(r.URL.Query().Get("code")); code != "" {
		http.Redirect(w, r, "/a/"+url.PathEscape(code), http.StatusSeeOther)
		return
	}
	s.render(w, r, "scan", page{Title: "Scan", Heading: "Scan a code"})
}

// getOfflineShell is the page a browser lands on when it asked for something
// that is not cached and there is no signal.
//
// Served without a session on purpose. It reaches nothing and says nothing
// about this installation; its whole job is to be a page rather than the
// browser's own dinosaur, and to carry the link to the outbox.
func (s *Server) getOfflineShell(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, offlineShell)
}

// offlineShell is deliberately one self-contained file with no asset it has to
// fetch. A page that needs a stylesheet to render is a page that renders as
// nothing on the one occasion it is ever shown.
const offlineShell = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>No signal · Hearth</title>
<style>
:root{color-scheme:dark light}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
     background:#0f1113;color:#e7e9ea;
     font:16px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:28rem;padding:2rem}
h1{font-size:1.4rem;margin:0 0 .75rem}
p{color:#9aa3ab}
a{color:#fb923c;display:inline-block;margin-top:1rem;padding:.85rem 1.2rem;
  border:1px solid #272c31;border-radius:8px;text-decoration:none;min-height:48px}
</style></head>
<body><main>
<h1>No signal</h1>
<p>This page was not on the phone, so there is nothing to show yet. Anything you
did while you were out of range is still here and goes in as soon as there is a
connection.</p>
<a href="/outbox">See what is waiting</a>
</main></body></html>`
