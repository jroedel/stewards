package workdayapp_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/muxer"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/notice/stores/noticedb"
	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
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

type msite struct {
	t       *testing.T
	h       http.Handler
	list    *subscriberbus.Business
	rsvps   *rsvpbus.Business
	days    *workdaybus.Business
	notices *noticebus.Business
	day     workdaybus.Day
	clock   *time.Time
	mail    *mail.Recorder
	texts   *sms.Recorder
	log     *strings.Builder
	cookie  *http.Cookie
	db      *sql.DB
}

// messaging is the site with everything for writing to many: the list, the
// sign-ups, texts, and an app context. The muxer is built by start, so a
// test can set the clock and the people first: the reminder loop's first
// look is at start.
func messaging(t *testing.T) *msite {
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

	s := &msite{t: t, db: db, mail: &mail.Recorder{}, texts: &sms.Recorder{}, log: &strings.Builder{}}

	clock := time.Date(2026, 10, 21, 9, 0, 0, 0, types.Garden)
	s.clock = &clock
	now := func() time.Time { return *s.clock }

	s.days = workdaybus.NewBusiness(workdaydb.NewStore(db), nil, now)
	s.notices = noticebus.NewBusiness(noticedb.NewStore(db), s.mail, s.texts, now)
	s.notices.Pace(0)
	s.list = subscriberbus.NewBusiness(subscriberdb.NewStore(db), s.notices, now)
	s.rsvps = rsvpbus.NewBusiness(rsvpdb.NewStore(db), s.days, s.notices, now)
	s.notices.OnStopped(s.list.Stopped)
	s.notices.OnStopped(s.rsvps.Stopped)

	if s.day, err = s.days.Create(t.Context(), workdaybus.Fields{
		Starts: time.Date(2026, 10, 24, 8, 0, 0, 0, types.Garden),
		Ends:   time.Date(2026, 10, 24, 11, 0, 0, 0, types.Garden),
		Title:  types.Text{EN: "Planting the rain garden"},
	}); err != nil {
		t.Fatal(err)
	}

	return s
}

func (s *msite) start() *msite {
	t := s.t
	t.Helper()

	log := slog.New(slog.NewTextHandler(s.log, nil))
	users := userbus.NewBusiness(log, userdb.NewStore(s.db), nil)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() { cancel(); s.notices.Wait() })

	var err error
	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: s.db, Expected: sqldb.Infrastructure,
		Places:      placebus.NewBusiness(placedb.NewStore(s.db), nil, nil),
		Species:     speciesbus.NewBusiness(speciesdb.NewStore(s.db), nil, nil),
		Listings:    listingbus.NewBusiness(listingdb.NewStore(s.db), nil, nil),
		Photos:      photos(t, s.db),
		Users:       users,
		Workdays:    s.days,
		Subscribers: s.list,
		Notices:     s.notices,
		RSVPs:       s.rsvps,
		Background:  ctx,
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

func (s *msite) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	s.t.Helper()

	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}

	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(s.cookie)

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

func (s *msite) contact(email, phone string) types.Contact {
	s.t.Helper()

	c, err := types.ParseContact(email, phone)
	if err != nil {
		s.t.Fatal(err)
	}

	return c
}

// coming signs Ana up in Spanish, by email and phone, and Luis in English
// by email, as the day's form would; the confirmations are forgotten.
func (s *msite) coming() {
	s.t.Helper()

	for _, p := range []struct {
		name, email, phone string
		lang               types.Lang
	}{
		{"Ana", "ana@example.invalid", "512 555 0123", types.Spanish},
		{"Luis", "luis@example.invalid", "", types.English},
	} {
		if _, err := s.rsvps.Request(s.t.Context(), s.day.ID, p.name, s.contact(p.email, p.phone), p.lang); err != nil {
			s.t.Fatal(err)
		}
	}

	s.forget()
}

func (s *msite) forget() {
	s.mail.Sent = nil
	s.texts.Refuse = nil
	*s.texts = sms.Recorder{}
}

func (s *msite) path(rest string) string { return "/steward/days/" + s.day.ID.String() + rest }

// emailTo is the last email to an address, or fails.
func (s *msite) emailTo(addr string) mail.Message {
	s.t.Helper()

	for i := len(s.mail.Sent) - 1; i >= 0; i-- {
		if s.mail.Sent[i].To == addr {
			return s.mail.Sent[i]
		}
	}

	s.t.Fatalf("no email to %s among %d", addr, len(s.mail.Sent))

	return mail.Message{}
}

