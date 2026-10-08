package inboxapp

import (
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/types"
)

// Words is this app's copy held in Go, for the catalog the translation
// memory lists. The rest is in the templates, through t.
var Words = page.Catalog{
	{Where: "the stewards' photo inbox: sending photos to it, and sorting them", Words: words},
	{Where: "the send screen's script, as it sends the photos one at a time; {placeholders} are filled in by the script", Words: scriptWords},
}

// The sentences both the server and the send screen's script say, the one
// for a batch sent without the script and the other with it.
var (
	tooMany     = types.Text{EN: "That is {count} photos. Send up to {max} at a time."}
	noNew       = types.Text{EN: "No new photos."}
	oneIn       = types.Text{EN: "1 photo is in the inbox."}
	manyIn      = types.Text{EN: "{count} photos are in the inbox."}
	oneAlready  = types.Text{EN: "1 was there already."}
	manyAlready = types.Text{EN: "{count} were there already."}
	wentWrong   = types.Text{EN: "Something went wrong on our end. Try again in a few minutes."}
)

// wording is what the inbox's screens say from Go, in English.
type wording struct {
	// After a photo is sorted, by the word its redirect carries.
	Sorted map[string]types.Text

	// After a batch, and after photos shared from the phone did not arrive.
	NoNew, OneIn, ManyIn, OneAlready, ManyAlready types.Text
	NoneChosen, SharedAgain, SharedLost           types.Text

	// What to fix.
	MoreThan, ChoosePhotos, TooMany, Dropped, ChoosePlace types.Text
	OneAtATime, TooLarge, NotWhole, Empty, Count          types.Text
	OnePlant, FromList, Confirm, WentWrong                types.Text

	// The lists' first choices.
	NotSure, ChooseThePlant, AnotherPlant, NotSaid types.Text

	// A photo sorted already, and where to see what it became.
	SortedAs, WasDiscarded, SeeStock, SeePhotos types.Text

	// Where a photo is in the inbox, and when it was taken.
	Of, OfChosen, TakenAt types.Text

	// Where a batch was taken, and each kind of photo as a button.
	At      map[inboxbus.At]types.Text
	Buttons map[photobus.Kind]types.Text
}

var words = wording{
	Sorted: map[string]types.Text{
		"photo":           {EN: "Added to the plant's photos. It is shown to volunteers once a steward checks it."},
		"photo-checked":   {EN: "Added to the plant's photos, checked. Volunteers see it now."},
		"planted":         {EN: "Listed as planted there, and added to the plant's photos to be checked."},
		"planted-checked": {EN: "Listed as planted there, and added to the plant's photos, checked."},
		"stock":           {EN: "Added to the nursery's stock."},
		"unsure":          {EN: "Set aside, with the question."},
		"discard":         {EN: "Discarded."},
		"taken":           {EN: "That photo had already been sorted, by somebody else or on another screen."},
	},

	NoNew: noNew, OneIn: oneIn, ManyIn: manyIn, OneAlready: oneAlready, ManyAlready: manyAlready,
	NoneChosen:  types.Text{EN: "No photos were chosen. Tap the photos of one plant, then Sort the chosen photos together."},
	SharedAgain: types.Text{EN: "Those photos did not arrive: the app was not ready for them yet. It is now. Share them again from your photos app."},
	SharedLost:  types.Text{EN: "Those photos did not arrive: the phone could not keep them for this page, perhaps for want of room. Share them again, or choose them below."},

	MoreThan:     types.Text{EN: "That is more than {size} MB at once. Send the photos in two or three goes."},
	ChoosePhotos: types.Text{EN: "Choose the photos to send."},
	TooMany:      tooMany,
	Dropped:      types.Text{EN: "The connection dropped before this one was kept. Send it again."},
	ChoosePlace:  types.Text{EN: "Choose the place from the list, or leave it empty."},
	OneAtATime:   types.Text{EN: "Send one photo at a time here."},
	TooLarge:     types.Text{EN: "Larger than {size} MB. Send it as the camera saved it, not a video."},
	NotWhole:     types.Text{EN: "It did not arrive whole. Send it again."},
	Empty:        types.Text{EN: "It arrived empty. Send it again."},
	Count:        types.Text{EN: "Write how many there were as a number, or leave it empty."},
	OnePlant:     types.Text{EN: "Choose one plant: a button or the list, not both."},
	FromList:     types.Text{EN: "Choose from the list."},
	Confirm:      types.Text{EN: "Tick the box to confirm, then press Discard again."},
	WentWrong:    wentWrong,

	NotSure:        types.Text{EN: "Not sure, or more than one"},
	ChooseThePlant: types.Text{EN: "Choose the plant"},
	AnotherPlant:   types.Text{EN: "Another plant"},
	NotSaid:        types.Text{EN: "Not said, or not here"},

	SortedAs:     types.Text{EN: "This photo has been sorted: {what}"},
	WasDiscarded: types.Text{EN: "This photo was discarded."},
	SeeStock:     types.Text{EN: "See the nursery stock"},
	SeePhotos:    types.Text{EN: "See the plant's photos"},

	Of:       types.Text{EN: "{n} of {count}"},
	OfChosen: types.Text{EN: "{n} of the {count} chosen"},
	TakenAt:  types.Text{EN: "{date}, {time}"},

	At: map[inboxbus.At]types.Text{
		inboxbus.Property:  {EN: inboxbus.Property.Label()},
		inboxbus.Nursery:   {EN: inboxbus.Nursery.Label()},
		inboxbus.Elsewhere: {EN: inboxbus.Elsewhere.Label()},
	},
	Buttons: func() map[photobus.Kind]types.Text {
		out := map[photobus.Kind]types.Text{}
		for _, k := range photobus.Kinds {
			out[k] = types.Text{EN: k.Word()}
		}

		return out
	}(),
}

