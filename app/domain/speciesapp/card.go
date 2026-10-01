package speciesapp

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
)

// This file is the species card a volunteer opens: /plants/<slug>. One record
// read two ways (phase-1-plan.md), and the way is a real link, ?view=weeding,
// rather than a tab drawn by script: there is no script on these pages, and a
// link can be shared with somebody standing in the bed.
//
// Planting leads with what a planter is imagining: flower colour, the bloom
// strip, mature size, light and water. Weeding leads with what a weeder is
// looking at: the young plant and its leaf, and what to do with it in each
// place. Both end with "Where it grows here" (design.md, "Keep from the first
// mockups"), the way back to the places.
//
// The photos are not here yet. Each view says which kinds it is missing, in
// the words design.md gives -- "No young-plant photo yet" -- which is the
// stewards' photo list as much as the volunteer's honest answer.

// CardRoutes mounts the species card: public, and mounted whether or not
// sign-in is configured.
func CardRoutes(mux *http.ServeMux, cfg Config) {
	a := newApp(cfg)

	mux.HandleFunc("GET /plants/{slug}", a.card)
}

type cardWording struct {
	Back, Planting, Weeding, NotConfirmed, CheckedAgainst, Sources,
	Flower, Blooms, NoBloom, Size, Tall, Wide, Light, Water, Note,
	WhereItGrows, NotListedAnywhere, Planned, NotSure, Edit types.Text

	Actions  map[listingbus.Action]types.Text
	Statuses map[speciesbus.Status]types.Text
	Lights   map[speciesbus.Light]types.Text
	Waters   map[speciesbus.Water]types.Text
	Missing  map[string]types.Text
}

// The card's own words. English, with Spanish to be written by a native
// speaker (design.md principle 6); until then say marks the English.
var cardWords = cardWording{
	Back:              types.Text{EN: "All places"},
	Planting:          types.Text{EN: "Planting"},
	Weeding:           types.Text{EN: "Weeding"},
	NotConfirmed:      types.Text{EN: "Not yet confirmed"},
	CheckedAgainst:    types.Text{EN: "ID checked against"},
	Sources:           types.Text{EN: "Sources so far"},
	Flower:            types.Text{EN: "Flower"},
	Blooms:            types.Text{EN: "Blooms"},
	NoBloom:           types.Text{EN: "No bloom months recorded yet."},
	Size:              types.Text{EN: "Mature size"},
	Tall:              types.Text{EN: "tall"},
	Wide:              types.Text{EN: "wide"},
	Light:             types.Text{EN: "Light"},
	Water:             types.Text{EN: "Water"},
	Note:              types.Text{EN: "For planters"},
	WhereItGrows:      types.Text{EN: "Where it grows here"},
	NotListedAnywhere: types.Text{EN: "Not listed at any place yet."},
	Planned:           types.Text{EN: "Planned"},
	NotSure:           types.Text{EN: "Not sure it is this one? Leave it."},
	Edit:              types.Text{EN: "Edit this plant"},

	Actions: map[listingbus.Action]types.Text{
		listingbus.Protect: {EN: "Protect"},
		listingbus.Pull:    {EN: "Pull"},
		listingbus.Careful: {EN: "Careful: wear gloves"},
	},
	Statuses: map[speciesbus.Status]types.Text{
		speciesbus.StatusNative:   {EN: "Native"},
		speciesbus.StatusCultivar: {EN: "Native cultivar or hybrid"},
		speciesbus.StatusAdapted:  {EN: "Adapted"},
		speciesbus.StatusEdible:   {EN: "Edible or herb"},
		speciesbus.StatusInvasive: {EN: "Invasive"},
	},
	Lights: map[speciesbus.Light]types.Text{
		speciesbus.FullSun: {EN: "Full sun"}, speciesbus.PartShade: {EN: "Part shade"}, speciesbus.Shade: {EN: "Shade"},
	},
	Waters: map[speciesbus.Water]types.Text{
		speciesbus.Dry: {EN: "Dry"}, speciesbus.Moist: {EN: "Moist"}, speciesbus.Wet: {EN: "Wet"},
	},
	Missing: map[string]types.Text{
		"young":  {EN: "No young-plant photo yet."},
		"leaf":   {EN: "No leaf photo yet."},
		"flower": {EN: "No flower photo yet."},
		"mature": {EN: "No full-size photo yet."},
	},
}

type grows struct {
	Slug    string
	Name    types.Text
	Action  types.Text
	Pull    bool
	Planned bool
	Note    types.Text
}

