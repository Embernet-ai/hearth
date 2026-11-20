package web

import (
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// errDurationAmbiguous is a bare small number: 8 could be eight hours or eight
// minutes, and guessing minutes is how a day's work used to be saved as eight
// minutes without anybody noticing.
var errDurationAmbiguous = errors.New("ambiguous")

// durationRe reads "1h", "1h30", "1h 30m", "1.5h", "90m", "90 min", "2 horas",
// "8hs" -- an optional hours part, an optional minutes part. A unitless number
// after an hours part is minutes ("1h30").
var durationRe = regexp.MustCompile(
	`^(?:(\d+(?:\.\d+)?)\s*(?:h|hr|hrs|hs|hour|hours|hora|horas))?\s*(?:(\d+)\s*(m|min|mins|minute|minutes|minuto|minutos)?)?$`)

var bareDecimal = regexp.MustCompile(`^\d+\.\d+$`)

// parseDuration reads how long something took, in the forms people actually
// type, and returns minutes.
//
//	1:30  1h  1h30  1h 30m  1.5h  1,5h  90m  90 min  2 horas  8hs  -> minutes
//	90 (a bare number over 12)                                     -> minutes, as it always was
//	8  (a bare number 1-12, or any bare decimal)                    -> errDurationAmbiguous
func parseDuration(v string) (int, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, ",", ".") // 1,5h is how half of Fireball writes it
	if v == "" {
		return 0, errors.New("empty")
	}

	if h, m, ok := strings.Cut(v, ":"); ok {
		hours, err1 := strconv.Atoi(strings.TrimSpace(h))
		mins, err2 := strconv.Atoi(strings.TrimSpace(m))
		if err1 != nil || err2 != nil || hours < 0 || mins < 0 || mins > 59 {
			return 0, errors.New("not a time")
		}
		return hours*60 + mins, nil
	}

	// A bare decimal ("1.5", "7,5") is nearly always hours, but "nearly" is
	// the trap this exists to close: ask.
	if bareDecimal.MatchString(v) {
		return 0, errDurationAmbiguous
	}

	g := durationRe.FindStringSubmatch(v)
	if g == nil || (g[1] == "" && g[2] == "") {
		return 0, errors.New("not a duration")
	}
	hours, minutes := 0.0, 0
	if g[1] != "" {
		hours, _ = strconv.ParseFloat(g[1], 64)
	}
	if g[2] != "" {
		minutes, _ = strconv.Atoi(g[2])
	}
	// A number on its own, no unit at all.
	if g[1] == "" && g[3] == "" {
		if strings.Contains(v, ".") || minutes <= 12 {
			return 0, errDurationAmbiguous
		}
	}
	total := int(math.Round(hours*60)) + minutes
	if total <= 0 {
		return 0, errors.New("zero")
	}
	return total, nil
}

// durationProblem is what to tell somebody whose duration could not be read.
func durationProblem(v string, err error) string {
	if errors.Is(err, errDurationAmbiguous) {
		n := strings.TrimSpace(v)
		return "Is " + n + " hours or minutes? Write " + n + "h for hours or " + n + "m for minutes."
	}
	return "How long did it take? Write it like 1:30, 1h30, 1.5h or 90m."
}

// durationError is a reason a pair of hours and minutes fields cannot be taken, already worded for the person who typed them.
type durationError struct{ msg string }

func (e *durationError) Error() string { return e.msg }

// wholeNumber reads one of the two number fields: blank is zero, and anything else must be digits.
func wholeNumber(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 0
}

// formDuration reads how long from a form, and says which kind of form it was.
//
// The forms post two whole-number fields, hours and minutes (named hoursKey and minsKey), so nobody has to type an h or an m, and
// "is 2 hours or minutes?" cannot come up. The old single free-text field (oldKey) is still read when the form has neither of the
// two: that is what an entry queued on the phone before this change, the API, and hearthctl send, and they keep working with the
// same parser and the same ambiguity check as before. blank is true when the person left hours and minutes both empty or zero, which
// the estimate form reads as "clear it" and the others refuse.
func formDuration(r *http.Request, hoursKey, minsKey, oldKey string) (minutes int, blank bool, err error) {
	_, hasH := r.Form[hoursKey]
	_, hasM := r.Form[minsKey]
	if !hasH && !hasM {
		v := r.FormValue(oldKey)
		n, err := parseDuration(v)
		if err != nil {
			// ambiguous or unreadable: the same sentences as ever
			return 0, false, &durationError{durationProblem(v, err)}
		}
		return n, false, nil
	}
	h, okH := wholeNumber(r.FormValue(hoursKey))
	if !okH {
		return 0, false, &durationError{"Hours must be a whole number, like 2."}
	}
	m, okM := wholeNumber(r.FormValue(minsKey))
	if !okM {
		return 0, false, &durationError{"Minutes must be a whole number from 0 to 59."}
	}
	if m > 59 {
		return 0, false, &durationError{"Minutes go from 0 to 59. Put whole hours in the Hours box."}
	}
	if h > 999 {
		return 0, false, &durationError{"That is too many hours for one entry."}
	}
	if h == 0 && m == 0 {
		return 0, true, nil
	}
	return h*60 + m, false, nil
}

// blankDuration is the sentence for a log or correct form with hours and minutes both left empty.
const blankDuration = "Enter how long it took: hours, minutes, or both."
