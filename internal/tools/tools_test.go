package tools

import "testing"

func TestParseReadsCardsAndRefusesMistakes(t *testing.T) {
	got, err := Parse(`[{"name":"Ops","description":"Orders","href":"https://ops.example"},{"name":"Vord","description":"Account","href":"https://vord.example","note":"Sign in with Vord"}]`)
	if err != nil || len(got) != 2 || got[1].Note != "Sign in with Vord" {
		t.Fatalf("Parse = %+v, %v", got, err)
	}
	if got, err := Parse("  "); err != nil || got != nil {
		t.Errorf("empty config = %v, %v, want no tools and no error", got, err)
	}
	for name, raw := range map[string]string{
		"not a list": `{"name":"x"}`,
		"no name":    `[{"name":" ","href":"https://a.example"}]`,
		"bad scheme": `[{"name":"a","href":"javascript:alert(1)"}]`,
		"no host":    `[{"name":"a","href":"https://"}]`,
		"no address": `[{"name":"a"}]`,
	} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
