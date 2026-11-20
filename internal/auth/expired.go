package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

// With the EmberOPS back end a request can fail because the Vord session
// behind the Hearth session has ended (ErrTokenExpired), or because the Hearth
// session never had a Vord token (ErrNoUserToken). Handlers turn every error
// into a status code, so the cause is gone by the time the response is
// written. expiryWriter watches for the failure and, when the handler is about
// to answer with an error status, replaces that answer: a browser is sent to
// /login with the page it wanted as ?next=, an HTMX request is told to
// redirect, and the JSON API answers 401. The Hearth session is ended as well,
// otherwise /login would see a signed-in person and send them straight back.
type expiryWriter struct {
	http.ResponseWriter
	s     *Service
	r     *http.Request
	watch *emberops.AuthWatch
	took  bool // the answer was replaced
	sent  bool
}

func (e *expiryWriter) expired() bool {
	err := e.watch.Err()
	return errors.Is(err, ErrTokenExpired) || errors.Is(err, ErrNoUserToken)
}

func (e *expiryWriter) WriteHeader(code int) {
	if e.sent {
		return
	}
	e.sent = true
	if code >= 400 && e.expired() {
		e.took = true
		e.replace()
		return
	}
	e.ResponseWriter.WriteHeader(code)
}

func (e *expiryWriter) Write(b []byte) (int, error) {
	if !e.sent {
		e.WriteHeader(http.StatusOK)
	}
	if e.took {
		return len(b), nil
	}
	return e.ResponseWriter.Write(b)
}

func (e *expiryWriter) Flush() {
	if f, ok := e.ResponseWriter.(http.Flusher); ok && !e.took {
		f.Flush()
	}
}

func (e *expiryWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }

func (e *expiryWriter) replace() {
	h := e.ResponseWriter.Header()
	for k := range h {
		if strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		h.Del(k)
	}
	_ = e.s.Destroy(e.r.Context(), e.ResponseWriter, e.r)
	to := "/login"
	if next := returnTo(e.r); next != "" {
		to += "?next=" + url.QueryEscape(next)
	}
	switch {
	case wantsJSON(e.r):
		h.Set("Content-Type", "application/json")
		e.ResponseWriter.WriteHeader(http.StatusUnauthorized)
		_, _ = e.ResponseWriter.Write([]byte(`{"error":"your sign-in has ended; sign in again"}` + "\n"))
	default:
		h.Set("Location", to)
		e.ResponseWriter.WriteHeader(http.StatusSeeOther)
	}
}

func wantsJSON(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/") ||
		strings.Contains(r.Header.Get("Accept"), "application/json")
}
