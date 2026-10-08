package placeapp

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
)

// This file is the place card: the page a volunteer opens from the list,
// at /places/<slug>. design.md, section 6: "What's here, what do I do?"
//
// It shows what a place holds -- its name, what it is for, its conditions,
// the smaller places inside it, its photo point and, for a station's space,
// the way to that station's prayer -- and the plants listed there: what is
// planned, when it flowers, and the Protect, Pull and Careful panels. The
// dated photo and today's job arrive with the data behind them.
//
// Whatever is listed, the card ends its plant sections with the one piece of
// advice that keeps a native in the ground: not sure? Leave it.
//
// Public, like the list. The Phase 1 test is a volunteer with a phone and no
// account, so nothing here asks who you are; a steward who is signed in also
// gets a link to edit.

// trailURL is the public trail page. Its anchors are the QR codes on the
// boards, so a station's link is this plus "#" plus the anchor, and never
// anything else (design.md, section 1).
const trailURL = "https://schoenstatt-fathers.us/trail/"

// CardRoutes mounts the place card. Unlike Routes it is not behind sign-in,
// and it is mounted whether or not sign-in is configured.
func CardRoutes(mux *http.ServeMux, cfg Config) {
	a := newApp(cfg)

	mux.HandleFunc("GET /places/{slug}", a.card)
}

// The words on the card, in English; Claude translates them through the
// translation memory, and say looks them up (page/words.go).
type cardWording struct {
	Back, PartOf, Inside, Conditions, PhotoPoint, PhotoPointHelp,
	StationIntro, StationGo, NotListed, NotSure, Edit,
	PlannedHere, PlannedHelp, Flowers, FlowersHelp, Plant,
	Protect, ProtectHelp, Pull, PullHelp, Careful, CarefulHelp, NothingHere types.Text

	// A plant's size on the To plant list, and a band's plants in a few
	// words on its parent's card.
	TallWide, Tall, ToPlant, ToPull types.Text

	// Stations is each station's name as the card heads its link, by its
	// anchor.
	Stations map[string]types.Text
}

// Words is the card's copy, for the catalog the translation memory lists.
var Words = page.Catalog{
	{Where: "the place card, which a volunteer reads standing in the garden", Words: cardWords},
}

var cardWords = cardWording{
	Back:           types.Text{EN: "All places"},
	PartOf:         types.Text{EN: "Part of"},
	Inside:         types.Text{EN: "Inside this place"},
	Conditions:     types.Text{EN: "Conditions"},
	PhotoPoint:     types.Text{EN: "Photo point"},
	PhotoPointHelp: types.Text{EN: "Where to stand for this place's photo each season."},
	StationIntro:   types.Text{EN: "A station on the Trail of the Saints."},
	StationGo:      types.Text{EN: "Open its prayer"},
	NotListed:      types.Text{EN: "The plants for this place are not listed yet."},
	NotSure:        types.Text{EN: "Not sure what something is? Leave it."},
	Edit:           types.Text{EN: "Edit this place"},
	PlannedHere:    types.Text{EN: "To plant"},
	PlannedHelp:    types.Text{EN: "Plant these here. Some may be growing already; the note says how many more."},
	Flowers:        types.Text{EN: "When it flowers"},
	FlowersHelp:    types.Text{EN: "Each row is a plant growing here or going in, coloured in the months it blooms."},
	Protect:        types.Text{EN: "Protect"},
	ProtectHelp:    types.Text{EN: "Leave these. They grow here."},
	Pull:           types.Text{EN: "Pull"},
	PullHelp:       types.Text{EN: "Take these out, root and all."},
	Careful:        types.Text{EN: "Careful"},
	CarefulHelp:    types.Text{EN: "Wear gloves near these."},
	NothingHere:    types.Text{EN: "Nothing listed."},
	Plant:          types.Text{EN: "Plant"},

	TallWide: types.Text{EN: "{height} tall, {width} wide"},
	Tall:     types.Text{EN: "{height} tall"},
	ToPlant:  types.Text{EN: "{count} to plant"},
	ToPull:   types.Text{EN: "{count} to pull"},

	Stations: stationWords(),
}

