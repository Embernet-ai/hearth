package offline

// The two decisions this package makes with no database in front of it: what
// may be replayed at all, and whether a photograph arrived intact.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

const doc = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
const item = "9c858901-8a57-4791-81fe-4c455b099bc9"

func TestWhatMayBeReplayed(t *testing.T) {
	allowed := map[string]string{
		"/documents/" + doc + "/status":            "a status change",
		"/documents/" + doc + "/readings":          "a meter reading",
		"/documents/" + doc + "/time":              "time logged",
		"/documents/" + doc + "/files":             "a photograph",
		"/documents/" + doc + "/comments":          "a note",
		"/documents/" + doc + "/checklist/" + item: "a step ticked off",
	}
	for path, want := range allowed {
		kind, ok := Replayable(path)
		if !ok {
			t.Errorf("%s cannot be queued, and it is one of the six things "+
				"somebody does standing at a machine", path)
			continue
		}
		if kind != want {
			t.Errorf("%s is described as %q, want %q", path, kind, want)
		}
	}
}

// The allowlist is the security boundary, not a convenience. A batch endpoint
// that replays any path is a way to reach every handler on the site with one
// request, and it is worth spelling out exactly which attempts are refused.
func TestWhatMayNotBeReplayed(t *testing.T) {
	refused := []struct{ path, why string }{
		{"", "nothing at all"},
		{"/documents/" + doc + "/delete", "deleting is not a thing to do blind, hours later"},
		{"/documents/" + doc, "editing the whole document, which is not a one handed action"},
		{"/documents/" + doc + "/status/", "a trailing slash, which the mux would redirect"},
		{"/documents/" + doc + "/status?force=1", "a query string none of these forms produce"},
		{"/offline/sync", "the batch endpoint itself, which would recurse"},
		{"/documents/../people", "a traversal"},
		{"/people", "administration"},
		{"/logout", "signing somebody out an hour after they walked away"},
		{"/documents/not-a-uuid/status", "an id that is not one"},
		{"/documents/" + doc + "/status#x", "a fragment, which a server never sees anyway"},
		{"/documents/" + doc + "/checklist", "the add form rather than one item"},
		{"//evil.example.com/documents/" + doc + "/status", "another host entirely"},
		{"/api/v1/documents", "the API, which authenticates differently"},
	}
	for _, c := range refused {
		if kind, ok := Replayable(c.path); ok {
			t.Errorf("%q was accepted as %q, and it is %s", c.path, kind, c.why)
		}
	}
}

func TestTheSubjectIsPulledOutOfThePath(t *testing.T) {
	if got := Subject("/documents/" + doc + "/status"); got != doc {
		t.Errorf("subject is %q, want %q", got, doc)
	}
	// Upper case in, lower case out, because it becomes a uuid column and a
	// map key on a page and the two have to agree.
	if got := Subject("/documents/" + strings.ToUpper(doc) + "/readings"); got != doc {
		t.Errorf("an upper case id came back as %q", got)
	}
	if got := Subject("/nothing"); got != "" {
		t.Errorf("a path with no document gave %q", got)
	}
}

// The photograph criterion, on its own.
//
// An upload that returns 200 having stored a truncated file is the failure
// being prevented here, and a flaky link is exactly what produces one.
func TestAPhotographHasToArriveIntact(t *testing.T) {
	data := []byte("the bytes of a cracked weld, near enough")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])

	if err := CheckDigest(&File{Data: data, Digest: good}); err != nil {
		t.Errorf("an intact photograph was refused: %v", err)
	}
	// Case is not the point. A phone that formats hex in capitals is not
	// sending a different photograph.
	if err := CheckDigest(&File{Data: data, Digest: strings.ToUpper(good)}); err != nil {
		t.Errorf("an upper case digest was refused: %v", err)
	}

	truncated := data[:len(data)-4]
	err := CheckDigest(&File{Data: truncated, Digest: good})
	if err == nil {
		t.Fatal("a photograph that lost its last four bytes was accepted, " +
			"which is exactly what a link dropping mid upload produces")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("the error is %v, and it has to be one the caller can classify", err)
	}

	for name, f := range map[string]*File{
		"no digest at all": {Data: data},
		"not a SHA-256":    {Data: data, Digest: "deadbeef"},
		"the right length but not hex": {Data: data,
			Digest: strings.Repeat("z", 64)},
	} {
		if err := CheckDigest(f); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// Nothing to check is not a failure. Only some operations carry a file.
	if err := CheckDigest(nil); err != nil {
		t.Errorf("an operation with no photograph was refused: %v", err)
	}
}

// A phone that has been in a drawer since the summer comes back confident and
// wrong about the date. The value is only ever shown to a person and used to
// order a list, so a wild one is replaced rather than rejected: refusing the
// work because the clock is wrong helps nobody standing at a machine.
func TestAWildClockIsReplacedRatherThanRefused(t *testing.T) {
	now := time.Now()
	recent := now.Add(-90 * time.Minute)
	if got := sane(recent); !got.Equal(recent) {
		t.Errorf("a reasonable time was changed to %v", got)
	}
	for name, in := range map[string]time.Time{
		"the epoch":          time.Unix(0, 0),
		"nothing at all":     {},
		"the middle of 2031": time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC),
		"a year ago":         now.AddDate(-2, 0, 0),
	} {
		got := sane(in)
		if got.Before(now.Add(-time.Minute)) || got.After(now.Add(time.Minute)) {
			t.Errorf("%s came back as %v rather than about now", name, got)
		}
	}
}

func TestALateArrivalIsNoticed(t *testing.T) {
	now := time.Now()
	quick := Record{QueuedAt: now.Add(-4 * time.Second), ReceivedAt: now}
	if quick.Late() {
		t.Error("four seconds was called late, and a queue that drained straight " +
			"away is not a story worth telling on a page")
	}
	held := Record{QueuedAt: now.Add(-2 * time.Hour), ReceivedAt: now}
	if !held.Late() {
		t.Error("two hours in a basement was not called late")
	}
	if got := held.Waited(); got < 119*time.Minute {
		t.Errorf("waited is %v", got)
	}
	// A phone whose clock is ahead of the server's would otherwise produce a
	// negative duration, and "held for -3m" on a page is worse than nothing.
	skewed := Record{QueuedAt: now.Add(time.Hour), ReceivedAt: now}
	if got := skewed.Waited(); got != 0 {
		t.Errorf("a clock running fast produced %v", got)
	}
}
