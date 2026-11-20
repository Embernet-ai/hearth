package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPlanTakesTheDryRunFileAsWritten(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plan.csv")
	if err := os.WriteFile(p, []byte(`id,kind,title,space,clickup_id,clickup_assignees,current_assignees,proposed_assignees,proposed_ids,primary,action,group,reason,patrick_removed
d1,task,"A, b",s,c1,Marco Dell,,Yara Stone,y1,Yara Stone,change,has gone assignee,assignee gone,
d2,task,t,s,c2,"Nina Alvarez, Ivan Brooks","Nina Alvarez;Ivan Brooks","Nina Alvarez;Ivan Brooks",n1;i1,Nina Alvarez,no change,mapped,,
`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := readPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 {
		t.Fatalf("rows = %d", len(plan))
	}
	if plan[0].DocID != "d1" || len(plan[0].Assignees) != 1 || plan[0].Primary != "y1" || len(plan[0].ExpectCurrent) != 0 {
		t.Fatalf("row 0 = %+v", plan[0])
	}
	if plan[0].ExpectCurrent == nil {
		t.Fatal("an empty current list must still be checked, not skipped")
	}
	if len(plan[1].Assignees) != 2 || plan[1].Primary != "n1" || len(plan[1].ExpectCurrent) != 2 {
		t.Fatalf("row 1 = %+v", plan[1])
	}
}

func TestReadPlanReadsTheReverseFileToo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "undo.csv")
	if err := os.WriteFile(p, []byte("id,proposed_ids,primary_id,was\nd1,a;b,b,A;B\nd2,,,\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := readPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	if plan[0].Primary != "b" || plan[0].ExpectCurrent != nil {
		t.Fatalf("row 0 = %+v", plan[0])
	}
	if len(plan[1].Assignees) != 0 || plan[1].Primary != "" {
		t.Fatalf("an emptied row = %+v", plan[1])
	}
}

func TestReadPlanRefusesADuplicateRow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dup.csv")
	_ = os.WriteFile(p, []byte("id,proposed_ids\nd1,a\nd1,b\n"), 0o644)
	if _, err := readPlan(p); err == nil {
		t.Fatal("accepted a document twice")
	}
}