// scriptWords are the send screen's scripts' sentences, by the name each
// script asks for (static/words.mjs). The page gives them to the scripts in
// its own language, {placeholders} and all, and a script fills those in:
// "Sending {n} of {count}…" is a different sentence every second, and the
// server is not asked for each one. TestTheScriptsAskForWordsThatAreThere
// holds the names here to the names the scripts use.
var scriptWords = map[string]types.Text{
	"tooMany":     tooMany,
	"noNew":       noNew,
	"oneIn":       oneIn,
	"manyIn":      manyIn,
	"oneAlready":  oneAlready,
	"manyAlready": manyAlready,

	"sending":   {EN: "Sending {n} of {count}…"},
	"sendingOn": {EN: "Sending…"},
	"opening":   {EN: "Sent. Opening the inbox…"},
	"dropped":   {EN: "The signal dropped on {n} of {count}. Trying again…"},
	"dropping":  {EN: "The signal keeps dropping on {n} of {count}. Still trying; keep this page open…"},
	"gone":      {EN: "The connection is gone. Press Send again when you have a signal: the photos already sent are in the inbox, and only the rest will be sent."},
	"signedOut": {EN: "Your sign-in has run out. Sign in again in a new tab, then come back and press Send: the photos already sent will not be added twice."},
	"tooLarge":  {EN: "Too large to send. Send it as the camera saved it, not a video."},
	"refused":   {EN: "The server would not take it ({status}). Open this page again and send once more."},
	"notKept":   {EN: "It could not be kept."},
	"stopped":   {EN: "{problem} Nothing more was sent; fix it and press Send again."},
	"countNot":  {EN: "{count} not kept:"},

	"sharedGone":      {EN: "The photos you shared are not on this page any more: they were sent, or the phone cleared them. Look in the inbox, and if they are not there, share them again."},
	"sharedTooMany":   {EN: "You shared {count} photos. The inbox takes up to {max} at a time: share them again in two goes."},
	"sharedOneStuck":  {EN: "1 photo shared, but this browser cannot put it in the form. Choose it below instead."},
	"sharedManyStuck": {EN: "{count} photos shared, but this browser cannot put them in the form. Choose them below instead."},
	"sharedOne":       {EN: "1 photo from your phone, ready to send. Say where it was taken, then press Send."},
	"sharedMany":      {EN: "{count} photos from your phone, ready to send. Say where they were taken, then press Send."},
}

// scriptWordsIn is scriptWords in l, for the page to hand the scripts.
func (a app) scriptWordsIn(l types.Lang) map[string]string {
	out := make(map[string]string, len(scriptWords))
	for name, t := range scriptWords {
		out[name] = a.render.Plain(l, t)
	}

	return out
}
