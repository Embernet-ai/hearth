package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/push"
)

func signedIn(r *http.Request) *http.Request {
	return r.WithContext(auth.WithIdentity(r.Context(), &auth.Identity{OrgID: "o", PrincipalID: "p"}))
}

// An installation with no push key says so as data, so the settings show a sentence instead of a switch that cannot work.
func TestPushSaysItIsOffWhenThereIsNoKey(t *testing.T) {
	s := newServer(t)
	s.Routes()
	for _, c := range []struct{ method, path string }{
		{"GET", "/push/state"}, {"POST", "/push/subscribe"}, {"POST", "/push/test"}, {"POST", "/push/kinds"},
	} {
		w := httptest.NewRecorder()
		s.authed.ServeHTTP(w, signedIn(httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}

// A post that is not JSON is refused before anything is read or saved: a cross-site form cannot send JSON.
func TestPushPostsMustBeJSON(t *testing.T) {
	s := newServer(t)
	s.Routes()
	s.push = &push.Service{}
	for _, path := range []string{"/push/subscribe", "/push/unsubscribe", "/push/kinds", "/push/test"} {
		r := httptest.NewRequest("POST", path, strings.NewReader("endpoint=https://fcm.googleapis.com/x"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.authed.ServeHTTP(w, signedIn(r))
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s answered %d to a form post, want 415", path, w.Code)
		}
	}
}

// The service worker and the manifest are what make this an installable app that a phone will ring. The worker answers a push
// by showing a notification and a click by opening the item, and it only ever follows a path on this site.
func TestTheWorkerRingsAndOpensTheItem(t *testing.T) {
	body, err := staticFS.ReadFile("static/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	for _, want := range []string{`addEventListener("push"`, `showNotification(`, `addEventListener("notificationclick"`, `openWindow(`, `addEventListener("pushsubscriptionchange"`, `charAt(1) !== "/"`} {
		if !strings.Contains(src, want) {
			t.Errorf("the service worker has no %s", want)
		}
	}
}
