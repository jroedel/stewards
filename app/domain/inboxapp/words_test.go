package inboxapp

import (
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The scripts ask for their sentences by name (static/words.mjs), and the
// page gives them scriptWords. A name one side changes and the other does
// not is a sentence a steward sees as "sendingOn" on a phone in the garden,
// which no other test would notice; so every name a script asks for must be
// one scriptWords has, and every name scriptWords has must be asked for, or
// it is a sentence waiting for Claude that nobody reads.
func TestTheScriptsAskForWordsThatAreThere(t *testing.T) {
	asked := regexp.MustCompile(`\bt\("([A-Za-z]+)"`)
	used := map[string]bool{}

	names, err := fs.Glob(scripts, "static/*.mjs")
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range names {
		body, err := fs.ReadFile(scripts, name)
		if err != nil {
			t.Fatal(err)
		}

		for _, m := range asked.FindAllStringSubmatch(string(body), -1) {
			used[m[1]] = true

			if _, ok := scriptWords[m[1]]; !ok {
				t.Errorf("%s asks for %q, which scriptWords does not have", name, m[1])
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(scriptWords)) {
		if !used[name] {
			t.Errorf("scriptWords has %q, which no script asks for", name)
		}
	}

	if len(used) < 10 {
		t.Errorf("found only %d names asked for: is the pattern still the one the scripts use?", len(used))
	}
}

// A sentence the scripts fill is given to them with its placeholders as
// they are, and those are the ones the script fills: a translation keeps them
// (translationbus refuses one that does not), so this is the English's.
func TestTheScriptsWordsKeepTheirPlaceholders(t *testing.T) {
	for name, want := range map[string][]string{
		"sending":       {"{n}", "{count}"},
		"dropped":       {"{n}", "{count}"},
		"tooMany":       {"{count}", "{max}"},
		"refused":       {"{status}"},
		"stopped":       {"{problem}"},
		"sharedTooMany": {"{count}", "{max}"},
	} {
		for _, p := range want {
			if !strings.Contains(scriptWords[name].EN, p) {
				t.Errorf("%s is %q, without %s", name, scriptWords[name].EN, p)
			}
		}
	}
}