// stationWords is stationName as copy: a saint's name has a Spanish form,
// San José for St. Joseph, which the translation gives.
func stationWords() map[string]types.Text {
	out := map[string]types.Text{}
	for anchor, name := range stationName {
		out[anchor] = types.Text{EN: name}
	}

	return out
}

type cardRow struct {
	Slug    string
	Name    types.Text
	Purpose types.Text

	// Summary is a band's plants in a few words, on its parent's card:
	// "4 to plant · 1 to pull".
	Summary page.List
}

// plantLine is one plant on a card: on the To plant list, in a panel, or a
// row of the flowering calendar.
type plantLine struct {
	SpeciesID  string
	Action     listingbus.Action
	Slug       string
	Name       types.Text
	Scientific string
	Note       types.Text
	Size       any       // a page.Phrase, or nil when no size is recorded
	Swatch     string    // the first colour, for the calendar; "" for none
	Bloom      []bool    // twelve, January first
	BloomWords page.List // "March to May", for a screen reader beside the cells

	// Thumb is the id of the plant's flower photo, or its full-size one,
	// for the To plant list: a planter imagining the bed needs to see what is
	// going in. Empty when it has neither checked yet. Shown as a square,
	// cropped by the stylesheet, so its own size is not needed.
	Thumb string
}

type cardView struct {
	Copy cardWording

	Name, Purpose, Conditions, PhotoPoint types.Text

	// Parent is the place this one is inside, for a band; zero otherwise.
	Parent *cardRow
	Bands  []cardRow

	// Station and StationURL are set for a station's space.
	Station    types.Text
	StationURL string

	// The plants listed here. Listed is false when there are none at all,
	// and the card then says the plants are not listed yet.
	//
	// Planned is the "To plant" list: listings with planting still to do,
	// whether none of the plant is in yet or more is going in beside what is
	// growing. Calendar is everything protected here, planted or to plant,
	// so a bed keeps its calendar the day its planting is done.
	Listed                 bool
	Planned, Calendar      []plantLine
	Protect, Pull, Careful []plantLine
	Months                 []types.Text // the calendar's columns, headed by their initials

	// EditURL is set only for a signed-in steward.
	EditURL string

	// Adds is each list's "+ Add", set only for a signed-in steward.
	Adds *cardAdds
}

// cardAdd is one list's "+ Add": a button under the list that opens a form
// in a popover, posting to the Plants screen's own handler (setPlant) with
// the list's action fixed and a word asking to come back to the card.
//
// A popover rather than a dialog because the browser opens and closes it
// from the button's popovertarget alone, with no script, and the pages have
// none (page.Policy). A browser too old for popover shows the form in place
// under its list, which still works.
type cardAdd struct {
	ID      string // the popover's id
	Title   string
	Help    string
	Action  listingbus.Action
	Planned bool
	PostURL string
	Species []option // the plants not yet listed here, by name

	// Growing is, for the To plant form only, the plants protected here and
	// not on To plant: "plant more" of something already growing. Choosing
	// one keeps its note unless a new one is typed (setPlant).
	Growing []option
}

type cardAdds struct {
	Planned, Protect, Pull, Careful cardAdd

	// None is true when every plant is already listed here, and there is
	// nothing to offer.
	None bool
}

