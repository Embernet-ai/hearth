package calendar

// Writing iCalendar, RFC 5545.
//
// Written out rather than pulled in, because the subset this needs is small and
// the parts that matter are the parts a library would hide: line folding, CRLF,
// escaping, and the difference between a date and an instant. Those are exactly
// the things that make a phone silently refuse a feed with no error anybody
// ever sees, so they are here where they can be read and tested.

import (
	"fmt"
	"strings"
	"time"
)

// icsTime is the UTC form: 20260909T143000Z.
const icsTime = "20060102T150405Z"

// icsDate is the all-day form. A day is a date, not an instant, and writing
// midnight in UTC instead moves somebody's leave to the previous evening for
// every colleague west of them.
const icsDate = "20060102"

// escape applies RFC 5545 section 3.3.11.
//
// Backslash first, or escaping the others would then escape the backslashes
// this function just added. Semicolons and commas are separators inside a
// property value, and a newline becomes a literal \n rather than a real one,
// which would otherwise end the property and produce a file that stops parsing
// half way through somebody's notes.
func escape(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		"\r\n", `\n`,
		"\n", `\n`,
		"\r", `\n`,
		`;`, `\;`,
		`,`, `\,`,
	)
	return r.Replace(s)
}

// fold wraps a line to 75 octets, continuing with a leading space, per RFC 5545
// section 3.1.
//
// Octets, not characters, and that is the whole difficulty: splitting a UTF-8
// sequence in the middle produces bytes no parser will accept, and a title with
// an accent in it is enough to hit that. The loop advances by runes and counts
// their encoded width, so a multi-byte character is never cut.
func fold(line string) string {
	const limit = 75
	if len(line) <= limit {
		return line
	}
	var b strings.Builder
	width := 0
	for _, r := range line {
		n := len(string(r))
		// A continuation line begins with one space, which counts towards its
		// own limit, so the budget after the first line is 74.
		if width+n > limit {
			b.WriteString("\r\n ")
			width = 1
		}
		b.WriteRune(r)
		width += n
	}
	return b.String()
}

// writeProp folds and terminates one property line.
func writeProp(b *strings.Builder, name, value string) {
	b.WriteString(fold(name + ":" + value))
	b.WriteString("\r\n")
}

// ICS renders entries as a subscribable calendar.
//
// name titles the calendar in whatever application subscribes to it, and base
// is the site's public URL so an entry that came from a work order can link
// back to it.
func ICS(name, base string, entries []Entry, now time.Time) string {
	var b strings.Builder

	// CRLF throughout, per the spec. Unix line endings are the single most
	// common reason a feed that looks right in a terminal is rejected.
	b.WriteString("BEGIN:VCALENDAR\r\n")
	writeProp(&b, "VERSION", "2.0")
	writeProp(&b, "PRODID", "-//Fireball Industries//Hearth//EN")
	writeProp(&b, "CALSCALE", "GREGORIAN")
	// Published rather than requested: this is a feed to read, and nothing in
	// it is an invitation anybody can accept.
	writeProp(&b, "METHOD", "PUBLISH")
	writeProp(&b, "X-WR-CALNAME", escape(name))
	// Ask for an hour between refreshes. Both spellings, because clients
	// disagree about which one they honour and neither is expensive to send.
	writeProp(&b, "REFRESH-INTERVAL;VALUE=DURATION", "PT1H")
	writeProp(&b, "X-PUBLISHED-TTL", "PT1H")

	for _, e := range entries {
		b.WriteString("BEGIN:VEVENT\r\n")

		// A UID has to be stable for the same event across fetches, or every
		// refresh deletes and recreates it and any reminder somebody set goes
		// with it. The row id is stable, so it is the uid.
		writeProp(&b, "UID", e.ID+"@hearth")
		writeProp(&b, "DTSTAMP", now.UTC().Format(icsTime))

		if e.AllDay {
			// VALUE=DATE, and DTEND is exclusive: a one-day event ends on the
			// following day. Writing the same date for both produces an event
			// of zero length that several clients decline to draw at all.
			writeProp(&b, "DTSTART;VALUE=DATE", e.StartsAt.Format(icsDate))
			end := e.EndsAt
			if !end.After(e.StartsAt) {
				end = e.StartsAt
			}
			writeProp(&b, "DTEND;VALUE=DATE", end.AddDate(0, 0, 1).Format(icsDate))
		} else {
			writeProp(&b, "DTSTART", e.StartsAt.UTC().Format(icsTime))
			end := e.EndsAt
			if !end.After(e.StartsAt) {
				end = e.StartsAt.Add(time.Hour)
			}
			writeProp(&b, "DTEND", end.UTC().Format(icsTime))
		}

		writeProp(&b, "SUMMARY", escape(e.Title))
		if e.Notes != "" {
			writeProp(&b, "DESCRIPTION", escape(e.Notes))
		}
		if e.Place != "" {
			writeProp(&b, "LOCATION", escape(e.Place))
		}
		if e.DocumentID != "" && base != "" {
			writeProp(&b, "URL", escape(strings.TrimSuffix(base, "/")+"/documents/"+e.DocumentID))
		} else if e.Link != "" {
			// An entry Hearth does not own, such as one pulled from a connected
			// Google calendar. It links back to where it actually lives.
			writeProp(&b, "URL", escape(e.Link))
		}
		// What it is, so a reader can tell a meeting from a due date without
		// reading the title. CATEGORIES is the standard place for it.
		writeProp(&b, "CATEGORIES", escape(strings.ToUpper(string(e.Kind))))

		// Work that is due is not time somebody is busy. Marking it
		// TRANSPARENT keeps it off the free/busy calculation, so a deadline
		// does not make somebody look unavailable all day.
		if e.Kind == KindDue {
			writeProp(&b, "TRANSP", "TRANSPARENT")
		}

		b.WriteString("END:VEVENT\r\n")
	}

	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// FeedFilename is what a browser saves the feed as, if somebody opens it
// directly rather than subscribing.
func FeedFilename(name string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, name)
	return fmt.Sprintf("%s.ics", strings.Trim(clean, "-"))
}
