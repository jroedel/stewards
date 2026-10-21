package dayapp_test

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/muxer"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/notice/stores/noticedb"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
	"github.com/jroedel/stewards/business/domain/photo/stores/photofs"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/rsvp/rsvpbus"
	"github.com/jroedel/stewards/business/domain/rsvp/stores/rsvpdb"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/domain/subscriber/stores/subscriberdb"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sms"
	"github.com/jroedel/stewards/foundation/sqldb"
)

type site struct {
	t      *testing.T
	h      http.Handler
	list   *subscriberbus.Business
	rsvps  *rsvpbus.Business
	day    workdaybus.Day
	clock  *time.Time
	mail   *mail.Recorder
	texts  *sms.Recorder
	log    *strings.Builder
	cookie *http.Cookie
}

// Through the muxer, with a recorded mailbox and phone and a log the test
// can read. Texts are on; serveNoTexts is the app with no Twilio.
func serve(t *testing.T) *site { return serveWith(t, true) }

func serveNoTexts(t *testing.T) *site { return serveWith(t, false) }

func serveWith(t *testing.T, withTexts bool) *site {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return placedb.Init(t.Context(), db) },
		func() error { return speciesdb.Init(t.Context(), db) },
		func() error { return listingdb.Init(t.Context(), db) },
		func() error { return photodb.Init(t.Context(), db) },
		func() error { return userdb.Init(t.Context(), db) },
		func() error { return workdaydb.Init(t.Context(), db) },
		func() error { return rsvpdb.Init(t.Context(), db) },
		func() error { return subscriberdb.Init(t.Context(), db) },
		func() error { return noticedb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	s := &site{t: t, mail: &mail.Recorder{}, texts: &sms.Recorder{}, log: &strings.Builder{}}

	var texts sms.Sender
	if withTexts {
		texts = s.texts
	}

	clock := time.Date(2026, 10, 21, 9, 0, 0, 0, types.Garden)
	s.clock = &clock
	now := func() time.Time { return *s.clock }

	days := workdaybus.NewBusiness(workdaydb.NewStore(db), nil, now)
	notices := noticebus.NewBusiness(noticedb.NewStore(db), s.mail, texts, now)
	s.list = subscriberbus.NewBusiness(subscriberdb.NewStore(db), notices, now)
	s.rsvps = rsvpbus.NewBusiness(rsvpdb.NewStore(db), days, notices, now)
	notices.OnStopped(s.list.Stopped)
	notices.OnStopped(s.rsvps.Stopped)

	if s.day, err = days.Create(t.Context(), workdaybus.Fields{
		Starts: time.Date(2026, 10, 24, 8, 0, 0, 0, types.Garden),
		Ends:   time.Date(2026, 10, 24, 11, 0, 0, 0, types.Garden),
		Title:  types.Text{EN: "Planting the rain garden"},
	}); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(s.log, nil))
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places:      placebus.NewBusiness(placedb.NewStore(db), nil, nil),
		Species:     speciesbus.NewBusiness(speciesdb.NewStore(db), nil, nil),
		Listings:    listingbus.NewBusiness(listingdb.NewStore(db), nil, nil),
		Photos:      photos(t, db),
		Users:       users,
		Workdays:    days,
		Subscribers: s.list,
		Notices:     notices,
		RSVPs:       s.rsvps,
		BaseURL:     "https://stewards.example.invalid", Mail: s.mail,
	}); err != nil {
		t.Fatal(err)
	}

	addr, _ := types.ParseEmail("steward@example.org")
	if _, err := users.Create(t.Context(), addr, ""); err != nil {
		t.Fatal(err)
	}

	req, _ := users.RequestSignIn(t.Context(), addr)
	_, value, err := users.SignIn(t.Context(), req.Secret)
	if err != nil {
		t.Fatal(err)
	}

	s.cookie = &http.Cookie{Name: mid.SessionCookie, Value: value}

	return s
}

func (s *site) do(method, path string, form url.Values, signedIn bool) *httptest.ResponseRecorder {
	s.t.Helper()

	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}

	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if signedIn {
		r.AddCookie(s.cookie)
	}

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

// link is the address in the last email that starts with prefix.
func (s *site) link(prefix string) string {
	s.t.Helper()

	m, ok := s.mail.Last()
	if !ok {
		s.t.Fatal("no email was sent")
	}

	at := strings.Index(m.Text, prefix)
	if at < 0 {
		s.t.Fatalf("the email has no %s link:\n%s", prefix, m.Text)
	}

	return strings.Fields(m.Text[at:])[0]
}

