package web

// The mailbox: a person's Gmail or Outlook mailbox in Hearth, read and write, as them.
//
// Everything passes straight through to Google or Microsoft and nothing is kept here, so what a person sees and does
// is exactly what their own mail client would show and do: open any message with its whole body, send, reply, reply
// to all, forward, archive, label, mark read and delete. The handlers act on the signed-in person's own connection,
// taken from the session, never from the request.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/connect"
)

// MailboxView is the mailbox page.
type MailboxView struct {
	Providers []CloudProvider
	Provider  string
	Label     string
	Folder    string
	Search    string
	Messages  []connect.Message
	Next      string
	Open      *connect.Message
	Labels    []string
	Notice    string
}

func (s *Server) mailboxProviders(ctx context.Context, org, principal string) []CloudProvider {
	var out []CloudProvider
	for _, p := range connect.Providers {
		b := s.connect.Backend(p)
		if b == nil {
			continue
		}
		if on, _ := s.connect.Enabled(ctx, org, p, connect.Mail); !on {
			continue
		}
		granted, err := b.Granted(ctx, principal)
		if err != nil || !connect.HasScopes(granted, b.Scopes(connect.Mail)) {
			continue
		}
		out = append(out, CloudProvider{Key: string(p), Label: p.Label()})
	}
	return out
}

func mailErr(err error) string {
	switch {
	case errors.Is(err, connect.ErrReconnect):
		return "That connection was revoked. Connect again under Settings, Connectors."
	case errors.Is(err, connect.ErrScope):
		return "Your connection does not include mail. Connect again and tick Mail."
	case errors.Is(err, connect.ErrNotFound):
		return "That message is not there any more."
	case errors.Is(err, connect.ErrDisabled):
		return "An administrator has not turned this on."
	}
	return strings.TrimPrefix(err.Error(), "connect: ")
}

func (s *Server) getMailbox(w http.ResponseWriter, r *http.Request) {
	if !s.connectorsOn() {
		s.fail(w, r, http.StatusNotFound, "the connectors are not set up on this installation")
		return
	}
	ctx := r.Context()
	id := auth.From(ctx)
	q := r.URL.Query()
	v := &MailboxView{Notice: q.Get("notice"), Folder: q.Get("folder"), Search: q.Get("q")}
	if v.Folder == "" {
		v.Folder = "inbox"
	}
	v.Providers = s.mailboxProviders(ctx, id.OrgID, id.PrincipalID)
	if len(v.Providers) > 0 {
		v.Provider = q.Get("provider")
		ok := false
		for _, cp := range v.Providers {
			ok = ok || cp.Key == v.Provider
		}
		if !ok {
			v.Provider = v.Providers[0].Key
		}
		p := connect.Provider(v.Provider)
		v.Label = p.Label()
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if mid := q.Get("id"); mid != "" {
			m, err := s.connect.Message(cctx, id.OrgID, id.PrincipalID, p, mid)
			if err != nil {
				v.Notice = mailErr(err)
			} else {
				v.Open = &m
			}
		} else {
			msgs, next, err := s.connect.Inbox(cctx, id.OrgID, id.PrincipalID, p, connect.MailQuery{Folder: v.Folder, Search: v.Search, Cursor: q.Get("cursor")})
			if err != nil {
				v.Notice = mailErr(err)
			}
			v.Messages, v.Next = msgs, next
		}
		v.Labels, _ = s.connect.MailLabels(cctx, id.OrgID, id.PrincipalID, p)
	}
	s.render(w, r, "mailbox", page{Title: "Mailbox", Mailbox: v})
}

// mailForm reads the provider and the person's connection for an action, and answers where to go after it.
func (s *Server) mailForm(w http.ResponseWriter, r *http.Request) (connect.Provider, bool) {
	if !s.connectorsOn() {
		s.fail(w, r, http.StatusNotFound, "the connectors are not set up on this installation")
		return "", false
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return "", false
	}
	p, err := connect.ParseProvider(r.FormValue("provider"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "choose a provider")
		return "", false
	}
	return p, true
}

