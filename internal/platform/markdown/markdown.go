// Package markdown renders a safe subset of Markdown to HTML.
//
// # Why not a library, and why not a sanitiser
//
// The usual shape is a full CommonMark renderer followed by an HTML sanitiser,
// and the sanitiser is the part that goes wrong. It has to enumerate every tag,
// attribute, protocol, and encoding trick that could execute something, and it
// is wrong the day a browser adds a feature nobody has heard of yet.
//
// This does it the other way round. Every character of input is HTML-escaped
// first, and the only tags in the output are ones this file wrote. There is no
// passthrough for raw HTML at any point, so "a script tag does not execute" is
// not a rule that has to be enforced, it is a property of the design: a script
// tag in the source comes out as visible text saying &lt;script&gt;.
//
// The cost is that raw HTML in a document does not work. For a maintenance
// runbook that is not a cost.
package markdown

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"net/url"
	"strings"
)

// Options tunes the render.
type Options struct {
	// LinkEntity is called for a link that points at something in this
	// installation, so a runbook can show a live card instead of a bare URL.
	// It returns the HTML for the card, or "" to render an ordinary link.
	LinkEntity func(kind, id string) string
}

// Render turns Markdown into HTML.
func Render(src string, opts Options) string {
	var out strings.Builder
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")

	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			i++

		// A fenced code block. Its contents are escaped and never inspected
		// for anything else, which is what makes a fence a reliable way to
		// show something that would otherwise be interpreted.
		case strings.HasPrefix(trimmed, "```"):
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			i++
			var code []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			if i < len(lines) {
				i++ // the closing fence
			}
			class := ""
			if lang != "" {
				class = fmt.Sprintf(` class="lang-%s"`, html.EscapeString(slug(lang)))
			}
			fmt.Fprintf(&out, "<pre><code%s>%s</code></pre>\n",
				class, html.EscapeString(strings.Join(code, "\n")))

		case strings.HasPrefix(trimmed, "#"):
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			if level > 6 || level >= len(trimmed) || trimmed[level] != ' ' {
				// Not a heading, just text that starts with a hash.
				i += writeParagraph(&out, lines, i, opts)
				continue
			}
			text := strings.TrimSpace(trimmed[level:])
			fmt.Fprintf(&out, "<h%d>%s</h%d>\n", level, inline(text, opts), level)
			i++

		case trimmed == "---" || trimmed == "***" || trimmed == "___":
			out.WriteString("<hr>\n")
			i++

		case strings.HasPrefix(trimmed, "> "):
			var quote []string
			for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "> ") {
				quote = append(quote, strings.TrimPrefix(strings.TrimSpace(lines[i]), "> "))
				i++
			}
			// Recursive, so a quote can hold a list or a heading. Bounded by
			// the fact that the quote markers are stripped each time, so it
			// cannot recurse on the same input.
			fmt.Fprintf(&out, "<blockquote>%s</blockquote>\n",
				Render(strings.Join(quote, "\n"), opts))

		case isBullet(trimmed):
			i += writeList(&out, lines, i, opts, false)

		case isOrdered(trimmed):
			i += writeList(&out, lines, i, opts, true)

		default:
			i += writeParagraph(&out, lines, i, opts)
		}
	}
	return out.String()
}

func isBullet(s string) bool {
	return strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") || strings.HasPrefix(s, "+ ")
}

func isOrdered(s string) bool {
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	return digits > 0 && digits+1 < len(s) && (s[digits] == '.' || s[digits] == ')') && s[digits+1] == ' '
}

// writeList emits one list and reports how many lines it consumed.
func writeList(out *strings.Builder, lines []string, start int, opts Options, ordered bool) int {
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	fmt.Fprintf(out, "<%s>\n", tag)

	i := start
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if ordered && !isOrdered(trimmed) {
			break
		}
		if !ordered && !isBullet(trimmed) {
			break
		}
		text := trimmed
		if ordered {
			text = strings.TrimSpace(text[strings.IndexAny(text, ".)")+1:])
		} else {
			text = strings.TrimSpace(text[2:])
		}
		// A task list item, which is what a checklist looks like when somebody
		// types one by hand. Rendered as a real checkbox, disabled: the
		// authoritative checklist is the one in the database, and a control
		// that looks live and does nothing is worse than one that looks inert.
		if done, rest, ok := taskItem(text); ok {
			checked := ""
			if done {
				checked = " checked"
			}
			fmt.Fprintf(out, "<li class=\"task\"><input type=\"checkbox\" disabled%s> %s</li>\n",
				checked, inline(rest, opts))
		} else {
			fmt.Fprintf(out, "<li>%s</li>\n", inline(text, opts))
		}
		i++
	}
	fmt.Fprintf(out, "</%s>\n", tag)
	return i - start
}

