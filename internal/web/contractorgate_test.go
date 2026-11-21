package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/deptreq"
)

func contractorRequest(t *testing.T, method, path string, id *auth.Identity) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	reached := false
	s := &Server{}
	h := s.contractorGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Accept", "application/json")
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, reached
}

// A contractor account reaches the work in its folder, its search, inbox and
// account, and every other address is not found, writes included. These are the
// pages that list the company's contacts, deals, channels, calendar, people and
// time.
func TestAContractorReachesOnlyTheWork(t *testing.T) {
	id := &auth.Identity{PrincipalID: "c1", Contractor: true}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/contacts"}, {http.MethodGet, "/contacts/abc"}, {http.MethodPost, "/contacts"},
		{http.MethodGet, "/companies"}, {http.MethodGet, "/companies/abc"}, {http.MethodGet, "/deals"},
		{http.MethodGet, "/deals/abc"}, {http.MethodGet, "/crm/unassigned"}, {http.MethodGet, "/crm/import"},
		{http.MethodGet, "/channels"}, {http.MethodGet, "/channels/abc"}, {http.MethodPost, "/channels/abc/messages"},
		{http.MethodPost, "/channels/abc/join"}, {http.MethodGet, "/calendar"}, {http.MethodPost, "/calendar"},
		{http.MethodGet, "/reports/daily"}, {http.MethodGet, "/reports/service"}, {http.MethodGet, "/reports/time"},
		{http.MethodGet, "/time"}, {http.MethodGet, "/time-off"}, {http.MethodGet, "/workload"},
		{http.MethodGet, "/people"}, {http.MethodGet, "/people/abc"}, {http.MethodPost, "/people/abc/contractor"},
		{http.MethodPost, "/people/abc/avatar"}, {http.MethodGet, "/teams"}, {http.MethodGet, "/rules"},
		{http.MethodGet, "/integrations"}, {http.MethodGet, "/agents"}, {http.MethodGet, "/settings"},
		{http.MethodGet, "/assets"}, {http.MethodGet, "/parts"}, {http.MethodGet, "/plant"}, {http.MethodGet, "/canvas"},
		{http.MethodGet, "/templates"}, {http.MethodGet, "/import"}, {http.MethodGet, "/audit"}, {http.MethodGet, "/scan"},
		{http.MethodGet, "/departments/request"}, {http.MethodGet, "/outbox"}, {http.MethodGet, "/ops/jobs"},
		{http.MethodGet, "/a/ASSET1"}, {http.MethodGet, "/tags"}, {http.MethodGet, "/maintenance"},
	} {
		w, reached := contractorRequest(t, c.method, c.path, id)
		if reached || w.Code != http.StatusNotFound {
			t.Errorf("%s %s reached=%v code=%d, want it refused with 404 for a contractor", c.method, c.path, reached, w.Code)
		}
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/documents"}, {http.MethodGet, "/documents/abc"}, {http.MethodPost, "/documents/abc"},
		{http.MethodPost, "/documents/abc/comments"}, {http.MethodGet, "/documents/new"}, {http.MethodGet, "/tasks"},
		{http.MethodGet, "/work-orders"}, {http.MethodGet, "/projects"}, {http.MethodGet, "/spaces"},
		{http.MethodGet, "/spaces/abc/access"}, {http.MethodGet, "/views"}, {http.MethodGet, "/search"},
		{http.MethodGet, "/inbox"}, {http.MethodPost, "/inbox/read"}, {http.MethodGet, "/account"},
		{http.MethodPost, "/account/password"}, {http.MethodGet, "/events"}, {http.MethodPost, "/preview"},
		{http.MethodPost, "/offline/sync"}, {http.MethodPost, "/push/subscribe"}, {http.MethodGet, "/people/abc/avatar"},
	} {
		if w, reached := contractorRequest(t, c.method, c.path, id); !reached || w.Code != http.StatusOK {
			t.Errorf("%s %s reached=%v code=%d, want it open to a contractor", c.method, c.path, reached, w.Code)
		}
	}
	// Where the company's front page would be.
	if w, reached := contractorRequest(t, http.MethodGet, "/", id); reached || w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/spaces" {
		t.Errorf("GET / = %d to %q, want 303 to /spaces", w.Code, w.Header().Get("Location"))
	}
	// A path that only starts like an open one is not open.
	for _, p := range []string{"/documentsx", "/spacesfoo", "/inboxes"} {
		if _, reached := contractorRequest(t, http.MethodGet, p, id); reached {
			t.Errorf("%s is open to a contractor because it begins like an open path", p)
		}
	}
}

// Everybody else goes straight through, every address, as they always did, and a
// super admin is never a contractor.
func TestEverybodyButAContractorIsUntouched(t *testing.T) {
	paths := []string{"/", "/contacts", "/companies", "/deals", "/channels", "/calendar", "/reports/daily", "/people", "/time", "/settings", "/documents/abc", "/assets"}
	for _, id := range []*auth.Identity{{PrincipalID: "p1"}, {PrincipalID: "s1", Subjects: core.Subjects{{Kind: core.SubjectSuper, ID: "s1"}}}, nil} {
		for _, p := range paths {
			if w, reached := contractorRequest(t, http.MethodGet, p, id); !reached || w.Code != http.StatusOK {
				t.Errorf("%+v GET %s reached=%v code=%d, want untouched", id, p, reached, w.Code)
			}
		}
	}
}

// A contractor account is in no department on purpose, so it is never asked
// which department it is in. Everybody else is, unless a request is waiting.
func TestAContractorIsNeverAskedWhichDepartment(t *testing.T) {
	if departmentPromptOwed(true, nil) {
		t.Error("a contractor account was asked which department it is in")
	}
	if !departmentPromptOwed(false, nil) {
		t.Error("an ordinary account in no department stopped being asked")
	}
	if departmentPromptOwed(false, &deptreq.Request{Status: "pending"}) {
		t.Error("somebody with a request waiting was asked again")
	}
	if !departmentPromptOwed(false, &deptreq.Request{Status: "denied"}) {
		t.Error("somebody whose request was denied stopped being asked")
	}
}
