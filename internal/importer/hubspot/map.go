package hubspot

import (
	"crypto/sha1"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Object names, as HubSpot spells them in its API.
const (
	Companies = "companies"
	Contacts  = "contacts"
	Deals     = "deals"
	Notes     = "notes"
	Calls     = "calls"
)

// Source is what external_refs calls a record this importer made or adopted.
const Source = "hubspot"

// LegacySource is what the Twenty import called the same records. The first HubSpot import (a one-off tool that wrote into
// Twenty) gave every record an id derived from its HubSpot id, and Hearth's import of Twenty kept that id, so a record
// that came across then is found again from its HubSpot id alone.
const LegacySource = "twenty"

// legacyNamespace is the fixed namespace the first import derived its ids under. It must never change: it is what links a
// HubSpot record to the Hearth record it already is.
var legacyNamespace = [16]byte{
	0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}

// uuid5 is RFC 4122 section 4.3.
func uuid5(name string) string {
	h := sha1.New()
	h.Write(legacyNamespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// legacyKey is the external id the Twenty import left in Hearth for a HubSpot record: the kind Hearth's import used,
// then the id the first import derived from the HubSpot id.
func legacyKey(object, hubspotID string) string {
	switch object {
	case Companies:
		return "company:" + uuid5("hubspot:company:"+hubspotID)
	case Contacts:
		return "person:" + uuid5("hubspot:contact:"+hubspotID)
	case Deals:
		return "opportunity:" + uuid5("hubspot:deal:"+hubspotID)
	case Notes:
		return "note:" + uuid5("hubspot:notes:"+hubspotID)
	case Calls:
		return "call:" + uuid5("hubspot:calls:"+hubspotID)
	}
	return ""
}

// key is the external id this importer records.
func key(object, hubspotID string) string { return object + ":" + hubspotID }

// Property lists each object is read with.
var props = map[string][]string{
	Companies: {"name", "domain", "website", "linkedin_company_page", "address", "city", "state", "zip", "country",
		"annualrevenue", "hubspot_owner_id", "createdate"},
	Contacts: {"firstname", "lastname", "email", "phone", "mobilephone", "jobtitle", "hs_linkedin_url", "hubspot_owner_id",
		"createdate", PropOptOut, PropHardBounce, PropBadAddress},
	Deals: {"dealname", "amount", "closedate", "dealstage", "hubspot_owner_id", "createdate"},
	Notes: {"hs_note_body", "hs_timestamp"},
	Calls: {"hs_call_title", "hs_call_body", "hs_call_duration", "hs_call_direction", "hs_call_disposition", "hs_timestamp"},
}

var (
	htmlTag   = regexp.MustCompile(`<[^>]*>`)
	urlPrefix = regexp.MustCompile(`^(?i)https?://`)
	unnamed   = regexp.MustCompile(`^\(unnamed [0-9]+\)$`)
)

func plain(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n")
	s = htmlTag.ReplaceAllString(s, "")
	for _, r := range [][2]string{{"&amp;", "&"}, {"&nbsp;", " "}, {"&quot;", `"`}, {"&#39;", "'"}, {"&lt;", "<"}, {"&gt;", ">"}} {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	return strings.TrimSpace(s)
}

func firstLine(s string, max int) string {
	s = plain(s)
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "..."
	}
	return s
}

// domainOf reduces a web address to the bare host Hearth matches mail against.
func domainOf(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = urlPrefix.ReplaceAllString(v, "")
	if i := strings.IndexAny(v, "/?#"); i >= 0 {
		v = v[:i]
	}
	v = strings.TrimPrefix(v, "www.")
	return strings.Trim(v, ". ")
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var extMark = regexp.MustCompile(`(?i)\s*(ext\.?|extension|x|;|#|,).*$`)

// phoneOf turns what was typed in HubSpot's phone boxes into a number EmberOps will accept, the way the first import did:
// ten digits, or eleven starting with 1, are North American (+1), anything else of eight to fifteen digits is taken as
// written with its country code, and what is left (a note, a partial number) is dropped. An extension ("ext 224", ";1123")
// is cut off first, because EmberOps refuses a phone number that carries one and the contact then never reaches it.
func phoneOf(p, mobile string) string {
	for _, v := range []string{p, mobile} {
		main := extMark.ReplaceAllString(strings.TrimSpace(v), "")
		d := digits(main)
		switch {
		case len(d) == 10:
			return "+1" + d
		case len(d) == 11 && d[0] == '1':
			return "+" + d
		case len(d) >= 8 && len(d) <= 15:
			return "+" + d
		}
	}
	return ""
}

// samePhone says two numbers written differently are one number.
func samePhone(a, b string) bool {
	da, db := digits(a), digits(b)
	if da == "" || db == "" {
		return da == db
	}
	da, db = strings.TrimPrefix(da, "1"), strings.TrimPrefix(db, "1")
	return da == db
}

// centsOf reads a HubSpot amount ("1200000.5") as cents.
func centsOf(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return int64(f*100 + 0.5), true
}

// stageFor is how a HubSpot deal stage becomes one of Hearth's eight. It is the mapping the first import used, so a deal
// keeps the same stage it would have had. A label this does not know is new in the pipeline: it is a new deal, and the
// run reports the label so the map can be extended.
func stageFor(label string) (stage string, known bool) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "closed - won", "firm quote accepted":
		return "won", true
	case "waiting on po", "project launch":
		return "customer", true
	case "closed - lost", "rejected quote":
		return "lost", true
	case "no response":
		return "no_response", true
	case "proposal prepared", "proposal under review", "proposal updates", "firm quote issued":
		return "proposal", true
	case "discussion scheduled", "opportunity debrieffed", "engineering assigned":
		return "meeting", true
	case "project scope in progress", "scope document drafted":
		return "screening", true
	case "unclear", "no info on the records":
		return "new", true
	}
	return "new", false
}

func day(v string) string {
	t := parseTime(v)
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

func atTime(v string) time.Time { return parseTime(v) }
