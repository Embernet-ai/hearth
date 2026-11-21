package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The playground's guard, checked against every route the server registers
// rather than a hand-picked few, so a door added next year is checked too.
//
// Three things are asserted:
//   - every route that reaches outside Hearth, or creates something real, is
//     refused inside a playground (the explicit list below);
//   - any write route whose NAME suggests it does (token, webhook, mail,
//     google, import, password, ...) is refused too, unless it is on the short
//     list of ones looked at and judged safe -- so somebody adding a new
//     "send" route has to make that decision rather than miss it;
//   - ordinary work stays allowed, or the playground teaches nothing.
func TestThePlaygroundGuardCoversEveryRouteThatReachesOutside(t *testing.T) {
	src, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatal(err)
	}
	route := regexp.MustCompile(`HandleFunc\("(POST|PUT|PATCH|DELETE) ([^"]+)"`)
	routes := route.FindAllStringSubmatch(string(src), -1)
	if len(routes) < 50 {
		t.Fatalf("found only %d write routes in web.go; the route pattern has stopped matching", len(routes))
	}
	sample := regexp.MustCompile(`\{[^}]+\}`)

	// Must be refused. Prefixes of the registered pattern.
	mustBlock := []string{
		"/integrations/tokens", "/integrations/webhooks", "/integrations/google/",
		"/integrations/provisioning", "/agents", "/settings/mail", "/calendar/feed",
		"/import/", "/rules", "/account/password", "/documents/{id}/reply",
		"/people/{id}/capability", "/people/{id}/password-link",
		"/settings/connectors/", "/cloud/", "/mailbox/",
	}
	// Names that smell of the outside world. A write route containing one must
	// be blocked, or be listed in judgedSafe with the reason it is.
	risky := regexp.MustCompile(`(?i)token|webhook|mail|google|import|password|provision|scim|invite|reply|capabilit|feed|agent|sync|export|smtp|sms|send`)
	judgedSafe := map[string]string{
		"/inbound/mail":          "the MTA delivering mail in, on a machine key with no session; a playground cannot own an inbound address (/settings/mail is blocked)",
		"/time/imported/apply":   "moves imported hours between people inside the same organisation",
		"/time/imported/{entry}": "moves one imported entry inside the same organisation",
		"/offline/sync":          "a phone replaying offline work; offline accepts only photo, time, note and checklist tick, all internal",
		"/channels/{id}/invite":  "adds a colleague to a channel; sends nothing",
		"/crm/import":            "reads a CSV the person uploads and creates companies, contacts and deals inside the same organisation; it fetches and sends nothing",
		"/set-password":          "outside the session wall, so no playground can reach it; it redeems one single-use token and touches only the account it was issued for",
	}

	checked := 0
	for _, m := range routes {
		method, pattern := m[1], m[2]
		path := sample.ReplaceAllString(pattern, "x")
		blocked := playgroundBlocks(method, path)

		for _, prefix := range mustBlock {
			if strings.HasPrefix(pattern, prefix) && !blocked {
				t.Errorf("%s %s reaches outside Hearth and is NOT refused in a playground", method, pattern)
			}
		}
		if risky.MatchString(pattern) && !blocked {
			if _, ok := judgedSafe[pattern]; !ok {
				t.Errorf("%s %s looks like it reaches outside Hearth and is allowed in a playground.\n"+
					"Either block it (playgroundBlocked in tutorialpage.go) or, if it is safe, add it to judgedSafe here with the reason.",
					method, pattern)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no routes checked")
	}

	// Adding a person is a real login: blocked. Everything people practise on
	// stays open.
	if !playgroundBlocks("POST", "/people") {
		t.Error("adding a person (a real login) is allowed in a playground")
	}
	for _, p := range []string{
		"/documents", "/documents/x/comments", "/documents/x/status", "/documents/x/checklist/x",
		"/people/x/departments", "/people/x/grant-space", "/people/x/manages", "/time-off",
		"/tutorial/leave", "/tutorial/reset", "/account/avatar",
	} {
		if playgroundBlocks("POST", p) {
			t.Errorf("POST %s is refused in a playground, but it is ordinary work the lessons need", p)
		}
	}
	// Reading is never refused: the guard is about what goes out.
	if playgroundBlocks("GET", "/integrations/tokens") {
		t.Error("a GET is refused; only writes should be")
	}
}
