package mautic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Mautic's own field definitions and company search, as it answers them, are what the limits and the name adoption stand on.
func TestFieldDefsAndFindCompanyReadMauticsAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v2/token":
			_, _ = w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
		case "/api/fields/contact":
			_, _ = w.Write([]byte(`{"total":"3","fields":{"1":{"id":1,"alias":"position","type":"text","charLengthLimit":64},
				"2":{"id":2,"alias":"phone","type":"tel","charLengthLimit":null},"3":{"id":3,"alias":"email","type":"email","charLengthLimit":64}}}`))
		case "/api/companies":
			_, _ = w.Write([]byte(`{"total":"2","companies":{"7":{"id":7,"fields":{"all":{"companyname":"Already In Mautic"}}},
				"9":{"id":9,"fields":{"all":{"companyname":"Already In Mautic Holdings"}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "id", "secret")
	defs, err := c.FieldDefs(context.Background(), "contact")
	if err != nil {
		t.Fatal(err)
	}
	if d := defs["position"]; d.Type != "text" || d.Limit != 64 {
		t.Errorf("position = %+v, want text 64", d)
	}
	if d := defs["phone"]; d.Limit != 0 {
		t.Errorf("a field with a null limit has limit %d, want none", d.Limit)
	}
	if d := defs["email"]; d.Type != "email" || d.Limit != 64 {
		t.Errorf("email = %+v", d)
	}
	id, err := c.FindCompany(context.Background(), "already in mautic")
	if err != nil || id != 7 {
		t.Errorf("FindCompany = %d, %v, want 7 (exact name, any case, not the longer one)", id, err)
	}
	if id, _ := c.FindCompany(context.Background(), "Nobody"); id != 0 {
		t.Errorf("a company that is not there answered %d", id)
	}
}