func (s *Server) mailBack(w http.ResponseWriter, r *http.Request, p connect.Provider, msg string, err error) {
	q := url.Values{"provider": {string(p)}}
	if f := r.FormValue("folder"); f != "" {
		q.Set("folder", f)
	}
	if err != nil {
		s.log.WarnContext(r.Context(), "mailbox", "error", err)
		q.Set("notice", mailErr(err))
	} else if msg != "" {
		q.Set("notice", msg)
	}
	http.Redirect(w, r, "/mailbox?"+q.Encode(), http.StatusSeeOther)
}

func addrs(v string) []string {
	var out []string
	for _, a := range strings.FieldsFunc(v, func(c rune) bool { return c == ',' || c == ';' || c == '\n' }) {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func outgoingFrom(r *http.Request) connect.Outgoing {
	return connect.Outgoing{To: addrs(r.FormValue("to")), Cc: addrs(r.FormValue("cc")), Bcc: addrs(r.FormValue("bcc")),
		Subject: strings.TrimSpace(r.FormValue("subject")), Body: r.FormValue("body")}
}

func (s *Server) mailAct(w http.ResponseWriter, r *http.Request, done string, act func(ctx context.Context, org, who string, p connect.Provider) error) {
	p, ok := s.mailForm(w, r)
	if !ok {
		return
	}
	id := auth.From(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	err := act(ctx, id.OrgID, id.PrincipalID, p)
	s.mailBack(w, r, p, done, err)
}

func (s *Server) postMailSend(w http.ResponseWriter, r *http.Request) {
	s.mailAct(w, r, "Sent.", func(ctx context.Context, org, who string, p connect.Provider) error {
		return s.connect.SendMail(ctx, org, who, p, outgoingFrom(r))
	})
}

func (s *Server) postMailReply(w http.ResponseWriter, r *http.Request) {
	s.mailAct(w, r, "Sent.", func(ctx context.Context, org, who string, p connect.Provider) error {
		return s.connect.ReplyMail(ctx, org, who, p, r.FormValue("id"), outgoingFrom(r), r.FormValue("all") == "on")
	})
}

func (s *Server) postMailForward(w http.ResponseWriter, r *http.Request) {
	s.mailAct(w, r, "Forwarded.", func(ctx context.Context, org, who string, p connect.Provider) error {
		return s.connect.ForwardMail(ctx, org, who, p, r.FormValue("id"), outgoingFrom(r))
	})
}

func (s *Server) postMailArchive(w http.ResponseWriter, r *http.Request) {
	s.mailAct(w, r, "Archived.", func(ctx context.Context, org, who string, p connect.Provider) error {
		return s.connect.ArchiveMail(ctx, org, who, p, r.FormValue("id"))
	})
}

func (s *Server) postMailLabel(w http.ResponseWriter, r *http.Request) {
	s.mailAct(w, r, "Labelled.", func(ctx context.Context, org, who string, p connect.Provider) error {
		return s.connect.LabelMail(ctx, org, who, p, r.FormValue("id"), addrs(r.FormValue("add")), addrs(r.FormValue("remove")))
	})
}

func (s *Server) postMailRead(w http.ResponseWriter, r *http.Request) {
	s.mailAct(w, r, "", func(ctx context.Context, org, who string, p connect.Provider) error {
		return s.connect.MarkMailRead(ctx, org, who, p, r.FormValue("id"), r.FormValue("read") != "off")
	})
}

func (s *Server) postMailDelete(w http.ResponseWriter, r *http.Request) {
	s.mailAct(w, r, "Moved to the bin.", func(ctx context.Context, org, who string, p connect.Provider) error {
		return s.connect.DeleteMail(ctx, org, who, p, r.FormValue("id"))
	})
}
