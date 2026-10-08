package photoapp

import (
	"strings"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/types"
)

// Words is this app's copy held in Go, for the catalog the translation
// memory lists. The rest is in the templates, through t.
var Words = page.Catalog{
	{Where: "the stewards' screens for a plant's photos, and the queue of photos to check", Words: words},
}

// wording is what the photo screens say from Go, in English.
type wording struct {
	// What the last button did.
	Added, Saved, Removed, ChangesSaved types.Text

	// The queue's, about the photo it did it to.
	Checked, ChangedChecked, Changed, Unchecked types.Text
	Undo, Uncheck                               types.Text

	// The many-at-once page's.
	NoneTicked, CheckedOne, CheckedMany, Refused types.Text

	// What to fix.
	TooLarge, TooLargeSent, Already, ChooseAPhoto, NotWhole types.Text
	Confirm, ChooseDay, ChoosePlace, Year, Month            types.Text
	NotChecked, OnePlant, WhichPlant, ChoosePlant           types.Text
	PlaceFromList                                           types.Text
	ChooseKind, OnItsScreen, NothingSaved                   types.Text

	// The lists' first choices.
	Elsewhere, ChooseAKind, NotKnown, NotSaid, AnotherPlant types.Text

	// A photo's caption, and where a date in the flowering record was seen.
	OurPhoto, NotTakenHere, OffTheProperty, Here types.Text
	Original                                     types.Text

	// Each kind as a heading, as a button three to a row, and in the
	// middle of a sentence.
	Kinds, Buttons, Mid map[photobus.Kind]types.Text
}

var words = wording{
	Added:        types.Text{EN: "Photo added."},
	Saved:        types.Text{EN: "Photo saved."},
	Removed:      types.Text{EN: "Photo removed."},
	ChangesSaved: types.Text{EN: "Changes saved."},

	Checked:        types.Text{EN: "Checked: {plant}, {kind}."},
	ChangedChecked: types.Text{EN: "Changed and checked: {plant}, {kind}."},
	Changed:        types.Text{EN: "Changed: {plant}, {kind}. It waits here to be checked."},
	Unchecked:      types.Text{EN: "Not checked any more. It is back in the queue."},
	Undo:           types.Text{EN: "Undo"},
	Uncheck:        types.Text{EN: "Uncheck"},

	NoneTicked:  types.Text{EN: "Nothing was checked: no photo was ticked."},
	CheckedOne:  types.Text{EN: "Checked 1 photo. Volunteers see it now."},
	CheckedMany: types.Text{EN: "Checked {count} photos. Volunteers see them now."},
	Refused:     types.Text{EN: "{count} could not be checked as they are. They are below, not ticked: open each one in the queue to see why."},

	TooLarge:      types.Text{EN: "That photo is larger than {size} MB. Send it as the camera saved it, not a video or a screen recording."},
	TooLargeSent:  types.Text{EN: "That photo is larger than {size} MB. Send it as the camera saved it."},
	Already:       types.Text{EN: "That photo is already here, under {kind}. Choose a different one."},
	ChooseAPhoto:  types.Text{EN: "Choose a photo to send."},
	NotWhole:      types.Text{EN: "The photo did not arrive whole. Try once more, nearer the house if the signal is weak."},
	Confirm:       types.Text{EN: "Tick the box to confirm, then press Remove again."},
	ChooseDay:     types.Text{EN: "Choose the day, or leave it empty."},
	ChoosePlace:   types.Text{EN: "Choose the place from the list, or leave it empty."},
	Year:          types.Text{EN: "Write the year as four figures, such as 2027, or leave it empty."},
	Month:         types.Text{EN: "Choose the month from the list."},
	NotChecked:    types.Text{EN: "Not checked: {problem} Change it first."},
	OnePlant:      types.Text{EN: "Choose one plant: a button or the list, not both."},
	WhichPlant:    types.Text{EN: "Choose the plant this photo shows."},
	ChoosePlant:   types.Text{EN: "Choose the plant from the list."},
	ChooseKind:    types.Text{EN: "Choose what the photo shows."},
	PlaceFromList: types.Text{EN: "Choose the place from the list."},
	OnItsScreen:   types.Text{EN: "{problem} Change it on the photo's own screen, linked below."},
	NothingSaved:  types.Text{EN: "Nothing was saved yet. Fix what is marked in the form below and press the button again."},

	Elsewhere:    types.Text{EN: "Somewhere else, off the property"},
	ChooseAKind:  types.Text{EN: "Choose a kind"},
	NotKnown:     types.Text{EN: "Not known"},
	NotSaid:      types.Text{EN: "Not said"},
	AnotherPlant: types.Text{EN: "Another plant"},

	OurPhoto:       types.Text{EN: "Our photo"},
	NotTakenHere:   types.Text{EN: "not taken here"},
	OffTheProperty: types.Text{EN: "off the property"},
	Here:           types.Text{EN: "here"},
	Original:       types.Text{EN: "{width} × {height} kept for the cards. Zoomed in, it is the photo as it was sent, with where it was taken and the camera's details taken out."},

	Kinds:   kindWords(photobus.Kind.Label),
	Buttons: kindWords(photobus.Kind.Word),
	Mid:     kindWords(func(k photobus.Kind) string { return lower(k.Label()) }),
}

// kindWords is each kind as photobus words it, as copy.
func kindWords(say func(photobus.Kind) string) map[photobus.Kind]types.Text {
	out := map[photobus.Kind]types.Text{}
	for _, k := range photobus.Kinds {
		out[k] = types.Text{EN: say(k)}
	}

	return out
}

// lower is a kind's label in the middle of a sentence: "leaf close-up".
func lower(s string) string {
	if s == "" {
		return s
	}

	return strings.ToLower(s[:1]) + s[1:]
}
