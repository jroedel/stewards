package photoapp

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
)

// The check queue: every unchecked photo, across every plant, one at a time,
// each beside the plant's checked photo of the same kind, with one button
// that checks it and goes on to the next (photobus.Unchecked has why).
//
// It is a page of plain forms and links, and works as one. check.mjs makes
// it quick: an unchecked picture is served no-store (file, above), so a
// phone cannot be handed the next one from its cache, and the script fetches
// the next photo's page while the steward is still looking at this one and
// swaps it in place. Without the script each Yes is a page load and the
// picture arrives after it, which is slower and no less right.

// CheckPath is the queue. The stewards' front page links to it.
const CheckPath = "/steward/check"

// checkScript is the queue's one module, served from beside the queue. One
// file, so its ETag is the whole of what a phone runs (the inbox's
// scriptTags has why that matters for modules importing each other).
//
//go:embed static/check.mjs
var checkScript []byte

var checkTag = func() string {
	sum := sha256.Sum256(checkScript)

	return `"` + hex.EncodeToString(sum[:])[:16] + `"`
}()

const checkScriptPath = CheckPath + "/static/check.mjs"

func (a app) script(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", checkTag)

	http.ServeContent(w, r, "check.mjs", time.Time{}, bytes.NewReader(checkScript))
}

type checkView struct {
	Count int

	// Done is what the last button did, and Undo the photo it did it to
	// when it can be undone. Problem is why it could not be done.
	Done, Undo, Problem string

	Empty bool

	ID, Name, Scientific, Slug string
	SpeciesID                  string
	Kind, KindWord             string
	Width, Height              int
	InFlower, InFruit          bool
	Caption, Taken             string
	Sources                    []speciesbus.Source

	// Ref is the plant's checked photo of the same kind, the one the card
	// shows: what this one is compared with. Empty when there is none yet.
	Ref          string
	RefW, RefH   int
	Next, Script string
}

// queue shows one photo of the queue: the one ?at= names, or the first.
func (a app) queue(w http.ResponseWriter, r *http.Request) {
	a.showQueue(w, r, http.StatusOK, r.URL.Query().Get("at"), checkView{})
}

func (a app) showQueue(w http.ResponseWriter, r *http.Request, status int, at string, v checkView) {
	ctx := r.Context()

	queue, err := a.photos.Unchecked(ctx)
	if err != nil {
		a.fail(w, r, "listing the photos to check", err)

		return
	}

	// What just happened, as a fixed sentence chosen by a word in the query
	// and the photo's own record, never the query echoed (placeapp's index
	// has why).
	q := r.URL.Query()
	if last, err := types.ParseID(q.Get("last")); err == nil && v.Done == "" {
		if p, err := a.photos.ByID(ctx, last); err == nil {
			if sp, err := a.species.ByID(ctx, p.SpeciesID); err == nil {
				switch {
				case q.Get("done") == "checked" && p.Checked:
					v.Done, v.Undo = "Checked: "+sp.Common.EN+", "+lower(p.Kind.Label())+".", p.ID.String()
				case q.Get("done") == "unchecked" && !p.Checked:
					v.Done = "Not checked any more. It is back in the queue."
				case q.Get("done") == "saved":
					v.Done = "Changes saved."
				}
			}
		}
	}

	v.Count = len(queue)
	if len(queue) == 0 {
		v.Empty = true
		a.render.Render(w, r, status, "steward-check", v)

		return
	}

	i := max(slices.IndexFunc(queue, func(p photobus.Photo) bool { return p.ID.String() == at }), 0)
	p := queue[i]

	sp, err := a.species.ByID(ctx, p.SpeciesID)
	if err != nil {
		a.fail(w, r, "reading the species of a photo to check", err)

		return
	}

	_, names, err := a.placeOptions(ctx, types.ID{})
	if err != nil {
		a.fail(w, r, "naming places for the photos to check", err)

		return
	}

	theirs, err := a.photos.ForSpecies(ctx, sp.ID)
	if err != nil {
		a.fail(w, r, "reading a plant's photos to compare with", err)

		return
	}

	v.ID, v.SpeciesID = p.ID.String(), sp.ID.String()
	v.Name, v.Scientific, v.Slug, v.Sources = sp.Common.EN, sp.Scientific, sp.Slug, sp.Sources
	v.Kind, v.KindWord, v.InFlower, v.InFruit = p.Kind.Label(), lower(p.Kind.Label()), p.InFlower, p.InFruit
	v.Width, v.Height = p.Small.Width, p.Small.Height
	v.Caption = caption(p, names)
	v.Script = checkScriptPath

	if !p.TakenAt.IsZero() {
		v.Taken = p.TakenAt.In(types.Garden).Format("2 January 2006")
	}

	if ref, ok := photobus.Best(theirs, p.Kind); ok {
		v.Ref, v.RefW, v.RefH = ref.ID.String(), ref.Small.Width, ref.Small.Height
	}

	if next, ok := after(queue, i); ok {
		v.Next = CheckPath + "?at=" + next.ID.String()
	}

	a.render.Render(w, r, status, "steward-check", v)
}

// after is the photo after queue[i], going round to the start, or none when
// queue[i] is the only one.
func after(queue []photobus.Photo, i int) (photobus.Photo, bool) {
	if len(queue) < 2 {
		return photobus.Photo{}, false
	}

	return queue[(i+1)%len(queue)], true
}

// check is Yes, and Undo: checked=yes or checked=no for one photo.
//
// Yes goes on to the photo after it, worked out before it leaves the queue,
// as the inbox's sort does. Undo comes back to the photo, which is in the
// queue again.
func (a app) check(w http.ResponseWriter, r *http.Request) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	checked := r.PostFormValue("checked") == "yes"

	queue, err := a.photos.Unchecked(r.Context())
	if err != nil {
		a.fail(w, r, "listing the photos to check", err)

		return
	}

	to := CheckPath + "?at=" + id.String() + "&done=unchecked&last=" + id.String()

	if checked {
		to = CheckPath + "?done=checked&last=" + id.String()

		if i := slices.IndexFunc(queue, func(p photobus.Photo) bool { return p.ID == id }); i >= 0 {
			if next, ok := after(queue, i); ok {
				to += "&at=" + next.ID.String()
			}
		}
	}

	_, err = a.photos.SetChecked(r.Context(), id, checked)

	invalid, isInvalid := errors.AsType[photobus.Invalid](err)

	switch {
	case err == nil:
		http.Redirect(w, r, to, http.StatusSeeOther)
	case errors.Is(err, photobus.ErrNotFound):
		http.Redirect(w, r, CheckPath, http.StatusSeeOther)
	case isInvalid:
		a.showQueue(w, r, http.StatusUnprocessableEntity, id.String(), checkView{
			Problem: "Not checked: " + page.Sentence(invalid.Problem) + " Change it first.",
		})
	default:
		a.fail(w, r, "checking a photo", err)
	}
}

// lower is a kind's label in the middle of a sentence: "leaf close-up".
func lower(s string) string {
	if s == "" {
		return s
	}

	return strings.ToLower(s[:1]) + s[1:]
}
