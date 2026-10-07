package types_test

import (
	"testing"

	"github.com/jroedel/stewards/business/types"
)

func TestEachLanguageFallsBackToTheOtherUntilItIsWritten(t *testing.T) {
	both := types.Text{EN: "Rain garden", ES: "Jardín de lluvia"}
	englishOnly := types.Text{EN: "Fire pit"}
	spanishOnly := types.Text{ES: "Cama de los colibríes"}

	for _, tc := range []struct {
		text  types.Text
		lang  types.Lang
		want  string
		shown types.Lang
	}{
		{both, types.English, "Rain garden", types.English},
		{both, types.Spanish, "Jardín de lluvia", types.Spanish},
		{englishOnly, types.Spanish, "Fire pit", types.English},
		{englishOnly, types.English, "Fire pit", types.English},
		{spanishOnly, types.English, "Cama de los colibríes", types.Spanish},
		{spanishOnly, types.Spanish, "Cama de los colibríes", types.Spanish},
		{types.Text{}, types.Spanish, "", types.Spanish},
	} {
		if got := tc.text.In(tc.lang); got != tc.want {
			t.Errorf("%+v in %s = %q, want %q", tc.text, tc.lang, got, tc.want)
		}

		if got := tc.text.Shown(tc.lang); got != tc.shown {
			t.Errorf("%+v in %s is shown in %s, want %s", tc.text, tc.lang, got, tc.shown)
		}
	}

	if !spanishOnly.Written() || (types.Text{}).Written() {
		t.Error("Written is wrong")
	}

	if got := types.Only(types.Spanish, "Riega"); got != (types.Text{ES: "Riega"}) || got.Half(types.Spanish) != "Riega" || got.Half(types.English) != "" {
		t.Errorf("Only and Half: %+v", got)
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
