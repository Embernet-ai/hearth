package mail

// Reading a message that arrived, as opposed to composing one that has not.
//
// net/mail, mime, and mime/multipart, all standard library. An email parser is
// a famously miserable thing to write and a famously miserable thing to depend
// on, and the standard library has already done the awful parts: folded
// headers, RFC 2047 encoded words, and quoted-printable.
//
// What is left is choosing which part of a multipart message is the message.
// That is a judgement rather than a specification, and it is made here in one
// place with the reasoning written down.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// maxBody bounds what is read out of one part. A message with a fifty megabyte
// text part is not a message, and reading it into memory to find out is how one
// sender takes the worker down.
const maxBody = 1 << 20

// maxAttachment bounds one attachment for the same reason. Anything larger
// belongs in the object store by a route that streams, which this is not.
const maxAttachment = 25 << 20

// Message is an arrived email, reduced to what the product does with it.
type Message struct {
	MessageID  string
	InReplyTo  string
	References []string
	From       string
	FromName   string
	To         []string
	Cc         []string
	Subject    string
	// Text is the body as plain text, with an HTML part converted when that is
	// all there was.
	Text        string
	Attachments []Attachment
	// Raw is the whole thing as it arrived, kept so a dispute about what was
	// sent is settled by reading it rather than by trusting this parser.
	Raw []byte
}

// Attachment is one file that came with a message.
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// ErrNoMessageID means the message has no Message-ID header.
//
// It is refused rather than given one, because that header is the idempotency
// key: inventing one turns a relay retry into a second work order, which is
// exactly the failure the whole path is built to avoid.
var ErrNoMessageID = errors.New("mail: the message has no Message-ID")

// Parse reads a raw RFC 5322 message.
func Parse(raw []byte) (*Message, error) {
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("mail: parse: %w", err)
	}

	dec := new(mime.WordDecoder)
	// A charset this build cannot convert is passed through as bytes rather
	// than failing the message. A subject in the wrong encoding is a subject
	// somebody can still read most of; a rejected message is work lost.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	header := func(name string) string {
		v, err := dec.DecodeHeader(m.Header.Get(name))
		if err != nil {
			return m.Header.Get(name)
		}
		return v
	}

	out := &Message{
		MessageID:  TrimMessageID(m.Header.Get("Message-Id")),
		InReplyTo:  TrimMessageID(m.Header.Get("In-Reply-To")),
		References: splitReferences(m.Header.Get("References")),
		Subject:    header("Subject"),
		Raw:        raw,
	}
	if out.MessageID == "" {
		return nil, ErrNoMessageID
	}
	if from, err := mail.ParseAddress(m.Header.Get("From")); err == nil {
		out.From = strings.ToLower(from.Address)
		out.FromName = from.Name
	} else {
		out.From = strings.ToLower(strings.TrimSpace(m.Header.Get("From")))
	}
	out.To = addresses(m.Header.Get("To"))
	out.Cc = addresses(m.Header.Get("Cc"))
	// Delivered-To and Envelope-To carry the address the MTA actually
	// delivered to, which is the only one that survives a message sent to a
	// list or blind copied to the inbound address.
	for _, h := range []string{"Delivered-To", "X-Original-To", "Envelope-To"} {
		out.To = append(out.To, addresses(m.Header.Get(h))...)
	}

	ct := m.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	if err := readPart(out, ct, m.Header.Get("Content-Transfer-Encoding"), "", m.Body); err != nil {
		return nil, err
	}
	out.Text = strings.TrimSpace(out.Text)
	return out, nil
}

// readPart walks one part, recursing into multipart containers.
func readPart(out *Message, contentType, encoding, disposition string, body io.Reader) error {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		// An unparseable Content-Type is treated as plain text rather than as
		// a failure. Something arrived, and refusing it loses the work.
		mediaType = "text/plain"
		params = map[string]string{}
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				// A truncated multipart still has whatever was read before the
				// break, and half a work order beats none.
				return nil
			}
			pct := part.Header.Get("Content-Type")
			if pct == "" {
				pct = "text/plain"
			}
			if err := readPart(out, pct, part.Header.Get("Content-Transfer-Encoding"),
				part.Header.Get("Content-Disposition"), part); err != nil {
				_ = part.Close()
				return err
			}
			_ = part.Close()
		}
	}

	decoded := decode(body, encoding)
	filename := attachmentName(disposition, params)

	switch {
	case filename != "":
		content, err := io.ReadAll(io.LimitReader(decoded, maxAttachment))
		if err != nil {
			return nil
		}
		out.Attachments = append(out.Attachments, Attachment{
			Filename: filename, ContentType: mediaType, Content: content,
		})
	case mediaType == "text/plain":
		b, _ := io.ReadAll(io.LimitReader(decoded, maxBody))
		out.Text += string(b)
	case mediaType == "text/html":
		// Only when there is no plain part. Almost every client sends both,
		// and taking the HTML when the text is there means a work order body
		// full of markup.
		if strings.TrimSpace(out.Text) == "" {
			b, _ := io.ReadAll(io.LimitReader(decoded, maxBody))
			out.Text += HTMLToText(string(b))
		}
	}
	return nil
}

