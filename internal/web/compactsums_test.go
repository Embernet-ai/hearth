package web

import (
	"testing"

	"github.com/Embernet-ai/hearth/internal/crm"
)

func TestCompactSumsShortensOnlyWhatIsBig(t *testing.T) {
	for _, c := range []struct {
		in           []crm.Sum
		short, exact string
	}{
		{[]crm.Sum{{Currency: "USD", Cents: 1806600728}}, "$18.07M", "$18,066,007.28"}, // the live open pipeline
		{[]crm.Sum{{Currency: "USD", Cents: 160352128}}, "$1.60M", "$1,603,521.28"},
		{[]crm.Sum{{Currency: "USD", Cents: 57600000}}, "$576,000", ""}, // under a million is written out
		{[]crm.Sum{{Currency: "USD", Cents: 99999999}}, "$999,999.99", ""},
		{[]crm.Sum{{Currency: "USD", Cents: 100000000}}, "$1.00M", "$1,000,000"},
		{[]crm.Sum{{Currency: "USD", Cents: 120000000000}}, "$1.20B", "$1,200,000,000"},
		{[]crm.Sum{{Currency: "USD", Cents: 250000000}, {Currency: "EUR", Cents: 90000}}, "$2.50M + EUR 900", "$2,500,000 + EUR 900"},
		{nil, "$0", ""},
	} {
		short, exact := compactSums(c.in)
		if short != c.short || exact != c.exact && !(c.exact == "" && exact == sums(c.in)) {
			t.Errorf("compactSums(%v) = %q, %q; want %q, %q", c.in, short, exact, c.short, c.exact)
		}
	}
}
