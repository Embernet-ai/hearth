package search

// Pulling text out of an attachment.
//
// Two formats, and honesty about the rest. Plain text of any flavour, and PDFs
// that were generated rather than scanned. A scanned PDF is a picture of words
// and needs OCR, which is a different product; it is marked unsupported rather
// than indexed as an empty document, so the difference between "no text in it"
// and "we never looked" stays visible in the database.
//
// No dependency for the PDF side. A content stream is a compressed blob of
// PostScript-ish operators, and the four that place text are enough for the
// manuals and datasheets a maintenance team actually attaches. Adding a library
// to read the other ninety percent of the specification would be a library
// carried for a feature nobody here has.

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

// Extraction outcomes, matching the CHECK constraint on files.extraction_status.
const (
	StatusPending     = "pending"
	StatusDone        = "done"
	StatusUnsupported = "unsupported"
	StatusFailed      = "failed"
)

// maxExtracted bounds what is stored. A million-word document contributes
// nothing after the first few hundred thousand characters that a search would
// not already have matched, and the row has to fit in a page cache.
const maxExtracted = 512 * 1024

// Extract reads whatever text a file has in it.
//
// It returns the text and the status. An unsupported format is not an error:
// the file uploaded fine, it simply has no words in it that can be read, and
// treating that as a failure would fill the dead letter table with images.
func Extract(contentType, filename string, r io.Reader) (string, string, error) {
	switch {
	case isPlainText(contentType, filename):
		text, err := readPlain(r)
		if err != nil {
			return "", StatusFailed, err
		}
		return text, StatusDone, nil

	case isPDF(contentType, filename):
		raw, err := io.ReadAll(io.LimitReader(r, 128<<20))
		if err != nil {
			return "", StatusFailed, fmt.Errorf("search: read pdf: %w", err)
		}
		text := extractPDF(raw)
		if !looksLikeProse(text) {
			// Either a scan, or a font encoding this does not read. Both are
			// "there is no text here we can use", and neither is worth
			// retrying, so the status says so rather than the error.
			return "", StatusUnsupported, nil
		}
		return clamp(text), StatusDone, nil

	default:
		return "", StatusUnsupported, nil
	}
}

func isPDF(contentType, filename string) bool {
	return strings.Contains(contentType, "pdf") ||
		strings.HasSuffix(strings.ToLower(filename), ".pdf")
}

// isPlainText covers the formats whose bytes ARE their text. Deliberately
// generous with suffixes: a runbook saved as .md and a controller export saved
// as .csv are both things somebody will want to find by their contents, and
// neither arrives with a useful content type from a browser.
func isPlainText(contentType, filename string) bool {
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	switch contentType {
	case "application/json", "application/xml", "application/x-yaml", "application/yaml":
		return true
	}
	lower := strings.ToLower(filename)
	for _, suffix := range []string{
		".txt", ".md", ".csv", ".log", ".json", ".xml", ".yaml", ".yml",
		".ini", ".cfg", ".conf", ".sql", ".go", ".py", ".sh", ".st", ".scl",
	} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func readPlain(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxExtracted+1))
	if err != nil {
		return "", fmt.Errorf("search: read text: %w", err)
	}
	// A file that claims to be text and is full of zero bytes is a binary that
	// was misnamed. Indexing it puts noise in the dictionary for every search.
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", nil
	}
	return clamp(string(raw)), nil
}

func clamp(s string) string {
	if len(s) <= maxExtracted {
		return s
	}
	// Cut on a rune boundary, so the column never holds an invalid sequence.
	cut := maxExtracted
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// looksLikeProse decides whether what came out of a PDF is words or noise.
//
// A CID font with a custom encoding produces plausible-looking bytes that are
// not letters in any language, and indexing them adds thousands of junk terms
// to the dictionary that will never match a query and slow down every one. The
// test is crude on purpose: a majority of the characters have to be things
// people type.
func looksLikeProse(s string) bool {
	if len(strings.TrimSpace(s)) < 16 {
		return false
	}
	var readable, total int
	for _, r := range s {
		total++
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) ||
			unicode.IsPunct(r) {
			readable++
		}
		if total > 4000 {
			break
		}
	}
	return total > 0 && readable*100/total >= 80
}