type cardView struct {
	Copy    cardWording
	Weeding bool // which view

	Name       types.Text
	Scientific string
	Status     types.Text
	Invasive   bool
	Confirmed  bool
	Sources    []speciesbus.Source

	FlowerColor types.Text
	Swatches    []string
	Months      []monthCell
	BloomWords  string
	Height      string
	Width       string
	Light       []types.Text
	Water       []types.Text
	Note        types.Text

	Missing []types.Text
	Grows   []grows

	Slug, EditURL string
}

type monthCell struct {
	Letter string
	On     bool
	Swatch string
}

func (a app) card(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sp, err := a.species.BySlug(ctx, r.PathValue("slug"))
	switch {
	case errors.Is(err, speciesbus.ErrNotFound):
		a.render.Render(w, r, http.StatusNotFound, "plant-missing", cardWords)

		return
	case err != nil:
		a.fail(w, r, "reading a species for its card", err)

		return
	}

	v := cardView{
		Copy: cardWords, Weeding: r.URL.Query().Get("view") == "weeding",
		Slug: sp.Slug, Name: sp.Common, Scientific: sp.Scientific,
		Status: cardWords.Statuses[sp.Status], Invasive: sp.Status == speciesbus.StatusInvasive,
		Confirmed: sp.Confirmed, Sources: sp.Sources,
		FlowerColor: sp.FlowerColor, Swatches: sp.Swatches, BloomWords: sp.Bloom.String(),
		Height: sp.Height.String(), Width: sp.Width.String(), Note: sp.Note,
	}

	letters := map[types.Lang]string{types.English: "JFMAMJJASOND", types.Spanish: "EFMAMJJASOND"}[mid.LangFrom(ctx)]
	swatch := "#2B3990"
	if len(sp.Swatches) > 0 {
		swatch = sp.Swatches[0]
	}

	for m := time.January; m <= time.December; m++ {
		v.Months = append(v.Months, monthCell{Letter: letters[m-1 : m], On: sp.Bloom.Has(m), Swatch: swatch})
	}

	for _, l := range []speciesbus.Light{speciesbus.FullSun, speciesbus.PartShade, speciesbus.Shade} {
		if sp.Light&l != 0 {
			v.Light = append(v.Light, cardWords.Lights[l])
		}
	}

	for _, wa := range []speciesbus.Water{speciesbus.Dry, speciesbus.Moist, speciesbus.Wet} {
		if sp.Water&wa != 0 {
			v.Water = append(v.Water, cardWords.Waters[wa])
		}
	}

	if v.Weeding {
		v.Missing = []types.Text{cardWords.Missing["young"], cardWords.Missing["leaf"]}
	} else {
		v.Missing = []types.Text{cardWords.Missing["flower"], cardWords.Missing["mature"]}
	}

	if v.Grows, err = a.where(r, sp); err != nil {
		a.fail(w, r, "reading where a species grows", err)

		return
	}

	if _, ok := mid.StewardFrom(ctx); ok {
		v.EditURL = "/steward/species/" + sp.ID.String() + "/edit"
	}

	a.render.Render(w, r, http.StatusOK, "plant", v)
}

// where is every place the species is listed, with what to do there, in the
// places' own list order.
func (a app) where(r *http.Request, sp speciesbus.Species) ([]grows, error) {
	listed, err := a.listings.ForSpecies(r.Context(), sp.ID)
	if err != nil || len(listed) == 0 {
		return nil, err
	}

	places, err := a.places.All(r.Context())
	if err != nil {
		return nil, err
	}

	order := map[types.ID]int{}
	byID := map[types.ID]placebus.Place{}

	for i, p := range places {
		order[p.ID], byID[p.ID] = i, p
	}

	slices.SortFunc(listed, func(x, y listingbus.Listing) int { return cmp.Compare(order[x.PlaceID], order[y.PlaceID]) })

	var out []grows

	for _, l := range listed {
		p, ok := byID[l.PlaceID]
		if !ok {
			continue
		}

		name := p.Name

		// A band is named with the place it is in, "Rain garden: Inflow
		// band", since "Inflow band" alone could be anywhere.
		if parent, ok := byID[p.ParentID]; ok && !p.TopLevel() {
			name = types.Text{EN: parent.Name.EN + ": " + p.Name.EN}
			if parent.Name.ES != "" && p.Name.ES != "" {
				name.ES = parent.Name.ES + ": " + p.Name.ES
			}
		}

		out = append(out, grows{
			Slug: p.Slug, Name: name, Action: cardWords.Actions[l.Action],
			Pull: l.Action == listingbus.Pull, Planned: l.Planned, Note: l.Note,
		})
	}

	return out, nil
}
