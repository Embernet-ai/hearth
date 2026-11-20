package calendar

// The iCalendar writer, which either is exactly right or produces a file a
// phone declines with no error anybody sees.
//
// These are unit tests with no database, because everything worth checking here
// is a string.

import (
	"strings"
	"testing"
	"time"
)

func lines(s string) []string {
	// Split on CRLF specifically, so a test would fail rather than pass if the
	// writer started emitting bare newlines.
	return strings.Split(strings.TrimSuffix(s, "\r\n"), "\r\n")
}

func TestEveryLineEndsCRLF(t *testing.T) {
	out := ICS("Pat", "https://hearth.example.com", []Entry{{
		ID: "abc", Kind: KindEvent, Title: "Stand-up",
		StartsAt: time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, 9, 9, 9, 15, 0, 0, time.UTC),
	}}, time.Now())

	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Error("there is a bare newline in the output. Unix line endings are " +
			"the most common reason a feed that looks right in a terminal is refused")
	}
	if !strings.HasPrefix(out, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(out, "END:VCALENDAR\r\n") {
		t.Error("the calendar is not wrapped correctly")
	}
}

func TestAnAllDayEventIsADateAndEndsTheFollowingDay(t *testing.T) {
	out := ICS("Pat", "", []Entry{{
		ID: "leave", Kind: KindEvent, Title: "Leave", AllDay: true,
		StartsAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
	}}, time.Now())

	if !strings.Contains(out, "DTSTART;VALUE=DATE:20260909") {
		t.Errorf("an all-day event was not written as a date:\n%s", out)
	}
	// DTEND is exclusive. Writing the same date produces a zero-length event
	// that several clients decline to draw at all.
	if !strings.Contains(out, "DTEND;VALUE=DATE:20260910") {
		t.Errorf("DTEND is not the following day, so the event has no length:\n%s", out)
	}
}

func TestATimedEventIsWrittenInUTC(t *testing.T) {
	// A zone deliberately not UTC, so a writer that forgot to convert would
	// produce the wall-clock time and fail here.
	loc := time.FixedZone("EDT", -4*3600)
	out := ICS("Pat", "", []Entry{{
		ID: "mtg", Kind: KindEvent, Title: "Meeting",
		StartsAt: time.Date(2026, 9, 9, 10, 30, 0, 0, loc),
		EndsAt:   time.Date(2026, 9, 9, 11, 30, 0, 0, loc),
	}}, time.Now())

	if !strings.Contains(out, "DTSTART:20260909T143000Z") {
		t.Errorf("10:30 EDT was not written as 14:30 UTC:\n%s", out)
	}
	if !strings.Contains(out, "DTEND:20260909T153000Z") {
		t.Errorf("the end was not converted:\n%s", out)
	}
}

func TestAnEventWithNoEndGetsAnHour(t *testing.T) {
	start := time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)
	out := ICS("Pat", "", []Entry{{
		ID: "x", Kind: KindEvent, Title: "Something", StartsAt: start,
	}}, time.Now())

	if !strings.Contains(out, "DTEND:20260909T100000Z") {
		t.Errorf("an event with no end did not get one:\n%s", out)
	}
}

func TestSeparatorsAndNewlinesAreEscaped(t *testing.T) {
	out := ICS("Pat", "", []Entry{{
		ID: "x", Kind: KindEvent,
		Title:    `Line 3; bearing, guard`,
		Notes:    "first\nsecond",
		StartsAt: time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC),
	}}, time.Now())

	if !strings.Contains(out, `SUMMARY:Line 3\; bearing\, guard`) {
		t.Errorf("a semicolon or comma was left unescaped, which ends the property early:\n%s", out)
	}
	if !strings.Contains(out, `DESCRIPTION:first\nsecond`) {
		t.Errorf("a newline was not escaped, so the file stops parsing mid-property:\n%s", out)
	}
}

