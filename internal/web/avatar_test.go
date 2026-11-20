package web

import "testing"

// The fallback picture is two letters of a name, the same colour every time.
func TestInitialsAndColourOfAName(t *testing.T) {
	for name, want := range map[string]string{
		"Luis Navarro": "LN", "yara": "Y", "Ana María López": "AM", "  ": "?", "élan vital": "ÉV",
	} {
		if got := initials(name); got != want {
			t.Errorf("initials(%q) = %q, want %q", name, got, want)
		}
	}
	if avatarHue("Luis Navarro") != avatarHue("Luis Navarro") {
		t.Fatal("the same name got two colours")
	}
	if h := avatarHue("Pat"); h < 0 || h >= 360 {
		t.Fatalf("hue %d is not a hue", h)
	}
}
