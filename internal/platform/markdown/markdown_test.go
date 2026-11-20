package markdown_test

// The renderer, and mostly the things it must NOT do.
//
// Every one of these is a way markup executes something. They pass by
// construction rather than by enumeration: the input is escaped before
// anything looks at it, so there is no live markup to preserve by accident.

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Embernet-ai/hearth/internal/platform/markdown"
)

func render(src string) string { return markdown.Render(src, markdown.Options{}) }

func TestHeadingsListsAndEmphasis(t *testing.T) {
	got := render(`# Restart procedure

Isolate the drive **before** removing the guard.

- Lock off at the panel
- Verify dead
- Fit the bearing

1. Torque to 45 Nm
2. Refit the guard

> The spare is in the cage.

` + "```bash\nsystemctl restart conveyor\n```")

	for _, want := range []string{
		"<h1>Restart procedure</h1>",
		"<strong>before</strong>",
		"<ul>", "<li>Lock off at the panel</li>",
		"<ol>", "<li>Torque to 45 Nm</li>",
		"<blockquote>",
		`<pre><code class="lang-bash">systemctl restart conveyor</code></pre>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// The check the plan names. A script tag does not execute, and neither does
// anything else that has ever been used to make one.
func TestNothingExecutes(t *testing.T) {
	hostile := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<iframe src="https://evil.example"></iframe>`,
		`[click](javascript:alert(1))`,
		`[click](JaVaScRiPt:alert(1))`,
		`[click](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)`,
		`[click](vbscript:msgbox)`,
		`<a href="javascript:alert(1)">x</a>`,
		`<svg/onload=alert(1)>`,
		"`<script>alert(1)</script>`",
		`<style>body{display:none}</style>`,
		`<!-- --><script>alert(1)</script>`,
		`**<script>alert(1)</script>**`,
		`# <script>alert(1)</script>`,
	}

	// The assertion is about MARKUP, not about characters. "onerror=" appearing
	// as visible text is the correct outcome and says the escaping worked; the
	// same string inside a tag is the bug. So this reads the tags out of the
	// output and requires every one of them to be a tag this renderer writes,
	// which is an allowlist over what was produced rather than a denylist over
	// what somebody might type.
	allowed := map[string]bool{
		"p": true, "br": true, "h1": true, "h2": true, "h3": true, "h4": true,
		"h5": true, "h6": true, "hr": true, "ul": true, "ol": true, "li": true,
		"strong": true, "em": true, "code": true, "pre": true, "a": true,
		"blockquote": true, "input": true, "div": true,
	}

	for _, src := range hostile {
		got := render(src)
		for _, tag := range tagsIn(got) {
			if !allowed[tag] {
				t.Errorf("rendering %q emitted a <%s>:\n%s", src, tag, got)
			}
		}
		// And the other half: a permitted tag carrying an event handler or a
		// script URL. Checked only INSIDE real tags, because "onerror=" sitting
		// in escaped text is the correct outcome and says the escaping worked.
		for _, tag := range rawTags(got) {
			lower := strings.ToLower(tag)
			for _, forbidden := range []string{
				"javascript:", "data:", "vbscript:", "onerror", "onload", "onclick",
				`href="//`,
			} {
				if strings.Contains(lower, forbidden) {
					t.Errorf("rendering %q produced a live tag %q:\n%s", src, tag, got)
				}
			}
		}
	}

	// And it is visible rather than silently swallowed, so somebody who pasted
	// a snippet into a runbook can see it did not vanish.
	got := render(`<script>alert(1)</script>`)
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("the escaped text is not shown either:\n%s", got)
	}
}

// A protocol-relative address is a link to somewhere else entirely, and it
// looks like a local path.
func TestProtocolRelativeLinksAreNotLocal(t *testing.T) {
	got := render(`[click](//evil.example/steal)`)
	if strings.Contains(got, `href="//evil.example`) {
		t.Errorf("a protocol-relative link was treated as local:\n%s", got)
	}
}

func TestExternalLinksCarryNoreferrer(t *testing.T) {
	got := render(`[the manual](https://example.invalid/manual.pdf)`)
	if !strings.Contains(got, `rel="noreferrer noopener"`) {
		t.Errorf("an external link has no rel:\n%s", got)
	}
	// A local one does not need it, and adding target=_blank to internal
	// navigation is how you end up with fifteen tabs.
	local := render(`[the work order](/documents/abc)`)
	if strings.Contains(local, "noreferrer") {
		t.Errorf("a local link was treated as external:\n%s", local)
	}
}