// -----------------------------------------------------------------------------
// PDF
// -----------------------------------------------------------------------------

// extractPDF pulls the text-showing operators out of every content stream.
//
// The approach is a scan rather than a parse of the cross-reference table,
// because a damaged or linearised file still has readable streams in it and a
// strict parser refuses the whole document over a byte somebody's exporter got
// wrong. What matters here is getting words out of a manual, not validating it.
func extractPDF(raw []byte) string {
	var out strings.Builder
	rest := raw

	for {
		start := bytes.Index(rest, []byte("stream"))
		if start < 0 {
			break
		}
		// The dictionary immediately before the keyword says how it is encoded.
		header := rest[:start]
		dictStart := bytes.LastIndex(header, []byte("<<"))
		var dict []byte
		if dictStart >= 0 {
			dict = header[dictStart:]
		}

		body := rest[start+len("stream"):]
		// Skip the end-of-line after the keyword, which may be CRLF or LF.
		body = bytes.TrimLeft(body, "\r")
		if len(body) > 0 && body[0] == '\n' {
			body = body[1:]
		}
		end := bytes.Index(body, []byte("endstream"))
		if end < 0 {
			break
		}
		chunk := body[:end]
		rest = body[end+len("endstream"):]

		// Only Flate. The other filters are rare in files produced this decade,
		// and a partially decoded stream is worse than a skipped one because
		// its noise reaches the index.
		if !bytes.Contains(dict, []byte("/FlateDecode")) {
			continue
		}
		decoded, err := inflate(chunk)
		if err != nil {
			continue
		}
		out.WriteString(textFromContentStream(decoded))
		if out.Len() > maxExtracted {
			break
		}
	}
	return normaliseSpace(out.String())
}

func inflate(b []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	// Bounded, because a zip bomb in an attachment must not be a way to fill
	// memory on the worker.
	out, err := io.ReadAll(io.LimitReader(zr, 32<<20))
	if err != nil && len(out) == 0 {
		return nil, err
	}
	// A truncated stream still has usable text at the front, so partial output
	// is kept rather than discarded.
	return out, nil
}

// textFromContentStream reads the operators that put glyphs on a page.
//
//	(text) Tj        show a string
//	(text) '         next line, then show
//	(text) "         spacing, then show
//	[(a) -20 (b)] TJ show an array with kerning between the pieces
//
// Everything else is position, colour, and graphics, none of which is text.
func textFromContentStream(b []byte) string {
	var out strings.Builder
	i := 0
	for i < len(b) {
		switch b[i] {
		case '(':
			s, next := readLiteralString(b, i)
			i = next
			// Only keep it if a showing operator follows within a short reach.
			// A string that is part of a dictionary is not page text.
			if op, ok := nextOperator(b, i); ok && isShowOp(op) {
				out.WriteString(s)
				if op == "'" || op == `"` {
					out.WriteByte('\n')
				} else {
					out.WriteByte(' ')
				}
			}
		case '<':
			// A hex string, or the start of a dictionary.
			if i+1 < len(b) && b[i+1] == '<' {
				i += 2
				continue
			}
			s, next := readHexString(b, i)
			i = next
			if op, ok := nextOperator(b, i); ok && isShowOp(op) {
				out.WriteString(s)
				out.WriteByte(' ')
			}
		case 'T':
			// T* is an explicit new line.
			if i+1 < len(b) && b[i+1] == '*' {
				out.WriteByte('\n')
				i += 2
				continue
			}
			i++
		default:
			i++
		}
		if out.Len() > maxExtracted {
			break
		}
	}
	return out.String()
}

