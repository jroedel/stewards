package stewardapp_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var revokeForm = regexp.MustCompile(`action="/steward/keys/([0-9a-z]+)/revoke"`)

func TestAStewardMakesAndRevokesTheirOwnKeys(t *testing.T) {
	s := serve(t, true)

	w := s.do(http.MethodPost, "/steward/keys", url.Values{"name": {"shed-desktop"}}, s.mine)
	body := w.Body.String()

	if w.Code != http.StatusOK || !strings.Contains(body, `<p class="key">stw_`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("making a key: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}

	// Shown once: the list after it has the name, not the key.
	list := s.do(http.MethodGet, "/steward/keys", nil, s.mine).Body.String()
	if strings.Contains(list, `<p class="key">`) || !strings.Contains(list, "shed-desktop") || !strings.Contains(list, "last used never") {
		t.Error("the list shows the key again, or not its name")
	}

	// Another steward sees none of mine, and cannot revoke them.
	_, theirs := s.steward("other@example.org")
	other := s.do(http.MethodGet, "/steward/keys", nil, theirs)
	if other.Code != http.StatusOK || strings.Contains(other.Body.String(), "shed-desktop") || !strings.Contains(other.Body.String(), "You have no keys.") {
		t.Errorf("another steward's keys page: %d", other.Code)
	}

	id := revokeForm.FindStringSubmatch(list)[1]
	s.do(http.MethodPost, "/steward/keys/"+id+"/revoke", url.Values{}, theirs)

	if list := s.do(http.MethodGet, "/steward/keys", nil, s.mine).Body.String(); !strings.Contains(list, "shed-desktop") {
		t.Error("another steward revoked my key")
	}

	if w := s.do(http.MethodPost, "/steward/keys/"+id+"/revoke", url.Values{}, s.mine); w.Code != http.StatusSeeOther {
		t.Fatalf("revoking: %d", w.Code)
	}

	if list := s.do(http.MethodGet, "/steward/keys?done=revoked", nil, s.mine).Body.String(); strings.Contains(list, "shed-desktop") || !strings.Contains(list, "Key revoked.") {
		t.Error("the key is still listed after revoking")
	}

	// A key needs a name.
	if w := s.do(http.MethodPost, "/steward/keys", url.Values{"name": {" "}}, s.mine); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("no name: %d", w.Code)
	}

	// Signed out, nothing.
	if w := s.do(http.MethodGet, "/steward/keys", nil, nil); w.Code != http.StatusSeeOther {
		t.Errorf("signed out: %d", w.Code)
	}
}