func TestBareAddressesBecomeLinks(t *testing.T) {
	got := render(`See https://example.invalid/manual for the torque figures.`)
	if !strings.Contains(got, `<a href="https://example.invalid/manual"`) {
		t.Errorf("a pasted address did not become a link:\n%s", got)
	}
	// And not twice.
	if strings.Count(got, "<a ") != 1 {
		t.Errorf("the address was linked %d times:\n%s", strings.Count(got, "<a "), got)
	}
}

// A single newline is a line break. Somebody typing a parts list expects their
// lines to stay where they put them.
func TestSingleNewlinesAreLineBreaks(t *testing.T) {
	got := render("Bearing: 6205-2RS\nSeal: 25x40x7\nGrease: EP2")
	if strings.Count(got, "<br>") != 2 {
		t.Errorf("line breaks were collapsed:\n%s", got)
	}
}

// The embed. A paragraph that is only a link to something here becomes a card,
// with no syntax to learn.
func TestALoneEntityLinkBecomesACard(t *testing.T) {
	var gotKind, gotID string
	out := markdown.Render("/documents/11111111-2222-3333-4444-555555555555",
		markdown.Options{
			LinkEntity: func(kind, id string) string {
				gotKind, gotID = kind, id
				return `<div class="card">Conveyor, Line 3</div>`
			},
		})
	if gotKind != "documents" || gotID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("the embed hook saw %q %q", gotKind, gotID)
	}
	if !strings.Contains(out, "Conveyor, Line 3") {
		t.Errorf("the card was not rendered:\n%s", out)
	}

	// A link with words around it stays an ordinary link, because that is a
	// sentence rather than an embed.
	inline := markdown.Render("see /documents/11111111-2222-3333-4444-555555555555 for details",
		markdown.Options{LinkEntity: func(string, string) string { return "<div>card</div>" }})
	if strings.Contains(inline, "<div>card</div>") {
		t.Errorf("a link inside a sentence became a card:\n%s", inline)
	}
}

// An unmatched marker is text. An asterisk in the middle of a sentence is
// usually an asterisk, and swallowing the rest of the paragraph looking for a
// partner is the classic way a renderer eats somebody's document.
func TestUnmatchedMarkersAreLeftAlone(t *testing.T) {
	got := render("Torque to 45 Nm * see note")
	if strings.Contains(got, "<em>") {
		t.Errorf("an unmatched asterisk started emphasis:\n%s", got)
	}
	if !strings.Contains(got, "45 Nm * see note") {
		t.Errorf("the text was mangled:\n%s", got)
	}
}

// A checklist typed by hand renders as one, and the boxes are inert because
// the authoritative list lives in the database.
func TestTaskListsRenderAsCheckboxes(t *testing.T) {
	got := render("- [x] Lock off\n- [ ] Verify dead")
	if !strings.Contains(got, `<input type="checkbox" disabled checked>`) {
		t.Errorf("a completed item did not render checked:\n%s", got)
	}
	if !strings.Contains(got, `<input type="checkbox" disabled>`) {
		t.Errorf("an open item did not render:\n%s", got)
	}
}

// An empty pair of markers must not spin.
func TestDegenerateInputTerminates(t *testing.T) {
	for _, src := range []string{"****", "____", "``", "[](", "**", "> ", "#", "```"} {
		done := make(chan string, 1)
		go func() { done <- render(src) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("rendering %q did not terminate", src)
		}
	}
}

// rawTags returns every real tag in the output, opening angle bracket to
// closing one. A tag is only a tag when the bracket is literal, which after
// escaping is true of nothing this renderer did not write itself.
func rawTags(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			break
		}
		out = append(out, s[i:i+end+1])
		i += end
	}
	return out
}

// tagsIn reports the name of every HTML tag in the output.
//
// Deliberately naive: it looks for a literal less-than followed by a letter,
// which is precisely what escaping is supposed to make impossible for anything
// the renderer did not write itself.
func tagsIn(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		j := i + 1
		if j < len(s) && s[j] == '/' {
			j++
		}
		start := j
		for j < len(s) && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z') ||
			(s[j] >= '0' && s[j] <= '9')) {
			j++
		}
		if j > start {
			out = append(out, strings.ToLower(s[start:j]))
		}
	}
	return out
}