// token is the t= of a link.
func token(t *testing.T, link string) string {
	t.Helper()

	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}

	return u.Query().Get("t")
}

func (s *site) dayPath() string { return "/days/" + s.day.ID.String() }

// The whole of it, as a newcomer does it: the day on the home page, its
// page, "I'll be there", the note with its links, the name on the page, and
// later the way off it.
func TestANewcomerSaysTheyllCome(t *testing.T) {
	s := serve(t)

	home := s.do(http.MethodGet, "/", nil, false).Body.String()
	if !strings.Contains(home, `href="`+s.dayPath()+`"`) || !strings.Contains(home, "I&#39;ll be there") && !strings.Contains(home, "I'll be there") {
		t.Fatalf("the home page does not link the day's page:\n%s", home)
	}

	page := s.do(http.MethodGet, s.dayPath(), nil, false).Body.String()
	for _, want := range []string{"Planting the rain garden", "Nobody has said yet", `name="name"`, `name="phone"`, `name="website"`, "Shown on this page to anyone"} {
		if !strings.Contains(page, want) {
			t.Errorf("the day's page does not have %q", want)
		}
	}

	w := s.do(http.MethodPost, s.dayPath(), url.Values{"name": {"Ana"}, "email": {"ana@example.invalid"}, "phone": {"512 555 0123"}, "list": {"yes"}}, false)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != s.dayPath()+"?done=coming" {
		t.Fatalf("signing up: %d %s\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}

	// One email and one text, each with the cancel link; the email also
	// says she is on the list, with its way off.
	m, _ := s.mail.Last()
	if len(s.mail.Sent) != 1 || m.To != "ana@example.invalid" || !strings.Contains(m.Subject, "See you on") ||
		!strings.Contains(m.Text, "Thank you, Ana.") || !strings.Contains(m.Text, "Planting the rain garden") ||
		!strings.Contains(m.Text, "/days/cancel?t=") || !strings.Contains(m.Text, "/unsubscribe?t=") {
		t.Errorf("the email: %d sent\n%s\n%s", len(s.mail.Sent), m.Subject, m.Text)
	}

	tx, _ := s.texts.Last()
	if len(s.texts.Sent()) != 1 || tx.To != "+15125550123" || !strings.Contains(tx.Body, "/days/cancel?t=") || !strings.Contains(tx.Body, "STOP") {
		t.Errorf("the text: %d sent, %+v", len(s.texts.Sent()), tx)
	}

	// On the list too.
	if on, _ := s.list.All(t.Context()); len(on) != 1 {
		t.Errorf("the list: %+v", on)
	}

	// Her name is on the page and the home page.
	if page := s.do(http.MethodGet, s.dayPath()+"?done=coming", nil, false).Body.String(); !strings.Contains(page, "<li>Ana</li>") || !strings.Contains(page, "You&#39;re signed up") && !strings.Contains(page, "You're signed up") {
		t.Errorf("after signing up:\n%s", page)
	}

	if home := s.do(http.MethodGet, "/", nil, false).Body.String(); !strings.Contains(home, "Ana is coming") {
		t.Errorf("the home page does not say Ana is coming")
	}

	// The steward sees her, with how to reach her, and the count.
	edit := s.do(http.MethodGet, "/steward/days/"+s.day.ID.String()+"/edit", nil, true).Body.String()
	if !strings.Contains(edit, "ana@example.invalid") || !strings.Contains(edit, "(512) 555-0123") {
		t.Errorf("the steward's day does not show who is coming")
	}

	if list := s.do(http.MethodGet, "/steward/days", nil, true).Body.String(); !strings.Contains(list, "1 coming") {
		t.Errorf("the steward's list does not count her")
	}

	// The cancel link shows a button and changes nothing; the button does.
	cancel := s.link("https://stewards.example.invalid/days/cancel?t=")
	if page := s.do(http.MethodGet, strings.TrimPrefix(cancel, "https://stewards.example.invalid"), nil, false).Body.String(); !strings.Contains(page, "Take my name off") {
		t.Errorf("the cancel page has no button:\n%s", page)
	}

	if names, _ := s.rsvps.Coming(t.Context(), s.day.ID); len(names) != 1 {
		t.Fatal("opening the cancel link took her off")
	}

	if w := s.do(http.MethodPost, "/days/cancel", url.Values{"t": {token(t, cancel)}}, false); !strings.Contains(w.Body.String(), "Your name is off that day") {
		t.Errorf("cancelling: %d\n%s", w.Code, w.Body)
	}

	if names, _ := s.rsvps.Coming(t.Context(), s.day.ID); len(names) != 0 {
		t.Errorf("still coming: %q", names)
	}

	// And no address or number reached the log.
	for _, secret := range []string{"ana@example.invalid", "5125550123", "555-0123"} {
		if strings.Contains(s.log.String(), secret) {
			t.Errorf("%q was logged:\n%s", secret, s.log)
		}
	}
}

func TestTheFormSaysWhatIsWrongBesideIt(t *testing.T) {
	s := serve(t)

	w := s.do(http.MethodPost, s.dayPath(), url.Values{"name": {"www.spam.example"}, "email": {"ana@"}}, false)
	body := w.Body.String()

	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "not an address or a link") || !strings.Contains(body, "doesn&#39;t look like an email") && !strings.Contains(body, "doesn't look like an email") {
		t.Errorf("both problems at once: %d\n%s", w.Code, body)
	}

	if len(s.mail.Sent) != 0 {
		t.Error("a refused form sent mail")
	}

	// The honeypot: the page a person would see, and nothing done.
	bot := s.do(http.MethodPost, s.dayPath(), url.Values{"name": {"Bot"}, "email": {"bot@example.invalid"}, "website": {"x"}}, false)
	if bot.Code != http.StatusSeeOther || len(s.mail.Sent) != 0 {
		t.Errorf("the honeypot: %d", bot.Code)
	}

	if names, _ := s.rsvps.Coming(t.Context(), s.day.ID); len(names) != 0 {
		t.Errorf("the bot is coming: %q", names)
	}
}

func TestADayThatIsOverHasNoForm(t *testing.T) {
	s := serve(t)

	*s.clock = time.Date(2026, 10, 24, 12, 0, 0, 0, types.Garden)

	page := s.do(http.MethodGet, s.dayPath(), nil, false).Body.String()
	if strings.Contains(page, `name="name"`) || !strings.Contains(page, "This day is over") {
		t.Errorf("an ended day:\n%s", page)
	}

	if w := s.do(http.MethodPost, s.dayPath(), url.Values{"name": {"Ana"}, "email": {"ana@example.invalid"}}, false); w.Code != http.StatusConflict {
		t.Errorf("signing up for an ended day: %d", w.Code)
	}

	if w := s.do(http.MethodGet, "/days/"+types.NewID().String(), nil, false); w.Code != http.StatusNotFound {
		t.Errorf("no such day: %d", w.Code)
	}
}

func TestAStewardTakesANameOff(t *testing.T) {
	s := serve(t)

	s.do(http.MethodPost, s.dayPath(), url.Values{"name": {"Rude Word"}, "email": {"x@example.invalid"}}, false)

	rows, _ := s.rsvps.ForDay(t.Context(), s.day.ID)
	if len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}

	path := "/steward/days/" + s.day.ID.String() + "/coming/" + rows[0].ID.String() + "/remove"

	s.do(http.MethodPost, path, url.Values{}, false)
	if names, _ := s.rsvps.Coming(t.Context(), s.day.ID); len(names) != 1 {
		t.Error("a name was taken off by someone signed out")
	}

	if w := s.do(http.MethodPost, path, url.Values{}, true); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "done=removed") {
		t.Errorf("signed in: %d %s", w.Code, w.Header().Get("Location"))
	}

	if page := s.do(http.MethodGet, s.dayPath(), nil, false).Body.String(); strings.Contains(page, "Rude Word") {
		t.Error("the name is still on the page")
	}
}