func TestTellingTheListAboutADay(t *testing.T) {
	s := messaging(t)

	for _, c := range []struct {
		email, phone string
		lang         types.Lang
	}{
		{"walker@example.invalid", "", types.English},
		{"", "512 555 0124", types.Spanish},
	} {
		if _, err := s.list.Request(t.Context(), s.contact(c.email, c.phone), c.lang); err != nil {
			t.Fatal(err)
		}
	}

	s.start()
	s.forget()

	edit := s.do(http.MethodGet, s.path("/edit"), nil).Body.String()
	if !strings.Contains(edit, s.path("/announce")) {
		t.Fatal("the day's screen does not offer to tell the list")
	}

	preview := s.do(http.MethodGet, s.path("/announce"), nil).Body.String()
	for _, want := range []string{"New stewardship day", "It goes to 2 people", "1 by email, 1 by text", "1 in Spanish", "Spanish", "Send to the list"} {
		if !strings.Contains(preview, want) {
			t.Errorf("the preview does not say %q", want)
		}
	}

	if len(s.mail.Sent) != 0 || len(s.texts.Sent()) != 0 {
		t.Fatal("the preview sent something")
	}

	if w := s.do(http.MethodPost, s.path("/announce"), url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("sending: %d", w.Code)
	}

	s.notices.Wait()

	m := s.emailTo("walker@example.invalid")
	if !strings.Contains(m.Text, "/days/"+s.day.ID.String()) || !strings.Contains(m.Text, "/unsubscribe?t=") {
		t.Errorf("the email:\n%s", m.Text)
	}

	tx, _ := s.texts.Last()
	if tx.To != "+15125550124" || !strings.Contains(tx.Body, "8:00-11:00") {
		t.Errorf("the text, with a plain dash: %+v", tx)
	}

	// Pressed again: nobody twice; and the screen says it went.
	s.do(http.MethodPost, s.path("/announce"), url.Values{})
	s.notices.Wait()

	if len(s.mail.Sent) != 1 || len(s.texts.Sent()) != 1 {
		t.Errorf("pressed twice: %d emails, %d texts", len(s.mail.Sent), len(s.texts.Sent()))
	}

	edit = s.do(http.MethodGet, s.path("/edit"), nil).Body.String()
	if !strings.Contains(edit, "Told the list") || !strings.Contains(edit, "2 sent") || strings.Contains(edit, s.path("/announce")+`"`) {
		t.Errorf("the day's screen after telling the list:\n%s", edit)
	}
}

func TestAChangeOfTimeIsToldToThoseComing(t *testing.T) {
	s := messaging(t)
	s.coming()
	s.start()

	w := s.do(http.MethodPost, s.path(""), day("2026-10-24", "09:00", "12:00", "Planting the rain garden"))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != s.path("/changed") {
		t.Fatalf("saving a new time: %d %s", w.Code, w.Header().Get("Location"))
	}

	preview := s.do(http.MethodGet, s.path("/changed"), nil).Body.String()
	if !strings.Contains(preview, "Tell them") || !strings.Contains(preview, "9:00") {
		t.Fatalf("the preview:\n%s", preview)
	}

	v := regexp.MustCompile(`name="v" value="(\d+)"`).FindStringSubmatch(preview)
	if v == nil {
		t.Fatal("the preview has no version")
	}

	s.do(http.MethodPost, s.path("/changed"), url.Values{"v": {v[1]}})
	s.do(http.MethodPost, s.path("/changed"), url.Values{"v": {v[1]}})
	s.notices.Wait()

	if len(s.mail.Sent) != 2 || len(s.texts.Sent()) != 1 {
		t.Errorf("told %d by email and %d by text, want 2 and 1", len(s.mail.Sent), len(s.texts.Sent()))
	}

	if m := s.emailTo("luis@example.invalid"); !strings.Contains(m.Subject, "Changed") || !strings.Contains(m.Text, "/days/cancel?t=") {
		t.Errorf("Luis's email: %s\n%s", m.Subject, m.Text)
	}

	// A version that is not the one saved sends nothing.
	s.forget()
	s.do(http.MethodPost, s.path("/changed"), url.Values{"v": {"1"}})
	s.notices.Wait()

	if len(s.mail.Sent) != 0 {
		t.Error("a stale version sent")
	}
}