// addsFor builds the four forms. A plant already listed here is left out:
// saving a listing replaces it, and a plant moved from Protect to Pull by a
// form that looked like adding would be the mistake the panels exist to
// prevent. Moving one is done on the Plants screen, where its row says what
// it is now.
func addsFor(p placebus.Place, species map[types.ID]speciesbus.Species, listed []listingbus.Listing) *cardAdds {
	here := map[types.ID]bool{}
	var growing []option

	for _, l := range listed {
		here[l.SpeciesID] = true

		if sp, ok := species[l.SpeciesID]; ok && l.Action == listingbus.Protect && !l.Planned {
			growing = append(growing, plantOption(sp, false))
		}
	}

	var opts []option
	for id, sp := range species {
		if !here[id] {
			opts = append(opts, plantOption(sp, false))
		}
	}

	byName := func(x, y option) int { return cmp.Compare(strings.ToLower(x.Label), strings.ToLower(y.Label)) }
	slices.SortFunc(opts, byName)
	slices.SortFunc(growing, byName)

	post := "/steward/places/" + p.ID.String() + "/plants"
	add := func(id, title, help string, act listingbus.Action, planned bool) cardAdd {
		return cardAdd{ID: id, Title: title, Help: help, Action: act, Planned: planned, PostURL: post, Species: opts}
	}

	toPlant := add("add-planned", "Add to To plant", "Not in the ground yet, or more are going in: it goes on To plant and Protect until a steward marks it growing. Say how many in the note.", listingbus.Protect, true)
	toPlant.Growing = growing

	return &cardAdds{
		Planned: toPlant,
		Protect: add("add-protect", "Add to Protect", "Found growing here, and to be left alone.", listingbus.Protect, false),
		Pull:    add("add-pull", "Add to Pull", "Volunteers see it under Pull straight away, so list only what you are sure of.", listingbus.Pull, false),
		Careful: add("add-careful", "Add to Careful", "It stays or goes as the note says, and volunteers handle it with gloves on.", listingbus.Careful, false),
		None:    len(opts) == 0 && len(growing) == 0,
	}
}

func (a app) card(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	p, err := a.places.BySlug(ctx, r.PathValue("slug"))
	switch {
	case errors.Is(err, placebus.ErrNotFound):
		// A page rather than a bare 404, because the likely way here is a
		// link typed from a stake or a guide, by somebody standing in the
		// garden who needs the list more than an error.
		a.render.Render(w, r, http.StatusNotFound, "place-missing", cardWords)

		return
	case err != nil:
		a.fail(w, r, "reading a place for its card", err)

		return
	}

	v := cardView{
		Copy: cardWords,
		Name: p.Name, Purpose: p.Purpose, Conditions: p.Conditions, PhotoPoint: p.PhotoPoint,
	}

	if !p.TopLevel() {
		parent, err := a.places.ByID(ctx, p.ParentID)
		if err != nil {
			a.fail(w, r, "reading the place a band is inside", err)

			return
		}

		v.Parent = &cardRow{Slug: parent.Slug, Name: parent.Name}
	}

	bands, err := a.places.Children(ctx, p.ID)
	if err != nil {
		a.fail(w, r, "reading the places inside a place", err)

		return
	}

	species, err := a.speciesByID(r)
	if err != nil {
		a.fail(w, r, "reading species for a card", err)

		return
	}

	for _, b := range bands {
		row := cardRow{Slug: b.Slug, Name: b.Name, Purpose: b.Purpose}

		listed, err := a.listings.ForPlace(ctx, b.ID)
		if err != nil {
			a.fail(w, r, "reading what is listed in a band", err)

			return
		}

		row.Summary = summary(listed)
		v.Bands = append(v.Bands, row)
	}

	listed, err := a.listings.ForPlace(ctx, p.ID)
	if err != nil {
		a.fail(w, r, "reading what is listed at a place", err)

		return
	}

	v.Listed = len(listed) > 0
	v.Months = page.Months()

	for _, l := range listed {
		sp, ok := species[l.SpeciesID]
		if !ok {
			continue // removed between the two reads; the reference makes it rare
		}

		line := lineOf(sp, l)

		// A planned plant is always protected (listingbus.Set), so it is in
		// the Protect panel as well as on the To plant list: the panel is
		// what a weeder reads a month later, and "leave this" has to include
		// everything that was put in on purpose.
		switch l.Action {
		case listingbus.Protect:
			v.Protect = append(v.Protect, line)
		case listingbus.Pull:
			v.Pull = append(v.Pull, line)
		case listingbus.Careful:
			v.Careful = append(v.Careful, line)
		}

		if l.Planned {
			if line, err = a.withThumb(r, line, sp); err != nil {
				a.fail(w, r, "reading a planned plant's photos", err)

				return
			}

			v.Planned = append(v.Planned, line)
		}

		if l.Action == listingbus.Protect && !sp.Bloom.Zero() {
			v.Calendar = append(v.Calendar, line)
		}
	}

	for _, list := range [][]plantLine{v.Planned, v.Calendar, v.Protect, v.Pull, v.Careful} {
		slices.SortStableFunc(list, func(x, y plantLine) int {
			return cmp.Compare(strings.ToLower(x.Name.In(types.English)), strings.ToLower(y.Name.In(types.English)))
		})
	}

	if name, ok := cardWords.Stations[p.TrailAnchor]; ok {
		v.Station = name
		v.StationURL = trailURL + "#" + p.TrailAnchor
	}

	if _, ok := mid.StewardFrom(ctx); ok {
		v.EditURL = "/steward/places/" + p.ID.String() + "/edit"
		v.Adds = addsFor(p, species, listed)
	}

	a.render.Render(w, r, http.StatusOK, "place", v)
}

