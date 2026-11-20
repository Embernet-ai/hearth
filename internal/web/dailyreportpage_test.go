package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/docs"
)

func TestEveryReportQueryDefaultsToTheLastSevenDays(t *testing.T) {
	r := newRequest("/reports/daily")
	f := reportFilterFrom(r)
	if f.To != docs.ReportDay(time.Now()) || f.From != docs.ReportDay(time.Now().AddDate(0, 0, -6)) {
		t.Fatalf("default range = %s to %s", f.From, f.To)
	}
	// Reversed dates are put right, and junk ids are dropped rather than
	// reaching the database.
	r = newRequest("/reports/daily?from=2026-10-26&to=2026-10-01&space=drop-table&person=x")
	f = reportFilterFrom(r)
	if f.From != "2026-10-01" || f.To != "2026-10-26" || f.SpaceID != "" || f.PersonID != "" {
		t.Fatalf("filter = %+v", f)
	}
}

func newRequest(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, nil)
}
