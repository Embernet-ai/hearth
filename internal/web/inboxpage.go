package web

// The inbox, and the controls that decide what lands in it.
//
// A notification system is judged by whether people turn it off, so the
// settings sit next to the list rather than three pages away.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/notify"
)

func (s *Server) getInbox(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	unreadOnly := r.URL.Query().Get("all") == ""

	list, err := s.inbox.Inbox(r.Context(), id.PrincipalID, unreadOnly, 100)
	if err != nil {
		s.log.ErrorContext(r.Context(), "inbox", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load your inbox")
		return
	}
	prefs, err := s.inbox.Prefs(r.Context(), id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "prefs", "error", err)
	}
	s.render(w, r, "inbox", page{
		Title: "Inbox", Notifications: list, Prefs: prefs, UnreadOnly: unreadOnly,
	})
}

func (s *Server) markNotificationsRead(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	// An empty id means all of them, which is the button people actually press.
	if err := s.inbox.MarkRead(r.Context(), id.PrincipalID, r.FormValue("id")); err != nil {
		s.log.ErrorContext(r.Context(), "mark read", "error", err)
	}
	back := r.Referer()
	if back == "" {
		back = "/inbox"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// toggleWatch is the one obvious way to stop.
func (s *Server) toggleWatch(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	ref := core.Ref{Type: core.EntityDocument, ID: docID}

	// The read check is the document's own: following something you cannot see
	// would be a way to be told when it changes.
	if _, err := s.docs.Get(r.Context(), id.OrgID, id.Subjects, docID); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such document")
		return
	}

	watching, err := s.inbox.IsWatching(r.Context(), id.PrincipalID, ref)
	if err != nil {
		s.log.ErrorContext(r.Context(), "is watching", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not check that")
		return
	}
	if watching {
		err = s.inbox.Unwatch(r.Context(), id.PrincipalID, ref)
	} else {
		err = s.inbox.Watch(r.Context(), id.PrincipalID, ref, notify.WatchManual)
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "toggle watch", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not change that")
		return
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

func (s *Server) saveNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	p := notify.Prefs{
		Email:    r.FormValue("email"),
		Timezone: strings.TrimSpace(r.FormValue("timezone")),
		DigestAt: minutesOf(r.FormValue("digest_at"), 8*60),
	}
	// Both ends or neither. Half a quiet window is a window that never closes.
	from, fromOK := parseMinutes(r.FormValue("quiet_from"))
	to, toOK := parseMinutes(r.FormValue("quiet_to"))
	if fromOK && toOK {
		p.QuietFrom, p.QuietTo = &from, &to
	}

	if err := s.inbox.SetPrefs(r.Context(), id.PrincipalID, p); err != nil {
		list, _ := s.inbox.Inbox(r.Context(), id.PrincipalID, false, 100)
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "inbox", page{
			Title: "Inbox", Notifications: list, Prefs: p,
			Message: strings.TrimPrefix(err.Error(), "notify: "),
		})
		return
	}
	http.Redirect(w, r, "/inbox", http.StatusSeeOther)
}

// parseMinutes reads an HH:MM time control into minutes past midnight.
func parseMinutes(v string) (int, bool) {
	hh, mm, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok {
		return 0, false
	}
	h, err := strconv.Atoi(hh)
	if err != nil || h < 0 || h > 23 {
		return 0, false
	}
	m, err := strconv.Atoi(mm)
	if err != nil || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func minutesOf(v string, fallback int) int {
	if n, ok := parseMinutes(v); ok {
		return n
	}
	return fallback
}