func taskItem(s string) (done bool, rest string, ok bool) {
	switch {
	case strings.HasPrefix(s, "[ ] "):
		return false, s[4:], true
	case strings.HasPrefix(s, "[x] "), strings.HasPrefix(s, "[X] "):
		return true, s[4:], true
	}
	return false, s, false
}

// writeParagraph gathers consecutive non-blank lines and reports how many it
// consumed.
func writeParagraph(out *strings.Builder, lines []string, start int, opts Options) int {
	i := start
	var para []string
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "> ") ||
			isBullet(trimmed) || isOrdered(trimmed) {
			break
		}
		// A YouTube link on a line of its own stands alone even without blank
		// lines around it ("Intro video:" then the link), so it still plays:
		// the paragraph so far ends above it, and it is its own below.
		if _, ok := youtubeID(trimmed); ok {
			if len(para) == 0 {
				para = append(para, trimmed)
				i++
			}
			break
		}
		para = append(para, trimmed)
		i++
	}
	if len(para) == 0 {
		return 1
	}

	// A paragraph that is nothing but a YouTube link becomes the video, for the
	// same reason as the card below: pasting the address is what somebody does
	// anyway. Only the video id reaches the page, and only after youtubeID has
	// checked it is eleven safe characters, so nothing else in the link can.
	// youtube-nocookie.com is the one frame source the Content-Security-Policy
	// allows (httpx/security.go).
	if len(para) == 1 {
		if id, ok := youtubeID(para[0]); ok {
			out.WriteString(youtubeEmbed(id))
			return i - start
		}
	}

	// A paragraph that is nothing but a link to something here becomes a card.
	// That is the embed: a runbook naming an asset shows the asset rather than
	// a URL, and it needs no syntax because pasting the address is what
	// somebody does anyway.
	if len(para) == 1 && opts.LinkEntity != nil {
		if kind, id, ok := entityRef(para[0]); ok {
			if card := opts.LinkEntity(kind, id); card != "" {
				out.WriteString(card)
				return i - start
			}
		}
	}

	// A single newline inside a paragraph is a line break rather than a space.
	// Somebody typing an address or a parts list expects their lines to stay
	// where they put them, and the alternative surprises everybody who is not
	// already a Markdown user.
	fmt.Fprintf(out, "<p>%s</p>\n", inline(strings.Join(para, "\n"), opts))
	return i - start
}