func isShowOp(op string) bool {
	switch op {
	case "Tj", "TJ", "'", `"`:
		return true
	}
	return false
}

// nextOperator looks ahead past whitespace, numbers, and array punctuation for
// the operator that consumes the string just read. Bounded, so a malformed
// stream cannot turn this into a scan of the whole file per string.
func nextOperator(b []byte, i int) (string, bool) {
	const reach = 64
	limit := min(i+reach, len(b))
	for j := i; j < limit; j++ {
		c := b[j]
		switch {
		case c == ' ' || c == '\n' || c == '\r' || c == '\t' ||
			c == ']' || c == '[' || c == '-' || c == '.' ||
			(c >= '0' && c <= '9'):
			continue
		case c == '(' || c == '<':
			// Another string in the same array. The operator is still coming.
			return "TJ", true
		case c == '\'' || c == '"':
			return string(c), true
		case c == 'T' && j+1 < len(b) && (b[j+1] == 'j' || b[j+1] == 'J'):
			return string(b[j : j+2]), true
		default:
			return "", false
		}
	}
	return "", false
}

// readLiteralString reads (…), honouring escapes and nesting.
func readLiteralString(b []byte, i int) (string, int) {
	var out strings.Builder
	depth := 0
	for ; i < len(b); i++ {
		c := b[i]
		switch c {
		case '\\':
			if i+1 >= len(b) {
				return out.String(), i + 1
			}
			i++
			switch e := b[i]; e {
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case 'b', 'f':
				out.WriteByte(' ')
			case '(', ')', '\\':
				out.WriteByte(e)
			case '\n':
				// A line continuation inside a string. Nothing is emitted.
			default:
				if e >= '0' && e <= '7' {
					// Up to three octal digits.
					digits := []byte{e}
					for len(digits) < 3 && i+1 < len(b) && b[i+1] >= '0' && b[i+1] <= '7' {
						i++
						digits = append(digits, b[i])
					}
					if n, err := strconv.ParseUint(string(digits), 8, 16); err == nil {
						out.WriteRune(rune(n))
					}
				} else {
					out.WriteByte(e)
				}
			}
		case '(':
			if depth > 0 {
				out.WriteByte(c)
			}
			depth++
		case ')':
			depth--
			if depth == 0 {
				return out.String(), i + 1
			}
			out.WriteByte(c)
		default:
			out.WriteByte(c)
		}
	}
	return out.String(), i
}

// readHexString reads <48656c6c6f>, which is how a PDF writes text that does
// not survive being a literal.
func readHexString(b []byte, i int) (string, int) {
	i++ // past '<'
	var digits []byte
	for ; i < len(b) && b[i] != '>'; i++ {
		c := b[i]
		if isHexDigit(c) {
			digits = append(digits, c)
		}
	}
	if i < len(b) {
		i++ // past '>'
	}
	if len(digits)%2 == 1 {
		// An odd count is padded with a zero, per the specification.
		digits = append(digits, '0')
	}
	var out strings.Builder
	for j := 0; j+1 < len(digits); j += 2 {
		n, err := strconv.ParseUint(string(digits[j:j+2]), 16, 8)
		if err != nil {
			continue
		}
		if n == 0 {
			// Two-byte encodings interleave zeros. Dropping them turns
			// UTF-16BE Latin text into something readable rather than into a
			// string full of NULs.
			continue
		}
		out.WriteByte(byte(n))
	}
	return out.String(), i
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// normaliseSpace collapses the runs of whitespace that a page layout produces,
// so the stored text reads as sentences and the index is not full of empties.
func normaliseSpace(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	lastSpace := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !lastSpace {
				out.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		// Control characters that survive a broken encoding.
		if r < 0x20 || r == 0x7f {
			continue
		}
		out.WriteRune(r)
		lastSpace = false
	}
	return strings.TrimSpace(out.String())
}
