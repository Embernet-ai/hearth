package importer

// Reading the four exports, with no database.
//
// Every fixture here is shaped the way the real thing is: Jira's key column,
// Trello's millisecond dates, Monday's numeric priority, Asana's completion
// column instead of a status. A parser tested against a file it wrote itself
// passes forever and fails on the first export anybody actually has.

import (
	"strings"
	"testing"
)

func TestJiraCSV(t *testing.T) {
	tasks, err := Parse(Jira, strings.NewReader(
		"Issue key,Summary,Description,Status,Priority,Assignee,Due Date,Parent\n"+
			"MAINT-14,Conveyor bearing noise,Drive end.,In Progress,Highest,Dana,2026-09-01,\n"+
			"MAINT-15,Replace the bearing,,Done,Low,Sam,,MAINT-14\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("%d tasks, want 2", len(tasks))
	}
	if tasks[0].ExternalID != "MAINT-14" || tasks[0].Title != "Conveyor bearing noise" {
		t.Errorf("first task is %+v", tasks[0])
	}
	if tasks[0].Status != "in_progress" {
		t.Errorf("status is %q, want in_progress", tasks[0].Status)
	}
	// Highest in Jira is most urgent, which is the opposite of Jira's own
	// numbering and the reason this is mapped rather than passed through.
	if tasks[0].Priority != 3 {
		t.Errorf("priority is %d, want 3 for Highest", tasks[0].Priority)
	}
	if tasks[0].DueAt == nil || tasks[0].DueAt.Format("2006-01-02") != "2026-09-01" {
		t.Errorf("due is %v", tasks[0].DueAt)
	}
	if tasks[1].Status != "done" || tasks[1].Priority != 0 {
		t.Errorf("second task is %+v", tasks[1])
	}
	if tasks[1].Parent != "MAINT-14" {
		t.Errorf("parent is %q", tasks[1].Parent)
	}
}

// Jira's numeric priority counts downwards: 1 is Highest. Passing it through
// makes every critical issue read as low, which nobody notices until a
// shutdown.
func TestJiraNumericPriorityIsInverted(t *testing.T) {
	tasks, err := Parse(Jira, strings.NewReader(
		"Issue key,Summary,Priority\nA-1,Critical,1\nA-2,Trivial,5\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tasks[0].Priority != 3 {
		t.Errorf("Jira priority 1 became %d, want 3 (most urgent)", tasks[0].Priority)
	}
	if tasks[1].Priority != 0 {
		t.Errorf("Jira priority 5 became %d, want 0", tasks[1].Priority)
	}
}

func TestAsanaUsesACompletionColumnRatherThanAStatus(t *testing.T) {
	tasks, err := Parse(Asana, strings.NewReader(
		"Task ID,Name,Notes,Due Date,Completed At,Assignee\n"+
			"1101,Grease the drive,Every quarter,2026-10-01,,Dana\n"+
			"1102,Change the filter,,2026-08-01,2026-08-02T09:00:00Z,Sam\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tasks[0].Status != "todo" {
		t.Errorf("an incomplete Asana task is %q, want todo", tasks[0].Status)
	}
	if tasks[1].Status != "done" {
		t.Errorf("a completed Asana task is %q, want done.\n"+
			"Asana has no status column, so reading only that marks every "+
			"finished task as outstanding.", tasks[1].Status)
	}
}

// Trello has no status at all: the list the card is in IS the status, and the
// dates are milliseconds.
func TestTrelloListIsTheStatusAndDatesAreMilliseconds(t *testing.T) {
	tasks, err := Parse(Trello, strings.NewReader(`[
	  {"id":"abc123","name":"Rewind the motor","desc":"Sent away.",
	   "list":{"name":"Doing"},"due":1788000000000,
	   "members":[{"fullName":"Dana"}],"url":"https://trello.com/c/abc123"},
	  {"id":"def456","name":"Order a spare","list":{"name":"Done"}}
	]`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("%d cards, want 2", len(tasks))
	}
	if tasks[0].Status != "in_progress" {
		t.Errorf("a card in Doing is %q, want in_progress", tasks[0].Status)
	}
	if tasks[1].Status != "done" {
		t.Errorf("a card in Done is %q, want done", tasks[1].Status)
	}
	if tasks[0].DueAt == nil || tasks[0].DueAt.Year() < 2026 {
		t.Errorf("the millisecond date parsed to %v", tasks[0].DueAt)
	}
	if len(tasks[0].Assignees) != 1 || tasks[0].Assignees[0] != "Dana" {
		t.Errorf("members came through as %v, and a nested object has to be "+
			"flattened to its name", tasks[0].Assignees)
	}
	if tasks[0].URL == "" {
		t.Error("the original URL was dropped")
	}
}

func TestMondayAndGenericCSV(t *testing.T) {
	tasks, err := Parse(Monday, strings.NewReader(
		"Item ID,Name,Status,Priority,Person,Date\n"+
			"9001,Inspect the guard,Stuck,1,Dana,2026-09-15\n"))
	if err != nil {
		t.Fatalf("monday: %v", err)
	}
	if tasks[0].Status != "blocked" {
		t.Errorf("Stuck became %q, want blocked", tasks[0].Status)
	}

	generic, err := Parse(Generic, strings.NewReader(
		"id,title,description,state,priority,due\n"+
			"W-1,Check the pump,It hums.,open,urgent,2026-07-01\n"))
	if err != nil {
		t.Fatalf("generic: %v", err)
	}
	if generic[0].Title != "Check the pump" || generic[0].Priority != 3 {
		t.Errorf("generic row is %+v", generic[0])
	}
	// An unrecognised state becomes todo rather than losing the row. A work
	// order in the wrong column can be moved; one that never imported has to
	// be found first.
	if generic[0].Status != "todo" {
		t.Errorf("an unknown state became %q, want todo", generic[0].Status)
	}
}

// Excel puts a byte order mark at the front of every CSV it writes. Without
// stripping it the first column name never matches, which loses the id column
// and makes the whole import unre-runnable.
func TestAByteOrderMarkDoesNotEatTheFirstColumn(t *testing.T) {
	tasks, err := Parse(Generic, strings.NewReader(
		"\ufeffid,title\nX-1,From Excel\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tasks[0].ExternalID != "X-1" {
		t.Errorf("the id column was lost to the byte order mark: %+v", tasks[0])
	}
}

// A column nobody mapped is kept rather than dropped. Losing it silently is
// how an import gets blamed for data that was never actually lost.
func TestUnmappedColumnsAreKept(t *testing.T) {
	tasks, err := Parse(Generic, strings.NewReader(
		"id,title,Serial Number\nX-2,Pump,SN-99187\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tasks[0].Extra["Serial Number"] != "SN-99187" {
		t.Errorf("the unmapped column was dropped: %+v", tasks[0].Extra)
	}
}

// One bad row does not lose the other three thousand nine hundred and
// ninety-nine.
func TestARaggedRowDoesNotLoseTheFile(t *testing.T) {
	tasks, err := Parse(Generic, strings.NewReader(
		"id,title,description\n"+
			"A,First,fine\n"+
			"B,Second,\"unterminated\n"+
			"C,Third,fine\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tasks) < 2 {
		t.Errorf("%d rows survived a ragged file, want at least the good ones", len(tasks))
	}
}

func TestAnEmptyExportIsAnError(t *testing.T) {
	if _, err := Parse(Generic, strings.NewReader("")); err == nil {
		t.Error("an empty file was accepted")
	}
	if _, err := Parse(Generic, strings.NewReader("id,title\n")); err == nil {
		t.Error("a file with only a header was accepted")
	}
	if _, err := Parse("notatool", strings.NewReader("id,title\nA,B\n")); err == nil {
		t.Error("an unknown source was accepted")
	}
}
