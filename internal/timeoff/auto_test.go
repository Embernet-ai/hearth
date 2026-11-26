package timeoff

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func vac(from, to time.Time, half bool) *Request {
	return &Request{Kind: "vacation", Status: "approved", StartsOn: from, EndsOn: to, HalfDay: half}
}

func TestMonthlyCap(t *testing.T) {
	mon := day(2030, 1, 7) // a Monday
	cases := []struct {
		name     string
		approved []*Request
		from, to time.Time
		half     bool
		want     bool
	}{
		{"a whole week fits", nil, mon, mon.AddDate(0, 0, 4), false, true},
		{"six working days do not", nil, mon, mon.AddDate(0, 0, 7), false, false},
		{"a week and a weekend is still five", nil, mon, mon.AddDate(0, 0, 6), false, true},
		{"already four, one more fits", []*Request{vac(mon, mon.AddDate(0, 0, 3), false)}, day(2030, 1, 21), day(2030, 1, 21), false, true},
		{"already four, two more do not", []*Request{vac(mon, mon.AddDate(0, 0, 3), false)}, day(2030, 1, 21), day(2030, 1, 22), false, false},
		{"a half day counts half, 5.5 is over", []*Request{vac(mon, mon.AddDate(0, 0, 3), false)}, day(2030, 1, 21), day(2030, 1, 22), true, false},
		{"a half day alone fits", []*Request{vac(mon, mon.AddDate(0, 0, 3), false)}, day(2030, 1, 21), day(2030, 1, 21), true, true},
		{"another month is its own week", []*Request{vac(mon, mon.AddDate(0, 0, 4), false)}, day(2030, 2, 4), day(2030, 2, 8), false, true},
		{"cancelled and other kinds do not count", []*Request{{Kind: "vacation", Status: "cancelled", StartsOn: mon, EndsOn: mon.AddDate(0, 0, 4)}, {Kind: "sick", Status: "approved", StartsOn: mon, EndsOn: mon.AddDate(0, 0, 4)}}, day(2030, 1, 21), day(2030, 1, 25), false, true},
		// 28 Jan (Mon) to 8 Feb: 4 working days in January (28 to 31), 6 in February (1, 4 to 8).
		{"a span is counted in each month it touches", nil, day(2030, 1, 28), day(2030, 2, 8), false, false},
		{"a span within the week in both months", nil, day(2030, 1, 30), day(2030, 2, 1), false, true},
		{"the other month's approved days count there", []*Request{vac(day(2030, 2, 4), day(2030, 2, 7), false)}, day(2030, 1, 30), day(2030, 2, 4), false, false},
	}
	for _, c := range cases {
		if got := WithinMonthlyCap(c.approved, c.from, c.to, c.half); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
