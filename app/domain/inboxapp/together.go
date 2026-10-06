package inboxapp

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/types"
)

// Sorting photos together: several of one plant chosen in the inbox -- the
// leaf, the flower and the whole plant, taken one after another -- and the
// plant named once, on the first. Each one after opens with that plant
// already chosen, so what is left is what it shows, and Save.
//
// The group is carried in the address of each sort screen, as the ids
// joined by dots, rather than kept anywhere: there is nothing to clean up
// when a steward walks away halfway, and a screen reloaded is the same
// screen. The plant chosen for it travels the same way.

// TogetherPath is where the inbox's form of chosen photos goes.
const TogetherPath = IndexPath + "/together"

// maxGroup is the most photos sorted together: more than a batch, and an
// address of a little over 2 KB.
const maxGroup = 60

// group is the photos chosen to sort together, in the inbox's order.
type group []types.ID

// parseGroup reads a group from an address. What is not an id is dropped,
// and so is a group of one, which is only a photo.
func parseGroup(s string) group {
	var g group

	for part := range strings.SplitSeq(s, ".") {
		if id, err := types.ParseID(part); err == nil && !slices.Contains(g, id) && len(g) < maxGroup {
			g = append(g, id)
		}
	}

	if len(g) < 2 {
		return nil
	}

	return g
}

func (g group) String() string {
	parts := make([]string, len(g))
	for i, id := range g {
		parts[i] = id.String()
	}

	return strings.Join(parts, ".")
}

// after is the photo of the group to sort after id: the next still in the
// inbox, going round to the start, or none when id is the last. open says
// whether a photo is still in the inbox.
func (g group) after(id types.ID, open func(types.ID) bool) (types.ID, bool) {
	at := slices.Index(g, id)

	for i := 1; i < len(g); i++ {
		if c := g[(at+i+len(g))%len(g)]; c != id && open(c) {
			return c, true
		}
	}

	return types.ID{}, false
}

// sortURL is a photo's sort screen, as one of a group with its plant when
// there is one, and with what the last sort did.
func sortURL(id types.ID, g group, plant types.ID, done string) string {
	q := url.Values{}
	if g != nil {
		q.Set("group", g.String())

		if !plant.Zero() {
			q.Set("plant", plant.String())
		}
	}

	if done != "" {
		q.Set("done", done)
	}

	to := IndexPath + "/" + id.String()
	if len(q) > 0 {
		to += "?" + q.Encode()
	}

	return to
}

// inInbox is whether a photo is still to be sorted, waiting or set aside:
// what a group goes round.
func (a app) inInbox(r *http.Request) (func(types.ID) bool, error) {
	waiting, err := a.inbox.Waiting(r.Context())
	if err != nil {
		return nil, err
	}

	aside, err := a.inbox.SetAside(r.Context())
	if err != nil {
		return nil, err
	}

	open := map[types.ID]bool{}
	for _, it := range slices.Concat(waiting, aside) {
		open[it.ID] = true
	}

	return func(id types.ID) bool { return open[id] }, nil
}

// together takes the photos chosen in the inbox to the first one's sort
// screen, as a group. One photo chosen is that photo alone; none is the
// inbox again, saying how.
func (a app) together(w http.ResponseWriter, r *http.Request) {
	waiting, err := a.inbox.Waiting(r.Context())
	if err != nil {
		a.fail(w, r, "listing the inbox", err)

		return
	}

	chosen := r.URL.Query()["photo"]

	// In the inbox's order, whatever order the form sent them in, and only
	// those still there.
	var g group
	for _, it := range waiting {
		if slices.Contains(chosen, it.ID.String()) && len(g) < maxGroup {
			g = append(g, it.ID)
		}
	}

	switch len(g) {
	case 0:
		http.Redirect(w, r, IndexPath+"?done=none-chosen", http.StatusSeeOther)
	case 1:
		http.Redirect(w, r, sortURL(g[0], nil, types.ID{}, ""), http.StatusSeeOther)
	default:
		http.Redirect(w, r, sortURL(g[0], g, types.ID{}, ""), http.StatusSeeOther)
	}
}

// groupOf is the group a sort screen was opened as part of, if it holds
// this photo; and the plant chosen for it so far.
func groupOf(form url.Values, it inboxbus.Item) (group, types.ID) {
	g := parseGroup(form.Get("group"))
	if !slices.Contains(g, it.ID) {
		return nil, types.ID{}
	}

	plant, _ := types.ParseID(form.Get("plant"))

	return g, plant
}
