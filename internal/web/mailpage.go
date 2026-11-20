package web

// Email and calls on screen, plus the one endpoint on this application that a
// machine talks to rather than a person.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/calls"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/mailint"
	"github.com/Embernet-ai/hearth/internal/mailsettings"
	"github.com/Embernet-ai/hearth/internal/platform/mail"
)

// maxInbound bounds one delivery. Twenty five megabytes is what most relays
// accept, and reading an unbounded body into memory is how one sender takes
// the process down.
const maxInbound = 25 << 20

// postInbound accepts a raw RFC 5322 message from the estate's own MTA.
//
// # Why an HTTP endpoint rather than an SMTP listener
//
// Because the estate already runs a relay, and it is better at SMTP than
// anything written here would be. Postfix pipes a message to curl in four
// lines of configuration, the relay keeps doing the spam filtering and the TLS
// and the queueing it already does well, and this application never listens on
// port 25. That is one fewer thing to get wrong and one fewer thing to patch.
//
// # Why it is outside the session middleware
//
// An MTA has no cookies. It authenticates with a shared secret in a header,
// compared in constant time, and the endpoint does nothing at all when no
// secret is configured: an inbound path that is open by default is an inbound
// path that files spam as work orders on the first afternoon it is reachable.
func (s *Server) postInbound(w http.ResponseWriter, r *http.Request) {
	if s.inboundKey == "" {
		http.Error(w, "inbound mail is not configured", http.StatusNotFound)
		return
	}
	if !auth.SecretEqual(r.Header.Get("X-Hearth-Inbound-Key"), s.inboundKey) {
		s.log.WarnContext(r.Context(), "inbound mail rejected", "remote", r.RemoteAddr)
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxInbound))
	if err != nil {
		http.Error(w, "could not read the message", http.StatusBadRequest)
		return
	}

	filed, err := s.mail.Receive(r.Context(), raw)
	switch {
	case errors.Is(err, mailint.ErrNoAddress), errors.Is(err, mail.ErrNoMessageID):
		// Not worth retrying. The relay was misconfigured or somebody was
		// copied in, and neither improves on the ninth attempt. A 2xx stops
		// the retries; the log is where an operator finds out.
		s.log.InfoContext(r.Context(), "inbound mail not for us", "error", err)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, "ignored")
		return
	case err != nil:
		// Something broke on this side. A 5xx is right, because the relay
		// retrying is exactly what should happen and the Message-ID means the
		// retry cannot duplicate anything.
		s.log.ErrorContext(r.Context(), "inbound mail failed", "error", err)
		http.Error(w, "could not file the message", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	switch {
	case filed.Duplicate:
		fmt.Fprintf(w, "already filed as %s\n", filed.DocumentID)
	case filed.Created:
		fmt.Fprintf(w, "created %s\n", filed.DocumentID)
	default:
		fmt.Fprintf(w, "threaded onto %s\n", filed.DocumentID)
	}
}

// -----------------------------------------------------------------------------
// Addresses
// -----------------------------------------------------------------------------

func (s *Server) getMailSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())

	addrs, err := s.mail.Addresses(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list inbound addresses", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the addresses")
		return
	}
	spaces, err := s.plant.SpaceTree(r.Context(), id.OrgID, id.Subjects)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load spaces", "error", err)
	}
	channels, err := s.channels.List(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load channels", "error", err)
	}

	var outbound *mailsettings.Settings
	canStore := false
	if s.mailset != nil {
		canStore = s.mailset.CanStorePassword()
		if outbound, err = s.mailset.Get(r.Context(), id.OrgID); err != nil {
			s.log.ErrorContext(r.Context(), "load outbound mail settings", "error", err)
		}
	}

	msg := ""
	if to := strings.TrimSpace(r.URL.Query().Get("sent")); to != "" {
		msg = "Test email sent to " + to + "."
	}

	s.render(w, r, "mail", page{
		Title: "Email", Message: msg,
		InboundAddresses: addrs, Spaces: spaces, Channels: channels,
		InboundReady: s.inboundKey != "", MailReady: s.mailReady,
		OutboundMail: outbound, CanStorePassword: canStore,
		// The endpoint the relay posts to, spelled out, because the commonest
		// way this feature fails is a relay pointed at the wrong host.
		ScanURL: strings.TrimSuffix(s.publicBase(r), "/") + "/inbound/mail",
	})
}

