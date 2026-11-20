package web

// Web Push, from the person's side: turn it on for this device, say which notifications ring, try it,
// turn it off. The page is the inbox's settings; these are the JSON addresses behind its buttons.
//
// All five act only on the signed-in person's own devices and choices. Nothing here takes a person id,
// so there is no address that could name somebody else's.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/push"
)

// pushJSON refuses a post that is not JSON. A cross-site form cannot send application/json without a
// preflight the server never answers, so this is a second wall behind the session cookie's SameSite.
func (s *Server) pushJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		s.writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "send JSON"})
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(into); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
		return false
	}
	return true
}

// pushOff answers when this installation has no push key. The page reads it and says so instead of
// showing a switch that cannot work.
func (s *Server) pushOff(w http.ResponseWriter) bool {
	if s.push != nil {
		return false
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
	return true
}

func (s *Server) getPushState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.pushOff(w) {
		return
	}
	id := auth.From(r.Context())
	devs, err := s.push.Devices(r.Context(), id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "push state", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read your devices"})
		return
	}
	on, err := s.push.Kinds(r.Context(), id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "push state", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read your choices"})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "key": s.push.PublicKey(), "devices": devs, "kinds": push.Kinds, "on": on,
	})
}

func (s *Server) postPushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.pushOff(w) {
		return
	}
	id := auth.From(r.Context())
	var b struct {
		push.Subscription
		Label string `json:"label"`
	}
	if !s.pushJSON(w, r, &b) {
		return
	}
	if err := s.push.Subscribe(r.Context(), id.OrgID, id.PrincipalID, b.Subscription, b.Label); err != nil {
		if errors.Is(err, push.ErrBadSubscription) {
			s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.log.ErrorContext(r.Context(), "push subscribe", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save this device"})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) postPushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.pushOff(w) {
		return
	}
	id := auth.From(r.Context())
	var b struct {
		Endpoint string `json:"endpoint"`
		ID       string `json:"id"`
	}
	if !s.pushJSON(w, r, &b) {
		return
	}
	which := b.Endpoint
	if which == "" {
		which = b.ID
	}
	if which == "" {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "which device"})
		return
	}
	if err := s.push.Unsubscribe(r.Context(), id.PrincipalID, which); err != nil {
		s.log.ErrorContext(r.Context(), "push unsubscribe", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not remove that device"})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) postPushKinds(w http.ResponseWriter, r *http.Request) {
	if s.pushOff(w) {
		return
	}
	id := auth.From(r.Context())
	var b struct {
		Kinds []string `json:"kinds"`
	}
	if !s.pushJSON(w, r, &b) {
		return
	}
	if err := s.push.SetKinds(r.Context(), id.PrincipalID, b.Kinds); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": strings.TrimPrefix(err.Error(), "push: ")})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) postPushTest(w http.ResponseWriter, r *http.Request) {
	if s.pushOff(w) {
		return
	}
	id := auth.From(r.Context())
	var b struct {
		Endpoint string `json:"endpoint"`
	}
	if !s.pushJSON(w, r, &b) {
		return
	}
	res, err := s.push.Test(r.Context(), id.PrincipalID, b.Endpoint)
	if err != nil {
		s.log.ErrorContext(r.Context(), "push test", "error", err)
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not send the test"})
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}