// speciesByID is every species, by identifier: one read for the whole card,
// rather than one per listing.
func (a app) speciesByID(r *http.Request) (map[types.ID]speciesbus.Species, error) {
	all, err := a.species.All(r.Context())
	if err != nil {
		return nil, err
	}

	out := make(map[types.ID]speciesbus.Species, len(all))
	for _, sp := range all {
		out[sp.ID] = sp
	}

	return out, nil
}

func lineOf(sp speciesbus.Species, l listingbus.Listing) plantLine {
	line := plantLine{
		SpeciesID: sp.ID.String(), Action: l.Action,
		Slug: sp.Slug, Name: sp.Common, Scientific: sp.Scientific, Note: l.Note,
		Bloom: make([]bool, 12), BloomWords: page.Run(sp.Bloom),
	}

	if len(sp.Swatches) > 0 {
		line.Swatch = sp.Swatches[0]
	}

	for m := time.January; m <= time.December; m++ {
		line.Bloom[m-1] = sp.Bloom.Has(m)
	}

	switch h, w := length(sp.Height), length(sp.Width); {
	case h != nil && w != nil:
		line.Size = page.Put(cardWords.TallWide, "height", h, "width", w)
	case h != nil:
		line.Size = page.Put(cardWords.Tall, "height", h)
	}

	return line
}

// length is a size as the card writes it, or nil for one not recorded.
func length(s speciesbus.Size) any {
	n, feet := s.Amount()
	if n == "" {
		return nil
	}

	return page.Length(n, feet)
}

// summary is a band's listings in a few words for its row on the parent's
// card.
func summary(listed []listingbus.Listing) page.List {
	var planned, pull int
	for _, l := range listed {
		if l.Planned {
			planned++
		}

		if l.Action == listingbus.Pull {
			pull++
		}
	}

	out := page.List{Sep: " · "}
	if planned > 0 {
		out.Items = append(out.Items, page.Put(cardWords.ToPlant, "count", planned))
	}

	if pull > 0 {
		out.Items = append(out.Items, page.Put(cardWords.ToPull, "count", pull))
	}

	return out
}

// withThumb adds the picture a planter is shown beside a planned plant: its
// flower if it has a checked one, else the grown plant.
func (a app) withThumb(r *http.Request, line plantLine, sp speciesbus.Species) (plantLine, error) {
	photos, err := a.photos.ForSpecies(r.Context(), sp.ID)
	if err != nil {
		return line, err
	}

	for _, k := range []photobus.Kind{photobus.Flower, photobus.Mature} {
		if p, ok := photobus.Best(photos, k); ok {
			line.Thumb = p.ID.String()

			break
		}
	}

	return line, nil
}
