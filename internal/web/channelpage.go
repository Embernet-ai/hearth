package web

// Channels, messages, and comments.
//
// The realtime topics change shape here. Until now everything was one org-wide
// "something changed" signal, which is fine for a list that refetches. A
// conversation needs to know WHICH conversation, or every open tab in the
// company refetches every channel on every message.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/realtime"
)

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	list, err := s.channels.List(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list channels", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the channels")
		return
	}
	s.render(w, r, "channels", page{Title: "Channels", Channels: list})
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	c, err := s.channels.Create(r.Context(), id.OrgID, id.PrincipalID,
		r.FormValue("name"), r.FormValue("topic"), r.FormValue("visibility"))
	if err != nil {
		list, _ := s.channels.List(r.Context(), id.OrgID, id.PrincipalID)
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "channels", page{
			Title: "Channels", Channels: list,
			Message: strings.TrimPrefix(err.Error(), "channels: "),
		})
		return
	}
	http.Redirect(w, r, "/channels/"+c.ID, http.StatusSeeOther)
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	channelID := r.PathValue("id")

	c, err := s.channels.Get(r.Context(), id.OrgID, id.PrincipalID, channelID)
	switch {
	case errors.Is(err, channels.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "no such channel")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "get channel", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load that channel")
		return
	}

	timeline, err := s.channels.ChannelTimeline(r.Context(), id.OrgID, id.PrincipalID, channelID, 200)
	if err != nil {
		s.log.ErrorContext(r.Context(), "channel timeline", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not load the messages")
		return
	}
	members, err := s.channels.Members(r.Context(), channelID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "channel members", "error", err)
	}
	attached, err := s.channels.AttachedDocuments(r.Context(), id.OrgID, channelID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "attached documents", "error", err)
	}

	// Reading it is what marks it read. Anything else means a badge that only
	// clears when somebody remembers to press a button.
	if c.Member {
		if err := s.channels.MarkRead(r.Context(), channelID, id.PrincipalID); err != nil {
			s.log.ErrorContext(r.Context(), "mark read", "error", err)
		}
	}

	people, err := s.docs.Assignable(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "load people", "error", err)
	}

	s.render(w, r, "channel", page{
		Title: c.Name, Channel: c, Messages: timeline,
		ChannelMembers: members, AttachedDocs: attached, People: people,
	})
}

func (s *Server) postChannelMessage(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	channelID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	_, err := s.channels.Post(r.Context(), id.OrgID, id.Subjects, channels.PostParams{
		ChannelID: channelID,
		ParentID:  r.FormValue("parent"),
		Body:      r.FormValue("body"),
	})
	switch {
	case errors.Is(err, channels.ErrEmpty):
		http.Redirect(w, r, "/channels/"+channelID, http.StatusSeeOther)
		return
	case errors.Is(err, channels.ErrNotMember):
		s.fail(w, r, http.StatusForbidden, "join the channel before posting in it")
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "post message", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not post that")
		return
	}

	// Named, so only the tabs looking at this channel refetch.
	s.hub.Publish(realtime.ChannelTopic(channelID),
		realtime.Event{Name: "message", Data: channelID})
	http.Redirect(w, r, "/channels/"+channelID, http.StatusSeeOther)
}

func (s *Server) joinChannel(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	channelID := r.PathValue("id")
	if err := s.channels.Join(r.Context(), id.OrgID, id.PrincipalID, channelID); err != nil {
		s.fail(w, r, http.StatusNotFound, "no such channel, or it is not open to join")
		return
	}
	http.Redirect(w, r, "/channels/"+channelID, http.StatusSeeOther)
}

