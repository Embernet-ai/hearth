package crmsync

// An unchanged record is left alone.
//
// Every field Hearth owns is sent on each run, which made the hourly job rewrite every Customer, Contact, Lead and
// Opportunity whether anything had changed or not: each one got a new modified time and a new version in EmberOps,
// and a CRM of thousands of records would be thousands of writes an hour. Now the document the sync is about to send
// is held against what EmberOps has, and a record that already matches is reported as "same" and not sent.
//
// The comparison only ever decides to skip. Anything it cannot read the same way on both sides counts as different,
// so a doubt becomes a write, which is what the sync did before.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Embernet-ai/hearth/internal/emberops"
)

// stamp puts the two things that tie an EmberOps record back to the person who works it in Hearth: the link to its
// Hearth page, and who owns it there.
func (s *Syncer) stamp(doc map[string]any, page, id, owner string) {
	if s.hearthURL != "" {
		doc["hearth_url"] = s.hearthURL + "/" + page + "/" + id
	}
	if o := strings.TrimSpace(owner); o != "" {
		doc["hearth_owner"] = o
	}
}

// unsubscribe marks a Contact or Lead doc unsubscribed when Hearth holds the person's opt-out. It only ever sets the flag:
// a person not opted out sends nothing, so EmberOps' own value stands and the sync cannot take an unsubscribe back.
func unsubscribe(doc map[string]any, optOut bool) {
	if optOut {
		doc["unsubscribed"] = 1
	}
}

// same says whether EmberOps already holds every value in doc.
func (s *Syncer) same(ctx context.Context, doc map[string]any, have emberops.Row) bool {
	return sameDoc(doc, have, func(dt, hid string) string {
		row, ok, err := s.known(ctx, dt, hid)
		if err != nil || !ok {
			return ""
		}
		n, _ := row["name"].(string)
		return n
	})
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x), true
	case float64, int, int64, int32, bool:
		if b, ok := x.(bool); ok {
			if b {
				return "1", true
			}
			return "0", true
		}
		return fmt.Sprint(x), true
	case nil:
		return "", true
	}
	return "", false
}

// sameDoc is the comparison. resolve turns a "hearth:<id>" reference in a link into the name EmberOps gave that
// record, or "" when it has none.
func sameDoc(doc map[string]any, have emberops.Row, resolve func(dt, hid string) string) bool {
	for k, want := range doc {
		if k == "doctype" || k == "hearth_id" {
			continue
		}
		switch k {
		case "_tags":
			w := lowerSet(want)
			if len(w) != len(lowerSet(tagList(have))) {
				return false
			}
			for t := range lowerSet(tagList(have)) {
				if !w[t] {
					return false
				}
			}
			continue
		case "email_ids", "phone_nos", "links":
			if !sameRows(k, want, have[k], resolve) {
				return false
			}
			continue
		}
		w, ok := scalar(want)
		if !ok {
			return false
		}
		h, ok := scalar(have[k])
		if !ok || w != h {
			return false
		}
	}
	return true
}

func tagList(r emberops.Row) []string { return tagsOf(r) }

func lowerSet(v any) map[string]bool {
	out := map[string]bool{}
	switch x := v.(type) {
	case []string:
		for _, t := range x {
			out[strings.ToLower(strings.TrimSpace(t))] = true
		}
	}
	return out
}

func rowsOf(v any) []map[string]any {
	var out []map[string]any
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
	case []map[string]any:
		out = x
	}
	return out
}

// sameRows holds a child table against the one EmberOps has: every row Hearth sends must be there with the same
// values, and there must be no others.
func sameRows(kind string, want, have any, resolve func(dt, hid string) string) bool {
	ws, hs := rowsOf(want), rowsOf(have)
	if len(ws) != len(hs) {
		return false
	}
	used := make([]bool, len(hs))
	for _, w := range ws {
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		found := false
		for i, h := range hs {
			if used[i] {
				continue
			}
			match := true
			for _, k := range keys {
				wv, ok := scalar(w[k])
				if !ok {
					match = false
					break
				}
				if kind == "links" && k == "link_name" {
					if rest, isRef := strings.CutPrefix(wv, "hearth:"); isRef {
						dt, _ := scalar(w["link_doctype"])
						wv = resolve(dt, rest)
						if wv == "" {
							match = false
							break
						}
					}
				}
				hv, ok := scalar(h[k])
				if !ok || hv != wv {
					match = false
					break
				}
			}
			if match {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
