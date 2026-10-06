package speciesapp

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
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
// Each view shows its own kinds of photo (design.md §5): planting the flower,
// the grown plant and its winter look; weeding the seedling, the leaf and the
// winter look, since a seed head is what gets pulled by mistake in February.
// Only checked photos, the best of each kind by photobus.Best. A kind with
// none says so in design.md's words -- "No young-plant photo yet" -- which is
// the stewards' photo list as much as the volunteer's honest answer.

// CardRoutes mounts the species card: public, and mounted whether or not
// sign-in is configured.
func CardRoutes(mux *http.ServeMux, cfg Config) {
	a := newApp(cfg)

	mux.HandleFunc("GET /plants/{slug}", a.card)
	mux.HandleFunc("GET /plants/{slug}/photos/{id}", a.photo)
}

type cardWording struct {
	Back, Planting, Weeding, NotConfirmed, CheckedAgainst, Sources,
	Flower, Blooms, NoBloom, Size, Tall, Wide, Light, Water, Note,
	WhereItGrows, NotListedAnywhere, Planned, NotSure, Edit types.Text

	Actions  map[listingbus.Action]types.Text
	Statuses map[speciesbus.Status]types.Text
	Lights   map[speciesbus.Light]types.Text
	Waters   map[speciesbus.Water]types.Text
	Missing  map[photobus.Kind]types.Text
	Kinds    map[photobus.Kind]types.Text

	OurPhoto, PhotoBy, Source, Photos types.Text

	// NotHere is where one of our photos was taken off the property, when
	// nobody said where: still never to be taken for this garden.
	NotHere types.Text

	// The photo on a page of its own, to look closer.
	ZoomIn, Pinch, ByItself types.Text
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
	Photos:            types.Text{EN: "Photos of this plant"},
	OurPhoto:          types.Text{EN: "Our photo"},
	NotHere:           types.Text{EN: "not taken here"},
	PhotoBy:           types.Text{EN: "Photo:"},
	Source:            types.Text{EN: "source"},
	ZoomIn:            types.Text{EN: "Zoom in:"},
	Pinch:             types.Text{EN: "Pinch to zoom in."},
	ByItself:          types.Text{EN: "Open the photo by itself"},

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
	Missing: map[photobus.Kind]types.Text{
		photobus.Young:  {EN: "No young-plant photo yet."},
		photobus.Leaf:   {EN: "No leaf photo yet."},
		photobus.Flower: {EN: "No flower photo yet."},
		photobus.Mature: {EN: "No full-size photo yet."},
		photobus.Winter: {EN: "No winter photo yet."},
	},
	Kinds: map[photobus.Kind]types.Text{
		photobus.Young:  {EN: "Young plant"},
		photobus.Leaf:   {EN: "Leaf"},
		photobus.Flower: {EN: "Flower"},
		photobus.Mature: {EN: "Full size"},
		photobus.Winter: {EN: "In winter"},
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

	Figures []figure
	Missing []types.Text
	Grows   []grows

	Slug, EditURL, PhotosURL string
}

// figure is one photo on the card, with what a volunteer is told about it.
type figure struct {
	ID                 string
	Width, Height      int // the small picture's, which reserves the space
	SmallW, LargeW     int
	Kind, Alt          types.Text
	Credit, Where      types.Text
	When               string
	License, SourceURL string
}

// viewKinds is the photos each view shows, in the order it shows them.
var viewKinds = map[bool][]photobus.Kind{
	false: {photobus.Flower, photobus.Mature, photobus.Winter},
	true:  {photobus.Young, photobus.Leaf, photobus.Winter},
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

	places, err := a.places.All(ctx)
	if err != nil {
		a.fail(w, r, "listing places for a species card", err)

		return
	}

	photos, err := a.photos.ForSpecies(ctx, sp.ID)
	if err != nil {
		a.fail(w, r, "reading a species' photos", err)

		return
	}

	names := placeNames(places)

	for _, k := range viewKinds[v.Weeding] {
		p, ok := photobus.Best(photos, k)
		if !ok {
			v.Missing = append(v.Missing, cardWords.Missing[k])

			continue
		}

		v.Figures = append(v.Figures, figureOf(p, sp, names))
	}

	if v.Grows, err = a.where(r, sp, places, names); err != nil {
		a.fail(w, r, "reading where a species grows", err)

		return
	}

	if _, ok := mid.StewardFrom(ctx); ok {
		v.EditURL = "/steward/species/" + sp.ID.String() + "/edit"
		v.PhotosURL = "/steward/species/" + sp.ID.String() + "/photos"
	}

	a.render.Render(w, r, http.StatusOK, "plant", v)
}

// where is every place the species is listed, with what to do there, in the
// places' own list order.
func (a app) where(r *http.Request, sp speciesbus.Species, places []placebus.Place, names map[types.ID]types.Text) ([]grows, error) {
	listed, err := a.listings.ForSpecies(r.Context(), sp.ID)
	if err != nil || len(listed) == 0 {
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

		out = append(out, grows{
			Slug: p.Slug, Name: names[p.ID], Action: cardWords.Actions[l.Action],
			Pull: l.Action == listingbus.Pull, Planned: l.Planned, Note: l.Note,
		})
	}

	return out, nil
}

// placeNames is every place's name as the card says it. A band is named with
// the place it is in, "Rain garden: Inflow band", since "Inflow band" alone
// could be anywhere.
func placeNames(places []placebus.Place) map[types.ID]types.Text {
	byID := map[types.ID]placebus.Place{}
	for _, p := range places {
		byID[p.ID] = p
	}

	names := map[types.ID]types.Text{}

	for _, p := range places {
		name := p.Name

		if parent, ok := byID[p.ParentID]; ok && !p.TopLevel() {
			name = types.Text{EN: parent.Name.EN + ": " + p.Name.EN}
			if parent.Name.ES != "" && p.Name.ES != "" {
				name.ES = parent.Name.ES + ": " + p.Name.ES
			}
		}

		names[p.ID] = name
	}

	return names
}

// figureOf is a photo as the card shows it, with its credit: "Our photo ·
// Rain garden: Inflow band · April 2027", "Our photo · Pedernales Falls State
// Park · May 2027" for one taken elsewhere, or the author, licence and source
// of a borrowed one, which its licence requires beside the picture.
func figureOf(p photobus.Photo, sp speciesbus.Species, names map[types.ID]types.Text) figure {
	kind := cardWords.Kinds[p.Kind]

	f := figure{
		ID: p.ID.String(), Width: p.Small.Width, Height: p.Small.Height,
		SmallW: p.Small.Width, LargeW: p.Large.Width,
		Kind: kind,
		Alt:  types.Text{EN: sp.Common.EN + ": " + strings.ToLower(kind.EN)},
		When: takenWords(p.TakenYear, p.TakenMonth),
	}

	if sp.Common.ES != "" && kind.ES != "" {
		f.Alt.ES = sp.Common.ES + ": " + strings.ToLower(kind.ES)
	}

	if p.Source == photobus.Borrowed {
		f.Credit = types.Text{EN: p.Credit}
		f.License, f.SourceURL = p.License, p.SourceURL
	} else {
		f.Credit = cardWords.OurPhoto
		if p.Credit != "" {
			f.Credit = types.Text{EN: p.Credit}
		}
		f.Where = names[p.PlaceID]

		// A volunteer looking at a park's plant must not think it grows
		// here, so a photo from elsewhere says where, or that it was not
		// here.
		if p.Elsewhere {
			f.Where = cardWords.NotHere
			if p.TakenWhere != "" {
				f.Where = types.Text{EN: p.TakenWhere}
			}
		}
	}

	return f
}

// takenWords is "April 2027", "April", "2027" or nothing. English month
// names, like the rest of the card's words until a native speaker writes the
// Spanish.
func takenWords(year, month int) string {
	var parts []string
	if month >= 1 && month <= 12 {
		parts = append(parts, time.Month(month).String())
	}

	if year != 0 {
		parts = append(parts, strconv.Itoa(year))
	}

	return strings.Join(parts, " ")
}

// ------------------------------------------------------------------ one photo, closer

// photoView is one of a plant's photos on a page of its own, at full size, to
// look at the hairs on a stem or the teeth of a leaf, which a card's picture
// is too small to show.
type photoView struct {
	Copy    cardWording
	Name    types.Text
	Slug    string
	Weeding bool // which view of the card to go back to

	Figure     figure
	LargeW     int
	LargeH     int
	Full       string // full.jpg, or full.png for a PNG
	EditURL    string // a steward's, to check it or take it down
	NotChecked bool   // a steward looking at one volunteers do not see
}

// photo is the page a card's photo opens. There is no script on it: the
// phone's own pinch-zoom is the zoom, and it is what a volunteer already
// knows. What the page adds is the picture with the most pixels in it, and
// the large one underneath while that arrives -- a few megabytes on a
// garden's signal is a few seconds of something rather than of nothing.
//
// The same rule as the pictures themselves: a checked photo is anybody's, an
// unchecked one a steward's. A photo that is not there, or not to be shown,
// goes back to the card, which is where the link that led here was, rather
// than to a page saying a photo exists that cannot be seen.
func (a app) photo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	sp, err := a.species.BySlug(ctx, r.PathValue("slug"))
	switch {
	case errors.Is(err, speciesbus.ErrNotFound):
		a.render.Render(w, r, http.StatusNotFound, "plant-missing", cardWords)

		return
	case err != nil:
		a.fail(w, r, "reading a species for one of its photos", err)

		return
	}

	weeding := r.URL.Query().Get("view") == "weeding"

	back := "/plants/" + sp.Slug
	if weeding {
		back += "?view=weeding"
	}

	photos, err := a.photos.ForSpecies(ctx, sp.ID)
	if err != nil {
		a.fail(w, r, "reading a species' photos", err)

		return
	}

	_, steward := mid.StewardFrom(ctx)

	i := slices.IndexFunc(photos, func(p photobus.Photo) bool { return p.ID.String() == r.PathValue("id") })
	if i < 0 || (!photos[i].Checked && !steward) {
		http.Redirect(w, r, back, http.StatusFound)

		return
	}

	p := photos[i]

	places, err := a.places.All(ctx)
	if err != nil {
		a.fail(w, r, "listing places for a photo", err)

		return
	}

	v := photoView{
		Copy: cardWords, Name: sp.Common, Slug: sp.Slug, Weeding: weeding,
		Figure: figureOf(p, sp, placeNames(places)),
		LargeW: p.Large.Width, LargeH: p.Large.Height,
		Full:       photobus.ServedName(photobus.Full, p.Format),
		NotChecked: !p.Checked,
	}

	if steward {
		v.EditURL = "/steward/photos/" + p.ID.String() + "/edit"
	}

	a.render.Render(w, r, http.StatusOK, "plant-photo", v)
}