func TestTheMessagesScreenShowsTheConfirmation(t *testing.T) {
	s := serve(t)

	page := s.do(http.MethodGet, "/steward/messages", nil, true).Body.String()
	for _, want := range []string{"Signed up for a day", "See you on", "Plantar el jardín de lluvia"} {
		if !strings.Contains(page, want) {
			t.Errorf("the messages screen does not say %q", want)
		}
	}
}

func photos(t *testing.T, db *sql.DB) *photobus.Business {
	t.Helper()

	files, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	return photobus.NewBusiness(photodb.NewStore(db), files, nil)
}

// A phone alone, then the same person's email: one row, and each half
// welcomed once, the text with its own short words and the links in it.
func TestAPhoneSignsUpAndAddsAnEmail(t *testing.T) {
	s := serve(t)

	home := s.do(http.MethodGet, "/", nil, false).Body.String()
	if !strings.Contains(home, `name="phone"`) || !strings.Contains(home, "reply STOP at any time") {
		t.Fatal("the home page does not ask for a phone, or does not say how to stop texts")
	}

	if w := s.do(http.MethodPost, "/subscribe", url.Values{"phone": {"(512) 555-0123"}}, false); w.Code != http.StatusSeeOther {
		t.Fatalf("a phone alone: %d\n%s", w.Code, w.Body)
	}

	tx, ok := s.texts.Last()
	if !ok || tx.To != "+15125550123" || !strings.Contains(tx.Body, "reply STOP") ||
		!strings.Contains(tx.Body, "https://stewards.example.invalid/unsubscribe?t=") || len(s.mail.Sent) != 0 {
		t.Fatalf("the welcome text: %+v, %d emails", tx, len(s.mail.Sent))
	}

	s.do(http.MethodPost, "/subscribe", url.Values{"email": {"walker@example.org"}, "phone": {"512 555 0123"}}, false)

	on, _ := s.list.All(t.Context())
	if len(on) != 1 || on[0].Contact.Email.String() != "walker@example.org" || len(s.texts.Sent()) != 1 || len(s.mail.Sent) != 1 {
		t.Errorf("after adding the email: %+v; %d texts, %d emails", on, len(s.texts.Sent()), len(s.mail.Sent))
	}

	// The steward sees the number the way it is read aloud.
	if list := s.do(http.MethodGet, "/steward/subscribers", nil, true).Body.String(); !strings.Contains(list, "(512) 555-0123") {
		t.Error("the stewards' list does not show the phone")
	}

	// The number never reaches the log, in any form.
	for _, n := range []string{"5125550123", "555-0123", "555 0123"} {
		if strings.Contains(s.log.String(), n) {
			t.Errorf("a number was logged:\n%s", s.log)
		}
	}
}

