package web

import (
	"errors"
	"testing"
)

// Every way somebody at Fireball writes a duration, and the one way that has
// to be refused: a bare small number, which used to save "8" (a day) as eight
// minutes without a word.
func TestDurationsAreReadTheWayPeopleWriteThem(t *testing.T) {
	ok := map[string]int{
		"1:30": 90, "0:15": 15, "8:00": 480, "12:45": 765,
		"1h": 60, "8h": 480, "1h30": 90, "1h 30m": 90, "1h30m": 90, "2 hours": 120, "1 hour": 60,
		"1.5h": 90, "1,5h": 90, "0.25h": 15, "2.75 h": 165,
		"90m": 90, "90 min": 90, "45 mins": 45, "15 minutes": 15, "5m": 5,
		"2 horas": 120, "8hs": 480, "30 minutos": 30,
		"90": 90, "45": 45, "13": 13, "240": 240,
		" 1H30 ": 90,
	}
	for in, want := range ok {
		got, err := parseDuration(in)
		if err != nil || got != want {
			t.Errorf("parseDuration(%q) = %d, %v; want %d", in, got, err, want)
		}
	}

	for _, in := range []string{"8", "1", "12", "1.5", "0.5", "7,5"} {
		if _, err := parseDuration(in); !errors.Is(err, errDurationAmbiguous) {
			t.Errorf("parseDuration(%q) = %v; a bare small number must be asked about, not guessed", in, err)
		}
	}
	for _, in := range []string{"", "abc", "1:75", "-1h", "0", "0h", "h", "1x", "1:"} {
		if n, err := parseDuration(in); err == nil {
			t.Errorf("parseDuration(%q) = %d; want a refusal", in, n)
		}
	}
	if msg := durationProblem("8", errDurationAmbiguous); msg != "Is 8 hours or minutes? Write 8h for hours or 8m for minutes." {
		t.Errorf("the ambiguous message reads %q", msg)
	}
}