func decode(r io.Reader, encoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	case "base64":
		// The decoder skips the line breaks MIME wraps base64 in.
		return base64.NewDecoder(base64.StdEncoding, r)
	default:
		return r
	}
}

// attachmentName is the filename when this part is a file.
//
// Both places it can live are checked, because a disposition with no filename
// and a type with one is common enough from scanners and phone cameras, which
// is exactly what arrives attached to a fault report.
func attachmentName(disposition string, typeParams map[string]string) string {
	if disposition != "" {
		if d, params, err := mime.ParseMediaType(disposition); err == nil {
			if name := params["filename"]; name != "" {
				return sanitiseFilename(name)
			}
			if d == "attachment" {
				return "attachment"
			}
		}
	}
	if name := typeParams["name"]; name != "" {
		return sanitiseFilename(name)
	}
	return ""
}

// sanitiseFilename keeps a name that came from outside from being a path.
func sanitiseFilename(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.LastIndexAny(v, `/\`); i >= 0 {
		v = v[i+1:]
	}
	v = strings.ReplaceAll(v, "..", "")
	if v == "" {
		return "attachment"
	}
	if len(v) > 120 {
		v = v[:120]
	}
	return v
}

// TrimMessageID strips the angle brackets a Message-ID is written in.
//
// Stored bare, so a comparison never fails on punctuation that some clients
// include and some do not.
func TrimMessageID(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "<")
	v = strings.TrimSuffix(v, ">")
	// Some clients fold a long References into In-Reply-To. Take the last one,
	// which is the message being answered.
	if fields := strings.Fields(v); len(fields) > 1 {
		v = strings.Trim(fields[len(fields)-1], "<>")
	}
	return strings.TrimSpace(v)
}

func splitReferences(v string) []string {
	var out []string
	for _, f := range strings.Fields(v) {
		if id := strings.Trim(f, "<>"); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func addresses(v string) []string {
	list, err := mail.ParseAddressList(v)
	if err != nil {
		if one, err2 := mail.ParseAddress(v); err2 == nil {
			return []string{strings.ToLower(one.Address)}
		}
		return nil
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, strings.ToLower(a.Address))
	}
	return out
}

// HTMLToText reduces an HTML part to something a person can read.
//
// Not a renderer and not trying to be. It drops script and style outright,
// turns block boundaries into newlines, unescapes the five entities that
// matter, and throws the rest of the tags away. The output goes into a work
// order body as plain text and is escaped on the way to the page like anything
// else somebody typed, so nothing here is a security control.
func HTMLToText(in string) string {
	var b strings.Builder
	b.Grow(len(in))

	lower := strings.ToLower(in)
	i := 0
	for i < len(in) {
		if in[i] != '<' {
			b.WriteByte(in[i])
			i++
			continue
		}
		// Elements whose contents are not text at all. Skipped whole, so a
		// stylesheet does not end up in the body of a work order.
		if next, skipped := skipElement(in, lower, i); skipped {
			i = next
			continue
		}
		end := strings.IndexByte(in[i:], '>')
		if end < 0 {
			break
		}
		name, _, _ := strings.Cut(
			strings.ToLower(strings.Trim(in[i+1:i+end], "/ \t\r\n")), " ")
		if blockTags[name] {
			b.WriteByte('\n')
		}
		i += end + 1
	}

	text := b.String()
	for _, e := range entities {
		text = strings.ReplaceAll(text, e.from, e.to)
	}
	// Collapse the runs of blank lines that dropping tags leaves behind.
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// skipElement jumps past an element whose contents must not be read, and
// reports whether it did.
func skipElement(in, lower string, at int) (int, bool) {
	for _, name := range []string{"script", "style", "head"} {
		if !strings.HasPrefix(lower[at:], "<"+name) {
			continue
		}
		rel := strings.Index(lower[at:], "</"+name)
		if rel < 0 {
			// Unterminated. The rest of the document is inside it, and
			// guessing otherwise puts CSS in somebody's fault report.
			return len(in), true
		}
		gt := strings.IndexByte(in[at+rel:], '>')
		if gt < 0 {
			return len(in), true
		}
		return at + rel + gt + 1, true
	}
	return at, false
}

var blockTags = map[string]bool{
	"p": true, "br": true, "div": true, "tr": true, "li": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"blockquote": true, "pre": true, "table": true, "ul": true, "ol": true,
}

// The handful that appear in real mail.
//
// A slice rather than a map, because the order is load-bearing: &amp; has to be
// replaced last or "&amp;lt;" becomes "<" instead of the "&lt;" that was
// written. Go randomises map iteration, so a map would get this wrong
// occasionally, which is the worst frequency for a bug to have.
var entities = []struct{ from, to string }{
	{"&nbsp;", " "}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`},
	{"&#39;", "'"}, {"&apos;", "'"}, {"&amp;", "&"},
}