// saveOutbound writes this org's own outbound relay settings.
func (s *Server) saveOutbound(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.mailset == nil {
		s.fail(w, r, http.StatusNotFound, "outbound mail settings are not available")
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	p := mailsettings.SaveParams{
		Enabled:       r.FormValue("enabled") == "on",
		Host:          r.FormValue("host"),
		Port:          port,
		From:          r.FormValue("from_address"),
		FromName:      r.FormValue("from_name"),
		Username:      r.FormValue("username"),
		Password:      r.FormValue("password"),
		ClearPassword: r.FormValue("clear_password") == "on",
		StartTLS:      r.FormValue("starttls") == "on",
	}
	if err := s.mailset.Save(r.Context(), id.OrgID, id.PrincipalID, p); err != nil {
		if errors.Is(err, mailsettings.ErrInvalid) {
			s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "mailsettings: "))
			return
		}
		s.log.ErrorContext(r.Context(), "save outbound mail settings", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save the email settings")
		return
	}
	http.Redirect(w, r, "/settings/mail", http.StatusSeeOther)
}

// testOutbound sends a one-off message through the org's own relay, to prove it
// works before any notification depends on it.
func (s *Server) testOutbound(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.orgMail == nil {
		s.fail(w, r, http.StatusNotFound, "outbound mail is not available")
		return
	}
	id := auth.From(r.Context())
	to := strings.TrimSpace(r.FormValue("to"))
	if to == "" {
		s.fail(w, r, http.StatusBadRequest, "a recipient address is needed for the test")
		return
	}
	if err := s.orgMail.SendTest(r.Context(), id.OrgID, to); err != nil {
		if errors.Is(err, mailsettings.ErrInvalid) {
			s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "mailsettings: "))
			return
		}
		s.log.WarnContext(r.Context(), "test email failed", "error", err)
		s.fail(w, r, http.StatusBadGateway,
			"the test did not send: "+err.Error())
		return
	}
	http.Redirect(w, r, "/settings/mail?sent="+url.QueryEscape(to), http.StatusSeeOther)
}

func (s *Server) saveInboundAddress(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	a := mailint.Address{
		ID:      strings.TrimSpace(r.FormValue("id")),
		Address: r.FormValue("address"), Kind: r.FormValue("kind"),
		Enabled: r.FormValue("enabled") != "off",
	}
	if v := strings.TrimSpace(r.FormValue("space")); v != "" {
		a.SpaceID = &v
	}
	if v := strings.TrimSpace(r.FormValue("channel")); v != "" {
		a.ChannelID = &v
	}
	if _, err := s.mail.SaveAddress(r.Context(), id.OrgID, id.Subjects, a); err != nil {
		if errors.Is(err, mailint.ErrInvalid) {
			s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "mailint: "))
			return
		}
		s.log.ErrorContext(r.Context(), "save inbound address", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not save the address")
		return
	}
	http.Redirect(w, r, "/settings/mail", http.StatusSeeOther)
}

