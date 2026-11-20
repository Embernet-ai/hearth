package auth

import "testing"

// SafeNext is the only thing standing between a helpful redirect and an open
// one. It is a pure function with no database behind it, so there is no excuse
// for not enumerating the ways in.
func TestSafeNextRefusesAnythingThatLeavesTheSite(t *testing.T) {
	for _, v := range []string{
		"", "   ",
		"https://evil.example/steal",
		"//evil.example/steal",
		"http://evil.example",
		`/\evil.example`,
		"/\\evil.example",
		"javascript:alert(1)",
		"documents/1",           // no leading slash, so it is relative to whatever
		"/login",                // signing in to land on the sign in page
		"/login?next=/login",    // and the loop that would make
		"/logout",               // signing in to sign out
		"/auth/callback?code=x", // replaying somebody else's flow
		"/documents\r\nSet-Cookie: a=b",
		"/documents\nLocation: https://evil.example",
	} {
		if got := SafeNext(v); got != "" {
			t.Errorf("SafeNext(%q) = %q, want empty.\n"+
				"Anything that is not a plain path on this site has to come back "+
				"empty, because the caller puts the result straight into a "+
				"Location header.", v, got)
		}
	}
}

func TestSafeNextKeepsAPlainPath(t *testing.T) {
	for _, v := range []string{
		"/",
		"/documents",
		"/documents/22222222-2222-2222-2222-222222222222",
		"/a/CV-003",
		"/work-orders?f=status%3Ais%3Atodo",
	} {
		if got := SafeNext(v); got != v {
			t.Errorf("SafeNext(%q) = %q, want it unchanged.\n"+
				"Refusing a legitimate path means somebody scanning a machine "+
				"lands on the front page instead of the machine.", v, got)
		}
	}
}
