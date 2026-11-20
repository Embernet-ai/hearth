package timelog

// The capability names this package asks about are strings, because the
// Authorizer interface takes a string so that timelog does not import authz for
// one bool. That is a reasonable trade and it has a cost: two spellings of the
// same idea in two packages, with nothing but this test between them.
//
// A drift here is silent in the worst way. HasCapability with a name nobody
// grants returns false, so the page refuses everybody, and the report is "the
// workload page is broken for everyone" rather than "a constant is misspelt".

import (
	"testing"

	"github.com/Embernet-ai/hearth/internal/authz"
)

func TestCapabilityNamesMatchTheOnesAuthzDefines(t *testing.T) {
	for _, pair := range []struct {
		here  string
		there authz.Capability
	}{
		{capTimeAll, authz.TimeAll},
		{capFinance, authz.Finance},
		{capPeople, authz.People},
	} {
		if pair.here != string(pair.there) {
			t.Errorf("timelog asks for %q and authz calls it %q.\n"+
				"HasCapability would return false for everybody, so the page "+
				"would refuse the whole company and look like a bug in the "+
				"page rather than in this constant.", pair.here, pair.there)
		}
	}
}
