package mail

// Parsing real mail, which is the part that goes wrong quietly.
//
// Every message here is written the way a client actually sends one: folded
// headers, encoded words, a multipart body with both alternatives, and base64
// attachments. A parser tested only against a message it wrote itself passes
// forever and fails on the first thing a phone sends.

import (
	"strings"
	"testing"
)

func TestParseAPlainMessage(t *testing.T) {
	raw := crlf(`From: Dana Whitfield <dana@example.com>
To: maintenance@works.example
Subject: Line 3 conveyor is making a noise
Message-Id: <abc123@example.com>
Date: Tue, 12 Aug 2026 09:14:00 +0100

It started this morning, on the drive end.
`)
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.MessageID != "abc123@example.com" {
		t.Errorf("message id is %q, want it without the angle brackets", m.MessageID)
	}
	if m.From != "dana@example.com" || m.FromName != "Dana Whitfield" {
		t.Errorf("from is %q / %q", m.FromName, m.From)
	}
	if len(m.To) == 0 || m.To[0] != "maintenance@works.example" {
		t.Errorf("to is %v", m.To)
	}
	if m.Subject != "Line 3 conveyor is making a noise" {
		t.Errorf("subject is %q", m.Subject)
	}
	if !strings.Contains(m.Text, "drive end") {
		t.Errorf("body is %q", m.Text)
	}
}

// No Message-ID is refused rather than given one. Inventing a key turns a
// relay retry into a second work order, which is the exact failure the whole
// path exists to avoid.
func TestAMessageWithNoIdIsRefused(t *testing.T) {
	raw := crlf(`From: nobody@example.com
To: maintenance@works.example
Subject: No id

Body.
`)
	if _, err := Parse([]byte(raw)); err == nil {
		t.Error("a message with no Message-ID was accepted, so a relay retry " +
			"would create a second work order")
	}
}

// A subject in an encoded word has to come out readable, because it becomes the
// title of a work order and "=?utf-8?B?...?=" is not a title.
func TestAnEncodedSubjectIsDecoded(t *testing.T) {
	raw := crlf(`From: dana@example.com
To: maintenance@works.example
Subject: =?utf-8?q?Pump_caf=C3=A9_area_is_leaking?=
Message-Id: <enc@example.com>

Body.
`)
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Subject != "Pump café area is leaking" {
		t.Errorf("subject decoded to %q", m.Subject)
	}
}

// Almost every client sends both a text and an HTML part. Taking the HTML when
// the text is there means a work order body full of markup.
func TestMultipartPrefersThePlainPart(t *testing.T) {
	raw := crlf(`From: dana@example.com
To: maintenance@works.example
Subject: Both parts
Message-Id: <multi@example.com>
Content-Type: multipart/alternative; boundary="BOUND"

--BOUND
Content-Type: text/plain; charset=utf-8

The bearing is rumbling.
--BOUND
Content-Type: text/html; charset=utf-8

<html><body><p>The bearing is <b>rumbling</b>.</p></body></html>
--BOUND--
`)
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(m.Text, "<b>") || strings.Contains(m.Text, "<p>") {
		t.Errorf("markup reached the body: %q", m.Text)
	}
	// Exactly once. Checking only that the tags are gone passes even when both
	// parts are taken, because the HTML one strips down to the same sentence
	// and the body quietly says everything twice.
	if n := strings.Count(m.Text, "The bearing is rumbling."); n != 1 {
		t.Errorf("the body says it %d times, want once: %q.\n"+
			"Almost every client sends both alternatives, so taking both means "+
			"every work order raised by email is doubled.", n, m.Text)
	}
}

// And when HTML is all there is, it has to become something readable rather
// than a wall of tags.
func TestHTMLOnlyBecomesReadableText(t *testing.T) {
	raw := crlf(`From: dana@example.com
To: maintenance@works.example
Subject: HTML only
Message-Id: <htmlonly@example.com>
Content-Type: text/html; charset=utf-8

<html><head><style>p{color:red}</style></head><body>
<p>Line 3 is down.</p><p>Called at 09:14 &amp; still waiting.</p>
<script>alert(1)</script>
</body></html>
`)
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(m.Text, "color:red") {
		t.Errorf("the stylesheet ended up in the body: %q", m.Text)
	}
	if strings.Contains(m.Text, "alert(1)") {
		t.Errorf("the script ended up in the body: %q", m.Text)
	}
	if !strings.Contains(m.Text, "Line 3 is down.") ||
		!strings.Contains(m.Text, "Called at 09:14 & still waiting.") {
		t.Errorf("body is %q", m.Text)
	}
	// The paragraphs are separate lines rather than run together.
	if strings.Contains(m.Text, "down.Called") {
		t.Errorf("block boundaries were lost: %q", m.Text)
	}
}

// The order of entity replacement is load-bearing, and Go randomises map
// iteration, so this would fail intermittently if it were a map.
func TestAmpersandIsUnescapedLast(t *testing.T) {
	for i := 0; i < 50; i++ {
		if got := HTMLToText("<p>&amp;lt;not a tag&amp;gt;</p>"); got != "&lt;not a tag&gt;" {
			t.Fatalf("got %q on run %d, want &lt;not a tag&gt;", got, i)
		}
	}
}

