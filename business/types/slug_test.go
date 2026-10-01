package types_test

import (
	"strings"
	"testing"

	"github.com/jroedel/stewards/business/types"
)

func TestAnAddressIsWhatAPersonCanType(t *testing.T) {
	for s, ok := range map[string]bool{
		"rain-garden":           true,
		"winecup":               true,
		"zone-2":                true,
		"Rain-Garden":           false,
		"rain garden":           false,
		"-rain":                 false,
		"rain-":                 false,
		"rain--garden":          false,
		"jardín":                false,
		strings.Repeat("a", 48): true,
		strings.Repeat("a", 49): false,
	} {
		if got := types.SlugProblem(s) == ""; got != ok {
			t.Errorf("SlugProblem(%q) = %q; want ok=%v", s, types.SlugProblem(s), ok)
		}
	}
}
