package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Embernet-ai/hearth/internal/docs"
)

func TestAResellersJobsSplitByEndCustomerAndNothingIsDropped(t *testing.T) {
	recs := []*docs.ServiceRecord{
		{ID: "1", Job: "MCS0391 Nucor Tubular"},
		{ID: "2", Job: "MCS0391 Nucor Tubular"},
		{ID: "3", Job: "MCS0374 CAD support"},
		{ID: "4", Job: "MCS0342 Alpha Shear"},
		{ID: "5", Job: "A job nobody mapped"},
	}
	jobs := map[string]string{
		"MCS0391 Nucor Tubular": "Nucor Tubular",
		"MCS0342 Alpha Shear":   "Alpha Shear",
		"MCS0374 CAD support":   "", // generic work: stays with the reseller
	}
	groups := splitByEndCustomer("MCSI", recs, jobs)
	got := map[string]int{}
	total := 0
	for _, g := range groups {
		got[g.name] = len(g.recs)
		total += len(g.recs)
	}
	if total != len(recs) {
		t.Fatalf("records dropped or duplicated: %d of %d", total, len(recs))
	}
	if got["MCSI"] != 2 || got["Nucor Tubular (via MCSI)"] != 2 || got["Alpha Shear (via MCSI)"] != 1 {
		t.Fatalf("groups = %v", got)
	}
	if recs[0].EndCustomer != "Nucor Tubular" || recs[2].EndCustomer != "" {
		t.Fatalf("end customer labels: %q / %q", recs[0].EndCustomer, recs[2].EndCustomer)
	}
}

func TestReadSplitMap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.csv")
	if err := os.WriteFile(p, []byte("folder,job,end_customer,note\nMCSI,MCS0342 Alpha Shear,Alpha Shear,\nMCSI,MCS0374 CAD support,,generic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := readSplitMap(p)
	if err != nil {
		t.Fatal(err)
	}
	if m["MCSI"]["MCS0342 Alpha Shear"] != "Alpha Shear" || m["MCSI"]["MCS0374 CAD support"] != "" {
		t.Fatalf("map = %v", m)
	}
	if none, err := readSplitMap(""); err != nil || none != nil {
		t.Fatalf("no path should mean no map: %v %v", none, err)
	}
}
