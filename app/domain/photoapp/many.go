package photoapp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
)

// Checking many at once: a page of photos, grouped by plant, each with a
// tick, and one button that checks every ticked one.
//
// It is for a batch whose ID was already settled somewhere a steward trusts
// more than a look at a thumbnail -- borrowed photos from research-grade
// iNaturalist observations, where the community has agreed on the species --
// so that two hundred of them are one look down a page and one press, rather
// than two hundred trips through the queue. Checking is still a steward's
// word given on a screen (CLAUDE.md, section 6): the page only lays the
// photos out and remembers which were ticked. The API still checks nothing.
//
// Which photos is a list in the address, ?ids=, so that whoever drew it up --
// the steward, or their Claude after reading the observations -- can hand
// the steward a link. Each id may be the eight characters the screens show
// ("Photo cbb9fac4") as well as the whole id: two hundred whole ids are over
// six thousand characters, near where a web server stops reading an
// address, and the short ones are what the steward can read back against
// the queue. A short id that two unchecked photos share is left out and
// counted, never guessed. A photo the list names is ticked; with no list,
// every unchecked photo is shown and none is ticked, so the page is a way to
// look over the queue, not a way past it.

// ManyPath is the page.
const ManyPath = CheckPath + "/many"

// maxMany is the most photos one list may name: more than any batch so far,
// and few enough that the page stays one a phone can scroll.
const maxMany = 500

type manyPhoto struct {
	ID              string
	Width, Height   int
	Kind            string
	InFlower        bool
	InFruit         bool
	Caption, Source string
	Ticked          bool
}

type manyPlant struct {
	Name, Scientific string
	Sources          []speciesbus.Source
	Photos           []manyPhoto
}

type manyView struct {
	Plants []manyPlant
	Count  int

	// Listed is true when the page shows a list from the address; Left is
	// how many of the list are not here -- checked already, gone, or a
	// short id two photos share -- and Over how many past maxMany.
	Listed     bool
	Left, Over int
	IDs        string

	Done, Problem string
}

// many shows the page.
func (a app) many(w http.ResponseWriter, r *http.Request) {
	a.showMany(w, r, http.StatusOK, r.URL.Query().Get("ids"), manyView{})
}

func (a app) showMany(w http.ResponseWriter, r *http.Request, status int, ids string, v manyView) {
	ctx := r.Context()

	queue, err := a.photos.Unchecked(ctx)
	if err != nil {
		a.fail(w, r, "listing the photos to check", err)

		return
	}

	shown := queue
	wanted := splitIDs(ids)

	if len(wanted) > 0 {
		v.Listed, v.IDs = true, strings.Join(wanted, ",")
		if len(wanted) > maxMany {
			v.Over, wanted = len(wanted)-maxMany, wanted[:maxMany]
		}

		var left int
		shown, left = pick(queue, wanted)
		v.Left = left
	}

	plants, err := a.group(ctx, shown, v.Listed)
	if err != nil {
		a.fail(w, r, "laying out the photos to check", err)

		return
	}

	v.Plants, v.Count = plants, len(shown)
	a.render.Render(w, r, status, "steward-check-many", v)
}

// checkMany checks every ticked photo, and shows the page again with what
// it did.
//
// It answers with the page rather than a redirect, which every other form
// here uses. A redirect would have to carry the list back in the address to
// show what is left, and a sentence such as "Checked 192 photos" in an
// address is one anybody can send without having checked anything (the
// queue's done= words are checked against the photo's record for that
// reason, and a count has no record to check against). Sending the form
// twice checks nothing twice: SetChecked does nothing to a photo already
// checked.
func (a app) checkMany(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	ids := r.PostFormValue("ids")

	var checked, refused int
	for _, s := range r.PostForm["id"] {
		id, err := types.ParseID(s)
		if err != nil {
			continue
		}

		switch _, err := a.photos.SetChecked(r.Context(), id, true); {
		case err == nil:
			checked++
		case isRefusal(err):
			refused++
		default:
			a.fail(w, r, "checking photos", err)

			return
		}
	}

	var v manyView
	switch checked {
	case 0:
		v.Problem = "Nothing was checked: no photo was ticked."
	case 1:
		v.Done = "Checked 1 photo. Volunteers see it now."
	default:
		v.Done = "Checked " + strconv.Itoa(checked) + " photos. Volunteers see them now."
	}

	if refused > 0 {
		v.Problem = strconv.Itoa(refused) + " could not be checked as they are. They are below: open each one in the queue to see why."
	}

	a.showMany(w, r, http.StatusOK, ids, v)
}

// isRefusal is an error that is about the photo, not about the server: one
// gone since the page was drawn, or one the rules will not check as it is.
func isRefusal(err error) bool {
	if errors.Is(err, photobus.ErrNotFound) {
		return true
	}

	_, ok := errors.AsType[photobus.Invalid](err)

	return ok
}

// splitIDs is the list in ?ids=: commas or spaces between, each lower-cased,
// anything that is not hex left out, the same one twice kept once.
func splitIDs(s string) []string {
	var out []string

	for f := range strings.FieldsFuncSeq(s, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' }) {
		f = strings.ToLower(strings.TrimSpace(f))
		if len(f) < 8 || len(f) > 32 || strings.Trim(f, "0123456789abcdef") != "" || slices.Contains(out, f) {
			continue
		}

		out = append(out, f)
	}

	return out
}

// pick is the photos of queue that wanted names, in the queue's order, and
// how many of wanted named none of them or more than one.
func pick(queue []photobus.Photo, wanted []string) ([]photobus.Photo, int) {
	var out []photobus.Photo
	left := 0

	for _, w := range wanted {
		var found []photobus.Photo
		for _, p := range queue {
			if strings.HasPrefix(p.ID.String(), w) {
				found = append(found, p)
			}
		}

		if len(found) != 1 {
			left++

			continue
		}

		if !slices.ContainsFunc(out, func(p photobus.Photo) bool { return p.ID == found[0].ID }) {
			out = append(out, found[0])
		}
	}

	slices.SortStableFunc(out, func(x, y photobus.Photo) int {
		return slices.IndexFunc(queue, func(p photobus.Photo) bool { return p.ID == x.ID }) -
			slices.IndexFunc(queue, func(p photobus.Photo) bool { return p.ID == y.ID })
	})

	return out, left
}

// group lays the photos out by plant, in the order they came: the queue's,
// which already keeps a plant's photos together.
func (a app) group(ctx context.Context, photos []photobus.Photo, ticked bool) ([]manyPlant, error) {
	_, names, err := a.placeOptions(ctx, types.ID{})
	if err != nil {
		return nil, err
	}

	var out []manyPlant
	var last types.ID

	for _, p := range photos {
		if len(out) == 0 || p.SpeciesID != last {
			sp, err := a.species.ByID(ctx, p.SpeciesID)
			if err != nil {
				return nil, err
			}

			out = append(out, manyPlant{Name: sp.Common.EN, Scientific: sp.Scientific, Sources: sp.Sources})
			last = p.SpeciesID
		}

		out[len(out)-1].Photos = append(out[len(out)-1].Photos, manyPhoto{
			ID: p.ID.String(), Width: p.Small.Width, Height: p.Small.Height,
			Kind: p.Kind.Label(), InFlower: p.InFlower, InFruit: p.InFruit,
			Caption: caption(p, names), Source: p.SourceURL, Ticked: ticked,
		})
	}

	return out, nil
}