// A STOP takes the number off: the next text to it is refused, and the
// person who also gave an email keeps getting email.
func TestAStopTakesTheNumberOff(t *testing.T) {
	s := serve(t)

	s.do(http.MethodPost, "/subscribe", url.Values{"email": {"walker@example.org"}, "phone": {"512 555 0123"}}, false)

	s.texts.Refuse = func(sms.Message) error { return sms.Refused{Code: 21610, Message: "unsubscribed recipient"} }

	// Signing up a second number for the same email welcomes it, is refused,
	// and the refusal takes that number off.
	s.do(http.MethodPost, "/subscribe", url.Values{"phone": {"512 555 0124"}}, false)

	on, _ := s.list.All(t.Context())
	if len(on) != 1 || on[0].Contact.Phone.String() != "+15125550123" {
		t.Errorf("the list: %+v", on)
	}
}

// With no Twilio the form is as it was: an email, required, and no phone.
func TestWithNoTextsTheFormAsksForAnEmail(t *testing.T) {
	s := serveNoTexts(t)

	home := s.do(http.MethodGet, "/", nil, false).Body.String()
	if strings.Contains(home, `name="phone"`) || !strings.Contains(home, `name="email"`) {
		t.Fatal("the home page asks for a phone with texts off")
	}

	// A phone sent anyway, from an old page, is not kept.
	w := s.do(http.MethodPost, "/subscribe", url.Values{"phone": {"512 555 0123"}}, false)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Leave your email") {
		t.Errorf("a phone with texts off: %d\n%s", w.Code, w.Body)
	}

	if on, _ := s.list.All(t.Context()); len(on) != 0 {
		t.Errorf("on the list: %+v", on)
	}
}

// The messages screen shows the welcome both ways and in both languages.
func TestTheMessagesScreenShowsTheWelcomeBothWays(t *testing.T) {
	s := serve(t)

	if w := s.do(http.MethodGet, "/steward/messages", nil, false); w.Code != http.StatusSeeOther {
		t.Errorf("signed out: %d", w.Code)
	}

	page := s.do(http.MethodGet, "/steward/messages", nil, true).Body.String()
	for _, want := range []string{"Welcome to the list", "Thank you for signing up for stewardship days", "reply STOP", "Spanish", "Waiting for its translation"} {
		if !strings.Contains(page, want) {
			t.Errorf("the messages screen does not say %q", want)
		}
	}
}
