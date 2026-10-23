package workdayapp

import (
	"net/http"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/foundation/web"
)

// This file is where each steward chooses what they are told about people
// signing up for the days: each time, once a morning, or not at all, and the
// list the evening before.
//
// It was on the stewards screen first, beside who can sign in, and that is
// not where a steward looks for it: the stewards asked for it here, on the
// screen about the days (2026-10-23). The stewards screen keeps a line
// pointing here under the old anchor, since every note sent before the move
// links to it.

// DayNoticesPath is the part of the days screen where the choice is: the
// link at the foot of every note to the stewards about the days.
const DayNoticesPath = Path + "#day-notices"

// dayNoticesSaved is the word each choice is saved as on the redirect, and
// so the sentence the screen says about it.
var dayNoticesSaved = map[userbus.SignUps]string{
	userbus.SignUpsEach:  "each",
	userbus.SignUpsDaily: "daily",
	userbus.SignUpsOff:   "quiet",
}

// dayNotices saves what the signed-in steward is told about the days. Their
// own choice and nobody else's: the form carries no account, so there is no
// steward to change but the one pressing it.
func (a app) dayNotices(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.StewardFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	signUps, err := userbus.ParseSignUps(r.PostFormValue("signups"))
	if err != nil {
		http.Error(w, "That is not one of the choices. Open the page again and choose one.", http.StatusBadRequest)

		return
	}

	evening := r.PostFormValue("evening") == "yes"

	// In the language of the screen they chose on; see userbus.DayNotices.
	if err := a.cfg.Stewards.SetDayNotices(r.Context(), me.ID, userbus.DayNotices{SignUps: signUps, Evening: evening, Lang: mid.LangFrom(r.Context())}); err != nil {
		a.fail(w, r, "saving what a steward is told about the days", err)

		return
	}

	a.cfg.Log.Info("a steward's notes about the days changed", "request_id", web.RequestIDFrom(r.Context()), "user_id", me.ID.String(), "signups", string(signUps), "evening", evening)

	done := dayNoticesSaved[signUps]
	if evening {
		done += "&evening=yes"
	}

	http.Redirect(w, r, Path+"?notices="+done+"#day-notices", http.StatusSeeOther)
}

// noticesView fills in the choice for the list, and what was just saved.
// The sentence about it is said in the section rather than at the top of
// the screen, since the redirect lands on the section and a sentence above
// it would be scrolled past.
func (a app) noticesView(r *http.Request, v *listView) {
	if a.cfg.Stewards == nil {
		return
	}

	me, _ := mid.StewardFrom(r.Context())
	v.DayNotices, v.SignUps, v.Evening = true, string(me.DayNotices.SignUps), me.DayNotices.Evening

	// A fixed sentence chosen by a word, never the query echoed.
	switch r.URL.Query().Get("notices") {
	case "each":
		v.NoticesDone = words.DayNoticesEach
	case "daily":
		v.NoticesDone = words.DayNoticesDaily
	case "quiet":
		v.NoticesDone = words.DayNoticesOff
	}

	if v.NoticesDone != nil && r.URL.Query().Get("evening") == "yes" {
		v.NoticesDoneToo = words.DayNoticesEvening
	}
}