func TestQuotedPrintableAndBase64AreDecoded(t *testing.T) {
	raw := crlf(`From: dana@example.com
To: maintenance@works.example
Subject: Encodings
Message-Id: <enc2@example.com>
Content-Type: multipart/mixed; boundary="B"

--B
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: quoted-printable

The caf=C3=A9 pump is leaking =3D badly.
--B
Content-Type: text/plain; name="reading.txt"
Content-Disposition: attachment; filename="reading.txt"
Content-Transfer-Encoding: base64

NDIwMCBydW4gaG91cnM=
--B--
`)
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(m.Text, "café pump is leaking = badly") {
		t.Errorf("quoted-printable body is %q", m.Text)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("%d attachments, want 1", len(m.Attachments))
	}
	if got := string(m.Attachments[0].Content); got != "4200 run hours" {
		t.Errorf("attachment decoded to %q", got)
	}
	if m.Attachments[0].Filename != "reading.txt" {
		t.Errorf("filename is %q", m.Attachments[0].Filename)
	}
}

// A filename from outside must not be a path.
func TestAnAttachmentFilenameCannotBeAPath(t *testing.T) {
	raw := crlf(`From: dana@example.com
To: maintenance@works.example
Subject: Nasty name
Message-Id: <path@example.com>
Content-Type: multipart/mixed; boundary="B"

--B
Content-Type: text/plain
Content-Disposition: attachment; filename="../../../etc/passwd"

x
--B--
`)
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("%d attachments", len(m.Attachments))
	}
	name := m.Attachments[0].Filename
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		t.Errorf("filename came through as %q", name)
	}
}

// The address the MTA delivered to is often in none of To or Cc: a message to a
// list, or one blind copied here. Matching only To means those never arrive.
func TestDeliveryHeadersCountAsRecipients(t *testing.T) {
	raw := crlf(`From: dana@example.com
To: everyone@list.example
Delivered-To: maintenance@works.example
Subject: Via a list
Message-Id: <list@example.com>

Body.
`)
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, to := range m.To {
		if to == "maintenance@works.example" {
			found = true
		}
	}
	if !found {
		t.Errorf("recipients are %v, and the address it was actually delivered "+
			"to is not among them", m.To)
	}
}

// Threading is the whole References chain, not only In-Reply-To. A reply from a
// phone commonly rewrites the latter and leaves the former intact.
func TestReferencesAreKeptWhole(t *testing.T) {
	raw := crlf(`From: dana@example.com
To: maintenance@works.example
Subject: Re: Line 3
Message-Id: <third@example.com>
In-Reply-To: <second@example.com>
References: <first@example.com>
	<second@example.com>

Still making the noise.
`)
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.InReplyTo != "second@example.com" {
		t.Errorf("in-reply-to is %q", m.InReplyTo)
	}
	if len(m.References) != 2 ||
		m.References[0] != "first@example.com" || m.References[1] != "second@example.com" {
		t.Errorf("references are %v, and a folded header has to survive", m.References)
	}
}

// The composer has to produce headers a client will thread on, and must not let
// a value smuggle in a header of its own.
func TestComposeWritesThreadingHeadersAndRefusesInjection(t *testing.T) {
	s := New(Config{Host: "relay.example:25", From: "hearth@works.example"})
	msg := s.compose("dana@example.com", "Re: Line 3", "On its way.", map[string]string{
		"Message-Id":  "<out@works.example>",
		"In-Reply-To": "<in@example.com>",
		"References":  "<first@example.com> <in@example.com>",
	})

	for _, want := range []string{
		"Message-Id: <out@works.example>",
		"In-Reply-To: <in@example.com>",
		"References: <first@example.com> <in@example.com>",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message is missing %q, so it will not thread", want)
		}
	}
	// A reply is not auto-generated. Marking it so is how an answer somebody
	// was waiting for gets filed as noise.
	if strings.Contains(msg, "Auto-Submitted") {
		t.Error("a reply was marked auto-generated")
	}
	// And a notification, which has no In-Reply-To, still is.
	plain := s.compose("dana@example.com", "Assigned to you", "A job.", nil)
	if !strings.Contains(plain, "Auto-Submitted: auto-generated") {
		t.Error("a notification is not marked auto-generated, so an out of " +
			"office will answer it")
	}

	// Header injection through a value. The defence is folding the value onto
	// one line, so the property to check is about lines rather than about the
	// substring: text reading "Bcc:" inside a header value is harmless, and a
	// line that starts with it is a header somebody else wrote.
	nasty := s.compose("dana@example.com",
		"Subject\r\nBcc: everyone@example.com", "Body.", map[string]string{
			"In-Reply-To": "id@x\r\nBcc: sneak@example.com",
		})
	headers, _, _ := strings.Cut(nasty, "\r\n\r\n")
	allowed := map[string]bool{
		"from": true, "to": true, "subject": true, "date": true,
		"mime-version": true, "content-type": true, "auto-submitted": true,
		"message-id": true, "in-reply-to": true, "references": true,
	}
	for _, line := range strings.Split(headers, "\r\n") {
		name, _, _ := strings.Cut(line, ":")
		if !allowed[strings.ToLower(strings.TrimSpace(name))] {
			t.Errorf("a value smuggled in a header line: %q", line)
		}
	}
	if strings.Count(nasty, "\r\n\r\n") != 1 {
		t.Error("a value ended the header block early, so the rest became body")
	}
}

// crlf makes a test message look like one off the wire. Writing \n in a Go
// literal and hoping is how a parser test passes on something no MTA sends.
func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}
