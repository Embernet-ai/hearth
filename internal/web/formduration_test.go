package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func formOf(v url.Values) *http.Request {
	r, _ := http.NewRequest("POST", "/time", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	return r
}

// The forms ask for hours and minutes in two number fields; the old single field is still read for entries queued before, the API
// and hearthctl.
func TestFormDurationReadsHoursAndMinutes(t *testing.T) {
	for _, c := range []struct {
		name string
		form url.Values
		want int
	}{
		{"0h 45m", url.Values{"hours": {"0"}, "mins": {"45"}}, 45},
		{"2h 0m", url.Values{"hours": {"2"}, "mins": {"0"}}, 120},
		{"1h 30m", url.Values{"hours": {"1"}, "mins": {"30"}}, 90},
		{"hours alone", url.Values{"hours": {"3"}, "mins": {""}}, 180},
		{"minutes alone", url.Values{"hours": {""}, "mins": {"20"}}, 20},
		{"only the hours field sent", url.Values{"hours": {"1"}}, 60},
		{"59 minutes", url.Values{"hours": {"0"}, "mins": {"59"}}, 59},
		{"spaces are ignored", url.Values{"hours": {" 1 "}, "mins": {" 5 "}}, 65},
	} {
		got, blank, err := formDuration(formOf(c.form), "hours", "mins", "minutes")
		if err != nil || blank || got != c.want {
			t.Errorf("%s: %d, blank=%v, %v; want %d", c.name, got, blank, err, c.want)
		}
	}
}

func TestFormDurationRefusesWhatCannotBeTaken(t *testing.T) {
	for _, c := range []struct {
		name  string
		form  url.Values
		blank bool
		msg   string
	}{
		{"both blank", url.Values{"hours": {""}, "mins": {""}}, true, ""},
		{"both zero", url.Values{"hours": {"0"}, "mins": {"0"}}, true, ""},
		{"over 59 minutes", url.Values{"hours": {"1"}, "mins": {"75"}}, false, "Minutes go from 0 to 59"},
		{"not a number", url.Values{"hours": {"two"}, "mins": {"0"}}, false, "Hours must be a whole number"},
		{"negative", url.Values{"hours": {"-1"}, "mins": {"0"}}, false, "Hours must be a whole number"},
		{"decimal minutes", url.Values{"hours": {"0"}, "mins": {"1.5"}}, false, "Minutes must be a whole number"},
		{"too many hours", url.Values{"hours": {"1000"}, "mins": {"0"}}, false, "too many hours"},
	} {
		_, blank, err := formDuration(formOf(c.form), "hours", "mins", "minutes")
		if blank != c.blank || (c.msg == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.msg)) {
			t.Errorf("%s: blank=%v err=%v", c.name, blank, err)
		}
	}
}

// What was queued on the old form, and what the API and hearthctl send, still goes through the old parser, ambiguity check and all.
func TestFormDurationStillReadsTheOldField(t *testing.T) {
	for in, want := range map[string]int{"90": 90, "37": 37, "1h30": 90, "1:30": 90, "90m": 90, "2h": 120} {
		got, blank, err := formDuration(formOf(url.Values{"minutes": {in}}), "hours", "mins", "minutes")
		if err != nil || blank || got != want {
			t.Errorf("old field %q: %d, %v; want %d", in, got, err, want)
		}
	}
	_, _, err := formDuration(formOf(url.Values{"minutes": {"2"}}), "hours", "mins", "minutes")
	if err == nil || !strings.Contains(err.Error(), "Is 2 hours or minutes?") {
		t.Errorf("an ambiguous old value still asks: %v", err)
	}
	if _, _, err := formDuration(formOf(url.Values{"minutes": {"soon"}}), "hours", "mins", "minutes"); err == nil {
		t.Error("an unreadable old value is refused")
	}
	// the estimate form's own names
	n, blank, err := formDuration(formOf(url.Values{"estimate_hours": {"3"}, "estimate_mins": {"15"}}), "estimate_hours", "estimate_mins", "estimate")
	if err != nil || blank || n != 195 {
		t.Errorf("estimate: %d %v %v", n, blank, err)
	}
	if _, blank, _ := formDuration(formOf(url.Values{"estimate_hours": {""}, "estimate_mins": {""}}), "estimate_hours", "estimate_mins", "estimate"); !blank {
		t.Error("blank estimate fields clear it")
	}
}

// The reason an address carries to My time is one of a fixed set of sentences, never text taken from the address.
func TestMyTimeShowsOnlyKnownReasons(t *testing.T) {
	if tf := refusedOwnTime(url.Values{"te": {"<b>hi</b>"}}); tf != nil {
		t.Fatalf("text from an address was shown: %+v", tf)
	}
	tf := refusedOwnTime(url.Values{"te": {"range"}, "th": {"1"}, "tm": {"75"}, "tn": {"a note"}})
	if tf == nil || !strings.Contains(tf.Error, "0 to 59") || tf.Hours != "1" || tf.Mins != "75" || tf.Note != "a note" {
		t.Fatalf("%+v", tf)
	}
	fix := refusedOwnTime(url.Values{"te": {"blank"}, "tk": {"e1"}})
	if fix == nil || fix.Fix == nil || fix.Fix.ID != "e1" || fix.Error != "" {
		t.Fatalf("a refused correction is shown beside the entry: %+v", fix)
	}
}