func (s *Server) leaveChannel(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := s.channels.Leave(r.Context(), r.PathValue("id"), id.PrincipalID); err != nil {
		s.log.ErrorContext(r.Context(), "leave channel", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not leave")
		return
	}
	// Back to the list rather than the channel, because leaving a private one
	// means the page they came from is now a 404 and landing on it looks like
	// something broke.
	http.Redirect(w, r, "/channels", http.StatusSeeOther)
}

func (s *Server) inviteToChannel(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	channelID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	err := s.channels.Invite(r.Context(), id.OrgID, id.PrincipalID, channelID, r.FormValue("principal"))
	if err != nil {
		s.log.WarnContext(r.Context(), "invite refused", "channel", channelID, "error", err)
		s.fail(w, r, http.StatusForbidden, "could not add them")
		return
	}
	http.Redirect(w, r, "/channels/"+channelID, http.StatusSeeOther)
}

// attachToChannel is the interface finally reaching the grant that docs.Create
// has been making since the substrate landed.
func (s *Server) attachToChannel(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	channelID := r.FormValue("channel")
	if channelID == "" {
		http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
		return
	}
	if err := s.channels.Attach(r.Context(), id.OrgID, id.Subjects, channelID, docID); err != nil {
		s.log.WarnContext(r.Context(), "attach refused", "doc", docID, "error", err)
		s.fail(w, r, http.StatusForbidden,
			"you need admin on this document and membership of that channel")
		return
	}
	s.notify(id.OrgID, "documents")
	http.Redirect(w, r, "/documents/"+docID, http.StatusSeeOther)
}

// postComment adds a comment to a document.
//
// It needs COMMENT access, not edit. That level has been in the enum since the
// beginning with nothing using it, and this is what it was for: somebody who
// may discuss a work order without being trusted to change it.
func (s *Server) postComment(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	docID := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}

	_, err := s.channels.Post(r.Context(), id.OrgID, id.Subjects, channels.PostParams{
		Entity:   core.Ref{Type: core.EntityDocument, ID: docID},
		ParentID: r.FormValue("parent"),
		Body:     r.FormValue("body"),
	})
	switch {
	case errors.Is(err, channels.ErrEmpty):
		http.Redirect(w, r, docPage(r, docID), http.StatusSeeOther)
		return
	case err != nil:
		s.log.WarnContext(r.Context(), "comment refused", "doc", docID, "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot comment on this")
		return
	}

	s.hub.Publish(realtime.DocTopic(docID), realtime.Event{Name: "comment", Data: docID})
	http.Redirect(w, r, docPage(r, docID), http.StatusSeeOther)
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	msgID := r.PathValue("id")
	if err := s.channels.Delete(r.Context(), id.OrgID, id.Subjects, msgID); err != nil {
		s.log.WarnContext(r.Context(), "delete message refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot remove that")
		return
	}
	// Back where they came from, because a message is deleted from two very
	// different pages and neither of them is a message page. commentRemoved
	// carries the id through so the page it lands on can offer an immediate
	// Undo -- same mechanism as time entries' justRemoved, an unrecognised
	// query param is harmless on the pages that do not look for it.
	back := r.Referer()
	if back == "" {
		back = "/channels"
	}
	http.Redirect(w, r, withParam(back, "commentRemoved", msgID), http.StatusSeeOther)
}

// editMessage changes a message's body in place. Same generic shape as
// deleteMessage: the service knows a message's home well enough that the
// route does not need to.
func (s *Server) editMessage(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	back := r.FormValue("back")
	if back == "" {
		back = r.Referer()
	}
	if back == "" {
		back = "/channels"
	}
	err := s.channels.Edit(r.Context(), id.OrgID, id.Subjects, r.PathValue("id"), r.FormValue("body"))
	switch {
	case errors.Is(err, channels.ErrEmpty):
		// Nothing typed -- leave the message as it was rather than erroring.
	case errors.Is(err, channels.ErrDeleted):
		s.fail(w, r, http.StatusConflict, "that comment was removed -- restore it first")
		return
	case err != nil:
		s.log.WarnContext(r.Context(), "edit message refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot edit that")
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// restoreMessage undoes a Delete. Reachable today only from the inline Undo
// banner deleteMessage's redirect offers -- there is no "show removed
// comments" list yet, the way My Time has for time entries. A comment
// mistakenly removed is normally noticed on the spot; a persistent list is a
// reasonable later addition if that turns out not to hold.
func (s *Server) restoreMessage(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, "malformed form")
		return
	}
	back := r.FormValue("back")
	if back == "" {
		back = r.Referer()
	}
	if back == "" {
		back = "/channels"
	}
	if err := s.channels.Restore(r.Context(), id.OrgID, id.Subjects, r.PathValue("id")); err != nil {
		s.log.WarnContext(r.Context(), "restore message refused", "error", err)
		s.fail(w, r, http.StatusForbidden, "you cannot restore that")
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