// A YouTube link on its own line becomes the video, in every shape people
// paste; anything that is not exactly a YouTube id is left as text or a link.
func TestAYouTubeLinkOnItsOwnLineEmbeds(t *testing.T) {
	const want = `src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"`
	for _, link := range []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com/watch?v=dQw4w9WgXcQ&t=42s",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?si=abc",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ",
	} {
		if out := markdown.Render(link, markdown.Options{}); !strings.Contains(out, want) {
			t.Errorf("%s did not embed: %s", link, out)
		}
	}
	for _, link := range []string{
		"https://youtu.be/short",                          // not an id
		`https://youtu.be/dQw4w9WgXc"><script>x</script>`, // injection attempt
		"https://evil.example/watch?v=dQw4w9WgXcQ",        // wrong site
		"javascript:alert(1)//youtu.be/dQw4w9WgXcQ",       // wrong scheme
		"watch this: https://youtu.be/dQw4w9WgXcQ",        // inside a sentence
	} {
		out := markdown.Render(link, markdown.Options{})
		if strings.Contains(out, "<iframe") {
			t.Errorf("%q embedded but must not: %s", link, out)
		}
		if strings.Contains(out, "<script>") {
			t.Errorf("%q let markup through: %s", link, out)
		}
	}
}

// The embed carries an "Open larger" button whose popup exists on the page,
// and two copies of the same video never share a popup.
func TestAYouTubeEmbedOpensLargerInItsOwnPopup(t *testing.T) {
	out := markdown.Render("https://youtu.be/dQw4w9WgXcQ\n\nhttps://youtu.be/dQw4w9WgXcQ", markdown.Options{})
	targets := regexp.MustCompile(`popovertarget="(vp-[0-9a-f]+)">Open larger`).FindAllStringSubmatch(out, -1)
	if len(targets) != 2 {
		t.Fatalf("want two Open larger buttons, got %d: %s", len(targets), out)
	}
	if targets[0][1] == targets[1][1] {
		t.Error("two copies of the same video share one popup")
	}
	for _, m := range targets {
		if !strings.Contains(out, `id="`+m[1]+`" popover="manual"`) {
			t.Errorf("button points at %s but no such popup is on the page", m[1])
		}
	}
}

// Nobody leaves blank lines around a link they paste under a heading line,
// so a YouTube link alone on its line plays wherever it sits, and the text
// around it stays text.
func TestAYouTubeLinkPlaysWithoutBlankLinesAroundIt(t *testing.T) {
	got := markdown.Render("Intro video:\nhttps://youtu.be/dQw4w9WgXcQ\nEnd", markdown.Options{})
	if strings.Count(got, `<div class="video">`) != 1 {
		t.Fatalf("the link did not play:\n%s", got)
	}
	if !strings.Contains(got, "<p>Intro video:</p>") || !strings.Contains(got, "<p>End</p>") {
		t.Fatalf("the text around the video was lost:\n%s", got)
	}
}

// An underscore in an address is part of the address. Emphasis used to read
// the one in a_b plus the one in target="_blank" as a pair and put <em> tags
// inside the attributes, which broke every such link on the page.
func TestAnUnderscoreInALinkIsNotItalics(t *testing.T) {
	for src, want := range map[string]string{
		"see https://example.com/a_b here":                 `<p>see <a href="https://example.com/a_b" rel="noreferrer noopener" target="_blank">https://example.com/a_b</a> here</p>`,
		"[the *doc*](https://x.com/my_file_v2) and _this_": `<p><a href="https://x.com/my_file_v2" rel="noreferrer noopener" target="_blank">the <em>doc</em></a> and <em>this</em></p>`,
	} {
		if got := strings.TrimSpace(markdown.Render(src, markdown.Options{})); got != want {
			t.Errorf("%q\n got %s\nwant %s", src, got, want)
		}
	}
}

// A backslash makes the next formatting character literal -- what the editor
// writes when somebody types an asterisk -- and nothing else: every other
// backslash, a Windows path's included, stays exactly where it was.
func TestABackslashMakesFormattingLiteral(t *testing.T) {
	for src, want := range map[string]string{
		`5\*3 and 2\*4`:            "<p>5*3 and 2*4</p>",
		`file\_name\_here`:         "<p>file_name_here</p>",
		`\# not a heading`:         "<p># not a heading</p>",
		`\- not a list`:            "<p>- not a list</p>",
		`1\. not a list`:           "<p>1. not a list</p>",
		`\> not a quote`:           "<p>&gt; not a quote</p>",
		`\[not\] a link`:           "<p>[not] a link</p>",
		`C:\Users\x and \\srv\shr`: `<p>C:\Users\x and \\srv\shr</p>`,
		"`a\\*b` stays":            "<p><code>a\\*b</code> stays</p>",
		`**bold** \*not\*`:         "<p><strong>bold</strong> *not*</p>",
	} {
		if got := strings.TrimSpace(markdown.Render(src, markdown.Options{})); got != want {
			t.Errorf("%q\n got %s\nwant %s", src, got, want)
		}
	}
}
