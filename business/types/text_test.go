package types_test

import (
	"testing"

	"github.com/jroedel/stewards/business/types"
)

func TestSpanishFallsBackToEnglishUntilItIsWritten(t *testing.T) {
	both := types.Text{EN: "Rain garden", ES: "Jardín de lluvia"}
	englishOnly := types.Text{EN: "Fire pit"}

	for _, tc := range []struct {
		text types.Text
		lang types.Lang
		want string
	}{
		{both, types.English, "Rain garden"},
		{both, types.Spanish, "Jardín de lluvia"},
		{englishOnly, types.Spanish, "Fire pit"},
		{englishOnly, types.English, "Fire pit"},
	} {
		if got := tc.text.In(tc.lang); got != tc.want {
			t.Errorf("%+v in %s = %q, want %q", tc.text, tc.lang, got, tc.want)
		}
	}

	if englishOnly.HasSpanish() {
		t.Error("English-only text claims to have Spanish")
	}
}

func TestParseLangAcceptsOnlyWhatTheAppSpeaks(t *testing.T) {
	for _, s := range []string{"en", "ES", " es "} {
		if _, err := types.ParseLang(s); err != nil {
			t.Errorf("ParseLang(%q): %v", s, err)
		}
	}

	for _, s := range []string{"", "fr", "english", "es-MX"} {
		if _, err := types.ParseLang(s); err == nil {
			t.Errorf("ParseLang(%q) was accepted", s)
		}
	}
}
