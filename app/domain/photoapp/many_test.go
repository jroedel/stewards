package photoapp_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/stewards/business/domain/photo/photobus"
)

// Checking many at once: a list from the address, ticked, by the short ids
// the screens show or whole ones; the ticked ones checked by one press, the
// unticked left; with no list, the whole queue unticked; and nothing ticked
// checks nothing.
func TestCheckingManyAtOnce(t *testing.T) {
	s := serve(t)

	ref := s.add(t, photobus.Leaf, true, 1)
	a := s.add(t, photobus.Leaf, false, 2)
	b := s.add(t, photobus.Flower, false, 3)
	c := s.add(t, photobus.Young, false, 4)

	if w := s.get("/steward/check/many", false); w.Code == http.StatusOK {
		t.Error("the page is open to somebody signed out")
	}

	// a by the eight characters a screen shows and again whole, b whole,
	// ref checked already, and one that names nothing: two not here.
	list := a[:8] + "," + strings.ToUpper(b) + "," + ref[:8] + ",deadbeef," + a
	page := s.get("/steward/check/many?ids="+url.QueryEscape(list), true).Body.String()
	contains(t, "the page from a list", page,
		"2 photos not checked yet.", "Untick any that do not show their plant",
		"Brazos penstemon",
		`name="id" value="`+a+`" checked`, `name="id" value="`+b+`" checked`,
		"2 of the photos in the list are not here")
	if strings.Contains(page, c) {
		t.Error("a photo the list does not name is on the page")
	}

	if n := strings.Count(page, `name="id" value="`+a+`"`); n != 1 {
		t.Errorf("a photo named twice is on the page %d times", n)
	}

	// No list: the whole queue, nothing ticked.
	page = s.get("/steward/check/many", true).Body.String()
	contains(t, "the page with no list", page, "3 photos not checked yet.", `name="id" value="`+c+`">`)
	if strings.Contains(page, `" checked>`) {
		t.Error("with no list, a photo is ticked")
	}

	// a ticked, b unticked.
	w := s.post("/steward/check/many", url.Values{"ids": {list}, "id": {a}})
	if w.Code != http.StatusOK {
		t.Fatalf("Check: %d", w.Code)
	}

	// b is still there and no longer ticked: the steward left it, and a
	// second press must not check it. a, just checked, is not counted
	// among the list's photos that are not here.
	page = w.Body.String()
	contains(t, "the page after Check", page,
		"Checked 1 photo. Volunteers see it now.", "1 photo not checked yet.",
		"Tick the ones that show their plant", `name="id" value="`+b+`">`,
		"2 of the photos in the list are not here")
	if strings.Contains(page, `" checked>`) {
		t.Error("after a press, a photo left on the page is ticked again")
	}

	for id, want := range map[string]bool{a: true, b: false, c: false} {
		p, err := s.photos.ByID(t.Context(), mustID(t, id))
		if err != nil {
			t.Fatal(err)
		}

		if p.Checked != want {
			t.Errorf("photo %s checked %v, want %v", id[:8], p.Checked, want)
		}
	}

	// Sent again, it checks nothing new and says the same.
	if w := s.post("/steward/check/many", url.Values{"ids": {list}, "id": {a}}); !strings.Contains(w.Body.String(), "Checked 1 photo.") {
		t.Errorf("sent twice: %s", w.Body.String())
	}

	// Nothing ticked.
	contains(t, "the page with nothing ticked", s.post("/steward/check/many", url.Values{"ids": {list}}).Body.String(), "Nothing was checked: no photo was ticked.")

	// A photo checked here is on the card.
	if w := s.get("/photos/"+a+"/small.jpg", false); w.Code != http.StatusOK {
		t.Errorf("signed out, a photo checked from the page: %d", w.Code)
	}

	// The list's last photo checked: the page says so.
	s.post("/steward/check/many", url.Values{"ids": {list}, "id": {b}})
	contains(t, "the list all checked", s.get("/steward/check/many?ids="+url.QueryEscape(list), true).Body.String(), "None of these photos is waiting to be checked.")
}