func TestABackslashIsEscapedOnlyOnce(t *testing.T) {
	// Backslash has to be replaced first, or escaping the separators would then
	// escape the backslashes that escaping introduced.
	if got := escape(`a\b`); got != `a\\b` {
		t.Errorf("escape(%q) = %q, want %q", `a\b`, got, `a\\b`)
	}
	if got := escape(`a;b`); got != `a\;b` {
		t.Errorf("escape(%q) = %q", `a;b`, got)
	}
}

// Folding is where this breaks in a way nobody catches by reading the file:
// splitting a UTF-8 sequence produces bytes no parser accepts, and one accented
// character in a title is enough to reach it.
func TestFoldingNeverSplitsAMultiByteCharacter(t *testing.T) {
	title := strings.Repeat("é", 100) // two octets each, 200 in total
	out := ICS("Pat", "", []Entry{{
		ID: "x", Kind: KindEvent, Title: title,
		StartsAt: time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC),
	}}, time.Now())

	for _, ln := range lines(out) {
		if len(ln) > 75 {
			t.Errorf("a line is %d octets, over the 75 limit: %q", len(ln), ln)
		}
	}
	// Unfolding is removing CRLF followed by a single space. What comes back
	// has to be the title we put in, byte for byte.
	unfolded := strings.ReplaceAll(out, "\r\n ", "")
	if !strings.Contains(unfolded, "SUMMARY:"+title) {
		t.Error("the title did not survive folding and unfolding, which means a " +
			"character was split across two lines")
	}
	if strings.Contains(unfolded, "�") {
		t.Error("there is a replacement character in the output, so a UTF-8 " +
			"sequence was cut in half")
	}
}

func TestAShortLineIsNotFolded(t *testing.T) {
	if got := fold("SUMMARY:short"); got != "SUMMARY:short" {
		t.Errorf("a short line was folded: %q", got)
	}
}

func TestDueWorkIsTransparentAndLinksBack(t *testing.T) {
	out := ICS("Pat", "https://hearth.example.com/", []Entry{{
		ID: "due-1", Kind: KindDue, Title: "Bearing", DocumentID: "doc-1",
		AllDay:   true,
		StartsAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC),
	}}, time.Now())

	// A deadline is not time somebody is busy. Marking it opaque would make
	// them look unavailable all day to anybody checking free/busy.
	if !strings.Contains(out, "TRANSP:TRANSPARENT") {
		t.Errorf("due work is not transparent:\n%s", out)
	}
	if !strings.Contains(out, "URL:https://hearth.example.com/documents/doc-1") {
		t.Errorf("the entry does not link back to the work order:\n%s", out)
	}
	if !strings.Contains(out, "CATEGORIES:DUE") {
		t.Errorf("the entry does not say what kind it is:\n%s", out)
	}
}

func TestTheUIDIsStableSoRemindersSurviveARefresh(t *testing.T) {
	e := Entry{ID: "abc", Kind: KindEvent, Title: "x",
		StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour)}
	first := ICS("P", "", []Entry{e}, time.Now())
	second := ICS("P", "", []Entry{e}, time.Now().Add(48*time.Hour))

	if !strings.Contains(first, "UID:abc@hearth") || !strings.Contains(second, "UID:abc@hearth") {
		t.Fatal("the uid is not the row id")
	}
	// DTSTAMP moves, and should. The uid must not, or every refresh deletes and
	// recreates the event and any reminder set on it goes with it.
	if strings.Contains(second, "UID:abc@hearth") != strings.Contains(first, "UID:abc@hearth") {
		t.Error("the uid changed between renders")
	}
}

func TestFeedFilenameIsSafe(t *testing.T) {
	if got := FeedFilename("Pat Morgan"); got != "Pat-Morgan.ics" {
		t.Errorf("FeedFilename = %q", got)
	}
	if got := FeedFilename(`../../etc/passwd`); strings.Contains(got, "/") || strings.Contains(got, "..") {
		t.Errorf("FeedFilename let a path through: %q", got)
	}
}