// entityRef recognises an address inside this installation.
func entityRef(s string) (kind, id string, ok bool) {
	s = strings.TrimSpace(s)
	// Both the bare path and a full address, because people paste both.
	for _, prefix := range []string{"/documents/", "/channels/"} {
		if idx := strings.Index(s, prefix); idx >= 0 {
			rest := s[idx+len(prefix):]
			if len(rest) >= 36 && isUUID(rest[:36]) && len(strings.TrimSpace(rest[36:])) == 0 {
				return strings.Trim(prefix, "/"), rest[:36], true
			}
		}
	}
	return "", "", false
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

// inline handles emphasis, code spans, and links.
//
// The input is escaped FIRST and the markers are matched against the escaped
// text. That ordering is the whole safety argument: by the time this function
// decides anything, there is no live markup left to preserve by accident.
func inline(s string, opts Options) string {
	escaped := html.EscapeString(protectEscapes(s))

	// Code spans first, and their contents are not processed further. A
	// backtick is how somebody shows an asterisk without it becoming italics.
	var b strings.Builder
	for {
		start := strings.Index(escaped, "`")
		if start < 0 {
			break
		}
		end := strings.Index(escaped[start+1:], "`")
		if end < 0 {
			break
		}
		end += start + 1
		b.WriteString(textEscapes.Replace(emphasis(linkify(escaped[:start], opts))))
		b.WriteString("<code>" + codeEscapes.Replace(escaped[start+1:end]) + "</code>")
		escaped = escaped[end+1:]
	}
	b.WriteString(textEscapes.Replace(emphasis(linkify(escaped, opts))))

	// A single newline is a line break. See writeParagraph.
	return strings.ReplaceAll(b.String(), "\n", "<br>\n")
}

// escapable is what a backslash makes literal: \* is an asterisk, not the
// start of italics. The rich-text editor writes these (editor/src/hearth-md.js
// keeps the same list) when somebody types a character that would otherwise
// be read as formatting. Deliberately not "any punctuation", and not the
// backslash itself: runbooks full of \\server\share and C:\Users paths were
// written for this renderer, and must keep every backslash they have.
const escapable = "`*_[]#>+-.)"

// escapeBase is where escaped characters hide while the rest of inline
// formatting runs: one private-use rune per character, which no formatting
// rule matches and html.EscapeString leaves alone.
const escapeBase rune = 0xE000

func protectEscapes(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if k := strings.IndexByte(escapable, s[i+1]); k >= 0 {
				b.WriteRune(escapeBase + rune(k))
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// shield hides the emphasis markers in something that must come out exactly
// as written -- an address, an attribute -- the same way an escaped character
// is hidden, so emphasis cannot read the underscore in a_b.html or in
// target="_blank" as the start of italics. textEscapes puts them back.
func shield(s string) string { return emphasisShield.Replace(s) }

var emphasisShield = strings.NewReplacer(
	"*", string(escapeBase+rune(strings.IndexByte(escapable, '*'))),
	"_", string(escapeBase+rune(strings.IndexByte(escapable, '_'))),
)

// textEscapes puts the hidden characters back as themselves; codeEscapes
// puts them back with their backslash, because inside a code span nothing is
// interpreted. HTML-escaped either way, as the text around them already is.
var textEscapes, codeEscapes = escapeReplacer(false), escapeReplacer(true)

func escapeReplacer(inCode bool) *strings.Replacer {
	var pairs []string
	for k := 0; k < len(escapable); k++ {
		c := html.EscapeString(escapable[k : k+1])
		if inCode {
			c = `\` + c
		}
		pairs = append(pairs, string(escapeBase+rune(k)), c)
	}
	return strings.NewReplacer(pairs...)
}

// emphasis handles **bold** and *italic*, in that order so the first does not
// eat the second's markers.
func emphasis(s string) string {
	s = pairs(s, "**", "<strong>", "</strong>")
	s = pairs(s, "__", "<strong>", "</strong>")
	s = pairs(s, "*", "<em>", "</em>")
	s = pairs(s, "_", "<em>", "</em>")
	return s
}

// pairs replaces balanced marker pairs. An unmatched marker is left as text,
// because an asterisk in the middle of a sentence is usually an asterisk.
func pairs(s, marker, open, close string) string {
	var b strings.Builder
	for {
		start := strings.Index(s, marker)
		if start < 0 {
			break
		}
		rest := s[start+len(marker):]
		end := strings.Index(rest, marker)
		if end < 0 {
			break
		}
		if end == 0 {
			// An empty pair. Emit one marker and carry on, or this loops.
			b.WriteString(s[:start+len(marker)])
			s = s[start+len(marker):]
			continue
		}
		b.WriteString(s[:start])
		b.WriteString(open + rest[:end] + close)
		s = rest[end+len(marker):]
	}
	b.WriteString(s)
	return b.String()
}

// linkify turns [text](url) and bare addresses into anchors.
//
// Only http, https, and paths inside this installation. A javascript: or data:
// URL in a link is the one remaining way markup could execute something, and
// the allowlist is what closes it. Anything else renders as plain text, so the
// person who wrote it can see what happened.
func linkify(s string, opts Options) string {
	var b strings.Builder
	for {
		open := strings.Index(s, "[")
		if open < 0 {
			break
		}
		closeBracket := strings.Index(s[open:], "](")
		if closeBracket < 0 {
			break
		}
		closeBracket += open
		closeParen := strings.Index(s[closeBracket:], ")")
		if closeParen < 0 {
			break
		}
		closeParen += closeBracket

		text := s[open+1 : closeBracket]
		href := s[closeBracket+2 : closeParen]
		b.WriteString(s[:open])
		if safeURL(href) {
			fmt.Fprintf(&b, `<a href="%s"%s>%s</a>`, shield(href), shield(relFor(href)), text)
		} else {
			// Shown rather than dropped, so somebody who pasted a bad scheme
			// can see why it is not a link.
			b.WriteString(text + " (" + href + ")")
		}
		s = s[closeParen+1:]
	}
	b.WriteString(s)
	return autolink(b.String())
}

// autolink turns a bare address into a link, because people paste them.
func autolink(s string) string {
	var b strings.Builder
	for {
		idx := strings.Index(s, "https://")
		alt := strings.Index(s, "http://")
		if idx < 0 || (alt >= 0 && alt < idx) {
			idx = alt
		}
		if idx < 0 {
			break
		}
		// Already inside an anchor this function or linkify wrote.
		if inAnchor(s[:idx]) {
			b.WriteString(s[:idx+8])
			s = s[idx+8:]
			continue
		}
		end := idx
		for end < len(s) && !isURLEnd(s[end]) {
			end++
		}
		url := strings.TrimRight(s[idx:end], ".,;:)")
		b.WriteString(s[:idx])
		fmt.Fprintf(&b, `<a href="%s"%s>%s</a>`, shield(url), shield(relFor(url)), shield(url))
		s = s[idx+len(url):]
	}
	b.WriteString(s)
	return b.String()
}

// inAnchor reports whether the text ends inside an open anchor tag, so an
// address that has already been linked is not linked again.
func inAnchor(before string) bool {
	lastOpen := strings.LastIndex(before, "<a ")
	lastClose := strings.LastIndex(before, "</a>")
	return lastOpen > lastClose
}

func isURLEnd(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '"', '<', '>':
		return true
	}
	return false
}

// safeURL is an allowlist, not a denylist. Enumerating the schemes that are
// dangerous is a game nobody wins.
func safeURL(href string) bool {
	lower := strings.ToLower(strings.TrimSpace(href))
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return true
	case strings.HasPrefix(lower, "mailto:"):
		return true
	case strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//"):
		// A path inside this installation. The double-slash exclusion matters:
		// //evil.example is a protocol-relative address to somewhere else.
		return true
	}
	return false
}

// relFor adds noreferrer and noopener to anything leaving this installation.
func relFor(href string) string {
	if strings.HasPrefix(href, "/") {
		return ""
	}
	return ` rel="noreferrer noopener" target="_blank"`
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '+' || r == '#' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// youtubeID recognises a YouTube address in any of the shapes people paste --
// watch?v=, youtu.be/, /embed/, /shorts/, /live/ -- and returns the video id.
// The id is the only part used, and only if it is exactly the eleven
// characters YouTube ids are made of.
func youtubeID(s string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	var id string
	switch host {
	case "youtu.be":
		id = strings.Trim(u.Path, "/")
	case "youtube.com", "youtube-nocookie.com":
		if u.Path == "/watch" {
			id = u.Query().Get("v")
		} else {
			for _, p := range []string{"/embed/", "/shorts/", "/live/"} {
				if strings.HasPrefix(u.Path, p) {
					id = strings.Trim(strings.TrimPrefix(u.Path, p), "/")
				}
			}
		}
	default:
		return "", false
	}
	if len(id) != 11 {
		return "", false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return "", false
		}
	}
	return id, true
}

// youtubeEmbed is the player for one video id (already checked by youtubeID),
// plus an "Open larger" button and a popup holding a second, bigger player
// that can be dragged by its bar and resized from its corner -- for somebody
// who wants it bigger without going full screen. The popup's own player only
// loads once opened (loading="lazy" inside a hidden popover). The popup is
// "manual", so it stays up while somebody keeps working until they close it.
func youtubeEmbed(id string) string {
	src := "https://www.youtube-nocookie.com/embed/" + id
	const attrs = `title="YouTube video" loading="lazy" allow="encrypted-media; picture-in-picture; fullscreen" allowfullscreen referrerpolicy="strict-origin-when-cross-origin"`
	pop := "vp-" + popID()
	return fmt.Sprintf(`<div class="video"><iframe src="%s" %s></iframe></div>`+
		`<button type="button" class="ghost small videopop-open" popovertarget="%s">Open larger &#8599;</button>`+
		`<div id="%s" popover="manual" class="popover videopop">`+
		`<div class="videopop-bar" data-drag-handle title="Drag to move"><span class="muted small">Drag to move &middot; resize from the corner</span>`+
		`<button type="button" class="ghost small" popovertarget="%s" popovertargetaction="hide">Close</button></div>`+
		`<div class="videopop-frame"><iframe src="%s" %s></iframe></div></div>`+"\n",
		src, attrs, pop, pop, pop, src, attrs)
}

// popID is a short random id, so two copies of the same video on one page
// (a task's details and a comment, say) never open each other's popup.
func popID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