func TestAStewardsMessageWithItsSpanish(t *testing.T) {
	s := messaging(t)
	s.coming()
	s.start()

	msg := "We'll decide at 9 am whether we're rained out."
	es := "Decidiremos a las 9 si se cancela por la lluvia."

	preview := s.do(http.MethodPost, s.path("/note"), url.Values{"message": {msg}, "spanish": {es}, "action": {"preview"}})
	body := preview.Body.String()

	if preview.Code != http.StatusOK || !strings.Contains(body, "Decidiremos") || len(s.mail.Sent) != 0 {
		t.Fatalf("the preview: %d\n%s", preview.Code, body)
	}

	key := regexp.MustCompile(`name="key" value="([0-9a-f]+)"`).FindStringSubmatch(body)
	if key == nil {
		t.Fatal("the preview has no key")
	}

	send := url.Values{"action": {"send"}, "key": {key[1]}, "sent_message": {msg}, "sent_spanish": {es}}
	s.do(http.MethodPost, s.path("/note"), send)
	s.do(http.MethodPost, s.path("/note"), send) // pressed twice
	s.notices.Wait()

	if tx, _ := s.texts.Last(); len(s.texts.Sent()) != 1 || !strings.Contains(tx.Body, es) {
		t.Errorf("Ana's text, in her Spanish: %+v", s.texts.Sent())
	}

	if m := s.emailTo("luis@example.invalid"); !strings.HasPrefix(m.Text, "We'll decide at 9 am") || len(s.mail.Sent) != 2 {
		t.Errorf("Luis's email: %d sent\n%s", len(s.mail.Sent), m.Text)
	}

	if m := s.emailTo("ana@example.invalid"); !strings.HasPrefix(m.Text, "Decidiremos") {
		t.Errorf("Ana's email:\n%s", m.Text)
	}

	// Too long, or empty: said, and nothing sent.
	s.forget()

	if w := s.do(http.MethodPost, s.path("/note"), url.Values{"message": {strings.Repeat("a", 321)}, "action": {"preview"}}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "320 characters") {
		t.Errorf("too long: %d", w.Code)
	}

	if w := s.do(http.MethodPost, s.path("/note"), url.Values{"action": {"send"}, "key": {"abc"}, "sent_message": {""}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty: %d", w.Code)
	}

	s.notices.Wait()

	if len(s.mail.Sent) != 0 {
		t.Error("a refused message was sent")
	}

	// No Spanish: the preview says the Spanish readers get it as written.
	if body := s.do(http.MethodPost, s.path("/note"), url.Values{"message": {msg}, "action": {"preview"}}).Body.String(); !strings.Contains(body, "get the message as written") {
		t.Error("the preview does not say there is no Spanish")
	}
}

func TestRemovingADayTellsThoseComingFirst(t *testing.T) {
	s := messaging(t)
	s.coming()
	s.start()

	if edit := s.do(http.MethodGet, s.path("/edit"), nil).Body.String(); !strings.Contains(edit, "Tell the people coming that it is cancelled") || !strings.Contains(edit, "Cancelled: the stewardship day") {
		t.Fatal("the remove form does not offer to tell them, or does not show the message")
	}

	if w := s.do(http.MethodPost, s.path("/delete"), url.Values{"confirm": {"yes"}, "tell": {"yes"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("removing: %d", w.Code)
	}

	s.notices.Wait()

	if len(s.mail.Sent) != 2 || len(s.texts.Sent()) != 1 {
		t.Errorf("told %d by email and %d by text", len(s.mail.Sent), len(s.texts.Sent()))
	}

	if m := s.emailTo("ana@example.invalid"); !strings.Contains(m.Subject, "Cancelled") {
		t.Errorf("Ana's email: %s", m.Subject)
	}

	if _, err := s.days.ByID(t.Context(), s.day.ID); !errors.Is(err, workdaybus.ErrNotFound) {
		t.Errorf("the day is still there: %v", err)
	}
}

func TestRemovingADayWithoutTellingThem(t *testing.T) {
	s := messaging(t)
	s.coming()
	s.start()

	s.do(http.MethodPost, s.path("/delete"), url.Values{"confirm": {"yes"}})
	s.notices.Wait()

	if len(s.mail.Sent) != 0 {
		t.Errorf("told them with the box unticked: %d", len(s.mail.Sent))
	}
}

// The reminder goes at 6 pm the evening before, to whoever signed up
// before then.
func TestTheReminderTheEveningBefore(t *testing.T) {
	s := messaging(t)

	*s.clock = time.Date(2026, 10, 23, 17, 0, 0, 0, types.Garden)
	s.coming()

	// Someone who signs up after six has just had their confirmation.
	*s.clock = time.Date(2026, 10, 23, 18, 10, 0, 0, types.Garden)
	if _, err := s.rsvps.Request(t.Context(), s.day.ID, "Sam", s.contact("sam@example.invalid", ""), types.English); err != nil {
		t.Fatal(err)
	}

	s.forget()

	*s.clock = time.Date(2026, 10, 23, 18, 30, 0, 0, types.Garden)
	s.start()

	// The loop's first look is at start, in its own goroutine.
	deadline := time.Now().Add(5 * time.Second)
	for len(s.texts.Sent()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	s.notices.Wait()

	if len(s.mail.Sent) != 2 || len(s.texts.Sent()) != 1 {
		t.Fatalf("reminded %d by email and %d by text, want 2 and 1", len(s.mail.Sent), len(s.texts.Sent()))
	}

	if m := s.emailTo("luis@example.invalid"); !strings.HasPrefix(m.Subject, "Tomorrow") {
		t.Errorf("Luis's reminder: %s", m.Subject)
	}

	for _, m := range s.mail.Sent {
		if m.To == "sam@example.invalid" {
			t.Error("Sam, who signed up after six, was reminded")
		}
	}
}