func (s *Server) deleteInboundAddress(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := s.mail.DeleteAddress(r.Context(), id.OrgID, r.PathValue("id")); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such address")
		return
	}
	http.Redirect(w, r, "/settings/mail", http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Replying
// -----------------------------------------------------------------------------

// postReply answers the latest inbound message on a work order.
//
// Queued rather than sent here. A relay that is slow makes saving a reply slow
// and a relay that is down makes saving one fail, and neither is acceptable
// when the reply is already written.
func (s *Server) postReply(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		s.fail(w, r, http.StatusBadRequest, "an empty reply says nothing")
		return
	}
	// Comment access, because a reply is a message on the work rather than a
	// change to it.
	if err := s.docs.CanComment(r.Context(), id.Subjects, docID); err != nil {
		s.fail(w, r, http.StatusForbidden, "you cannot reply on this")
		return
	}

	reply, err := s.mail.PrepareReply(r.Context(), id.OrgID, docID, body)
	if errors.Is(err, mailint.ErrNotFound) {
		s.fail(w, r, http.StatusBadRequest,
			"nothing arrived here by email, so there is nobody to reply to")
		return
	} else if err != nil {
		s.log.ErrorContext(r.Context(), "prepare reply", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not prepare the reply")
		return
	}

	if err := mailint.Enqueue(r.Context(), s.queue, id.OrgID, mailint.SendPayload{
		DocumentID: docID, To: reply.To, Subject: reply.Subject,
		Body: reply.Body, Headers: reply.Headers(),
	}); err != nil {
		s.log.ErrorContext(r.Context(), "queue reply", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not queue the reply")
		return
	}
	// Recorded now rather than when the relay accepts it, so an answer that
	// comes back before the queue drains still threads onto this work.
	if err := s.mail.RecordOutbound(r.Context(), id.OrgID, docID, reply); err != nil {
		s.log.ErrorContext(r.Context(), "record outbound", "error", err)
	}
	// And on the timeline, because a reply nobody else can see is a reply the
	// next person on shift sends again.
	if err := s.channels.PostAsSystem(r.Context(), id.OrgID, id.PrincipalID,
		docRef(docID), "Replied by email to "+reply.To+"\n\n"+body); err != nil {
		s.log.ErrorContext(r.Context(), "post reply to timeline", "error", err)
	}
	if s.notifyDB != nil {
		jobs.Kick(r.Context(), s.notifyDB)
	}
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// Calls
// -----------------------------------------------------------------------------

func (s *Server) logCall(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	c := calls.Call{
		Direction: r.FormValue("direction"), Number: r.FormValue("number"),
		Outcome: r.FormValue("outcome"), Note: r.FormValue("note"),
		StartedAt: parseMoment(r.FormValue("at")),
	}
	if v := strings.TrimSpace(r.FormValue("contact")); v != "" {
		c.ContactID = &v
	}
	if v := strings.TrimSpace(r.FormValue("document")); v != "" {
		c.EntityID = &v
	}
	// Minutes on the form, seconds in the column. Nobody times a call to the
	// second and a form that asks for seconds gets zeroes.
	if n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("minutes"))); err == nil && n > 0 {
		c.DurationS = n * 60
	}

	made, err := s.calls.Log(r.Context(), id.OrgID, id.Subjects, c)
	if err != nil {
		if errors.Is(err, calls.ErrInvalid) {
			s.fail(w, r, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "calls: "))
			return
		}
		s.log.WarnContext(r.Context(), "log call refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot log a call against that")
		return
	}
	http.Redirect(w, r, backTo(r, callBack(made)), http.StatusSeeOther)
}

func callBack(c *calls.Call) string {
	if c.EntityID != nil {
		return "/documents/" + *c.EntityID
	}
	if c.ContactID != nil {
		return "/contacts/" + *c.ContactID
	}
	return "/contacts"
}

func (s *Server) removeCall(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.calls.Remove(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.fail(w, r, http.StatusForbidden, "you cannot remove that call")
		return
	}
	http.Redirect(w, r, backTo(r, "/contacts"), http.StatusSeeOther)
}

// attachRecording stores audio against a call, through the ordinary file path.
func (s *Server) attachRecording(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	callID := r.PathValue("id")
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed upload")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "no file")
		return
	}
	defer file.Close()

	if _, err := s.calls.AttachRecording(r.Context(), id.OrgID, id.Subjects, callID,
		header.Filename, header.Header.Get("Content-Type"), header.Size, file); err != nil {
		s.log.WarnContext(r.Context(), "attach recording refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot attach a recording to that call")
		return
	}
	http.Redirect(w, r, backTo(r, "/contacts"), http.StatusSeeOther)
}

// -----------------------------------------------------------------------------
// The send job
// -----------------------------------------------------------------------------

// SendHandler returns the worker handler that pushes a reply through the relay.
//
// Registered by main rather than by this package, because the relay is
// platform and the queue is infrastructure, and neither should know what a
// reply is.
func SendHandler(sender interface {
	SendWithHeaders(ctx context.Context, to, subject, body string, headers map[string]string) error
	Configured() bool
}) jobs.Handler {
	return func(ctx context.Context, j *jobs.Job) error {
		var p mailint.SendPayload
		if err := j.Decode(&p); err != nil {
			return err
		}
		if !sender.Configured() {
			// No relay. A success rather than a job that retries forever, and
			// the timeline already shows what would have been sent.
			return nil
		}
		return sender.SendWithHeaders(ctx, p.To, p.Subject, p.Body, p.Headers)
	}
}

// docRef saves the handlers repeating the same two fields.
func docRef(id string) core.Ref {
	return core.Ref{Type: core.EntityDocument, ID: id}
}
