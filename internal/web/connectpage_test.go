package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Without the connectors set up, every connector route answers "not set up" and touches nothing, so a deployment
// that never configured Microsoft 365 or Google Workspace has no new surface.
func TestTheConnectorRoutesAnswerNotSetUpWhenTheDeploymentHasNoConnectors(t *testing.T) {
	s := newServer(t)
	for _, c := range []struct {
		method, path string
		h            http.HandlerFunc
	}{
		{"GET", "/settings/connectors", s.getConnectors},
		{"POST", "/settings/connectors/tenant", s.postConnectorTenant},
		{"POST", "/settings/connectors/toggle", s.postConnectorToggle},
		{"POST", "/settings/connectors/sync", s.postConnectorSync},
		{"POST", "/integrations/microsoft/connect", s.startMicrosoft},
		{"GET", "/integrations/microsoft/callback", s.microsoftCallback},
		{"GET", "/cloud", s.getCloud},
		{"POST", "/cloud/import", s.postCloudImport},
	} {
		w := httptest.NewRecorder()
		c.h(w, httptest.NewRequest(c.method, c.path, strings.NewReader("")))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s answered %d, want 404", c.method, c.path, w.Code)
		}
	}
}

func TestEveryAdministrativeConnectorHandlerChecksForAnAdministratorBeforeActing(t *testing.T) {
	// The handlers that decide what is allowed, or act on somebody else's connection, are the three below. Each
	// calls requireAdmin; this reads the source so a new one cannot be added without it.
	src := mustRead(t, "connectpage.go")
	for _, name := range []string{"postConnectorTenant", "postConnectorToggle", "postConnectorRemovePerson"} {
		i := strings.Index(src, "func (s *Server) "+name)
		if i < 0 {
			t.Fatalf("%s is gone", name)
		}
		body := src[i : i+600]
		if !strings.Contains(body, "requireAdmin") {
			t.Errorf("%s does not call requireAdmin in its first lines", name)
		}
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
