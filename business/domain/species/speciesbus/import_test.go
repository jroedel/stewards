package speciesbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/stewards/business/domain/species/speciesbus"
)

func TestAnImportCreatesThenChangesThenLeavesAlone(t *testing.T) {
	b := setup(t)

	f := winecup()
	f.Confirmed = false

	first, err := b.Import(t.Context(), f)
	if err != nil || first.Outcome != speciesbus.Created {
		t.Fatalf("first: %v, %v", first.Outcome, err)
	}

	again, err := b.Import(t.Context(), f)
	if err != nil || again.Outcome != speciesbus.Unchanged || again.Species.ID != first.Species.ID {
		t.Fatalf("again: %v, %v", again.Outcome, err)
	}

	// Untidy input that tidies to the same thing is the same.
	untidy := f
	untidy.Scientific = "  Callirhoe   involucrata "
	if again, _ := b.Import(t.Context(), untidy); again.Outcome != speciesbus.Unchanged {
		t.Errorf("untidy input: %v", again.Outcome)
	}

	f.Note.EN = "Plant in full sun."
	changed, err := b.Import(t.Context(), f)
	if err != nil || changed.Outcome != speciesbus.Updated || changed.Species.Note.EN != "Plant in full sun." {
		t.Fatalf("changed: %v, %v", changed.Outcome, err)
	}
}

// An import never confirms, and a change takes a confirmation away; sending
// the same thing again does not.
func TestAnImportCannotVouchForAPlant(t *testing.T) {
	b := setup(t)

	asked := winecup()
	asked.Confirmed = true

	if _, err := b.Import(t.Context(), asked); !isInvalidField(err, "confirmed") {
		t.Errorf("an import asking to confirm: %v", err)
	}

	// A steward confirms it on the screen.
	f := winecup()
	f.Confirmed = true

	sp, err := b.Create(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}

	f.Confirmed = false

	if same, _ := b.Import(t.Context(), f); same.Outcome != speciesbus.Unchanged || !same.Species.Confirmed || same.Unconfirmed {
		t.Errorf("the same again: %+v", same)
	}

	f.Scientific = "Callirhoe digitata"

	changed, err := b.Import(t.Context(), f)
	if err != nil || !changed.Unconfirmed || changed.Species.Confirmed {
		t.Fatalf("a change to a confirmed plant: %+v, %v", changed, err)
	}

	if back, _ := b.ByID(t.Context(), sp.ID); back.Confirmed {
		t.Error("the stored plant is still confirmed")
	}
}

func isInvalidField(err error, field string) bool {
	invalid, ok := errors.AsType[speciesbus.Invalid](err)

	return ok && invalid.Field == field
}
