// Package authapp is how a steward signs in and out: asking for a link,
// redeeming it, and the one-time bootstrap.
//
// Taken from mass-intentions, less backup codes, the account page and the
// per-address throttle (see userbus.MaxLiveLinks for what stands in for it).
//
// # The emailed link does not sign anybody in
//
// GET /sign-in/link renders a page with a button; POST /sign-in/link redeems
// the link. That split is the most important thing in this package. Mail
// scanners, security gateways and the link previews in chat apps all fetch
// the URLs in a message without anybody tapping them, and a single-use link
// spent on GET is spent by software before the steward sees it. The symptom
// would be "that link has already been used", every time, for everybody.
//
// # What the sign-in page will not tell you
//
// Asking for a link shows the same page whether or not the address is a
// steward's, and that includes when the mail could not be sent. A steward
// whose mail is broken sees "check your email" and gets nothing; the
// alternative is an error that appears only for addresses that have an
// account, which is the list of stewards handed to whoever asks. The way in
// when mail is broken is the bootstrap, once, and after that the log.
package authapp

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// The paths, named once, since the pages link to each other and the muxer's
// Require sends people to the first.
const (
	SignInPath = "/sign-in"
	linkPath   = "/sign-in/link"
	firstPath  = "/sign-in/first"
	signOut    = "/sign-out"

	// landing is where signing in lands a steward who was not going
	// anywhere in particular: the stewards' own front page, since editing
	// is what a steward signs in to do. Signing out goes home instead,
	// to the page a volunteer sees.
	landing = "/steward"
	home    = "/"
)

// Users is the slice of userbus this app uses.
type Users interface {
	RequestSignIn(ctx context.Context, email types.Email) (userbus.SignInRequest, error)
	SignIn(ctx context.Context, presented string) (userbus.User, string, error)
	Bootstrap(ctx context.Context, configured, presented string, email types.Email) (userbus.User, string, error)
	BootstrapSpent(ctx context.Context) (bool, error)
	SignOut(ctx context.Context, presented string) error
}

// Config is what this app needs.
type Config struct {
	Log    *slog.Logger
	Render *page.Renderer
	Users  Users

	// Mail may be nil, meaning no relay is configured. That is logged at
	// every request for a link rather than hidden behind a sender that
	// discards, so a missing relay is visible where the link went missing.
	Mail mail.Sender

	// BaseURL is the app's public origin, for the link in the email. Never
	// taken from the request: a link built from the Host header is a link an
	// attacker points at their own server by sending one request, and behind
	// konsoleH's proxy the Host is the loopback anyway.
	BaseURL string

	// Bootstrap is the one-time secret from the config. Empty means its
	// routes are not mounted: a 404 is a better answer than a form that
	// cannot succeed.
	Bootstrap string
}

type app struct{ cfg Config }

// Routes mounts the app. Nothing here is behind Require: these are the pages
// somebody reaches before they are signed in.
func Routes(mux *http.ServeMux, cfg Config) {
	a := app{cfg: cfg}

	mux.HandleFunc("GET "+SignInPath, a.signInForm)
	mux.HandleFunc("POST "+SignInPath, a.requestLink)
	mux.HandleFunc("GET "+linkPath, a.confirmLink)
	mux.HandleFunc("POST "+linkPath, a.redeemLink)
	mux.HandleFunc("POST "+signOut, a.signOut)

	if cfg.Bootstrap != "" {
		mux.HandleFunc("GET "+firstPath, a.bootstrapForm)
		mux.HandleFunc("POST "+firstPath, a.redeemBootstrap)
	}
}

// ------------------------------------------------------------------ copy

// The words on these pages. English only for now: a steward reads these, not
// a volunteer, and Spanish waits for a native speaker (design.md, principle
// 6) -- until then say shows the English marked lang="en".
var (
	sayCannotRead = types.Text{EN: "We could not read that. Open the page again and try once more."}
	sayNotAnEmail = types.Text{EN: "That does not look like an email address. Check it for a typo."}
	sayOurEnd     = types.Text{EN: "Something went wrong on our end. Try again in a few minutes."}
	sayLinkFailed = types.Text{EN: "That link did not work. It may have been used already, or it may be more than fifteen minutes old. Ask for a new one."}
	sayLinkBroken = types.Text{EN: "That link is not complete. It may have been cut in two by the mail app. Ask for a new one."}
	sayFirstFail  = types.Text{EN: "That did not work. The secret may be wrong, or it may have been used already."}
)

// ------------------------------------------------------------------ asking for a link

type signInView struct {
	Next    string
	Email   string
	Problem types.Text
	First   bool // whether to offer the bootstrap
}

func (a app) signInForm(w http.ResponseWriter, r *http.Request) {
	next := mid.SafeNext(r.URL.Query().Get("next"))

	// Already signed in: nothing to do here, and the form would invite a
	// second sign-in for no reason.
	if _, ok := mid.StewardFrom(r.Context()); ok {
		http.Redirect(w, r, orHome(next), http.StatusSeeOther)

		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "sign-in", signInView{Next: next, First: a.cfg.Bootstrap != ""})
}

func (a app) requestLink(w http.ResponseWriter, r *http.Request) {
	view := signInView{First: a.cfg.Bootstrap != ""}

	if err := r.ParseForm(); err != nil {
		view.Problem = sayCannotRead
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "sign-in", view)

		return
	}

	view.Next = mid.SafeNext(r.PostFormValue("next"))
	view.Email = r.PostFormValue("email")

	email, err := types.ParseEmail(view.Email)
	if err != nil {
		// The one thing this page complains about, and it reveals nothing:
		// it is about the text, not about whether anybody holds it.
		view.Problem = sayNotAnEmail
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "sign-in", view)

		return
	}

	req, err := a.cfg.Users.RequestSignIn(r.Context(), email)
	if err != nil {
		a.cfg.Log.Error("a sign-in link could not be prepared", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		view.Problem = sayOurEnd
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "sign-in", view)

		return
	}

	if req.Sendable() {
		a.send(r, req, view.Next)
	}

	// The same page either way, including when the send above failed.
	a.cfg.Render.Render(w, r, http.StatusOK, "sent", struct{ Email string }{Email: email.String()})
}

// send mails the link, and records a failure rather than reporting it; see
// the package comment for why the page cannot say.
func (a app) send(r *http.Request, req userbus.SignInRequest, next string) {
	id := web.RequestIDFrom(r.Context())

	if a.cfg.Mail == nil {
		a.cfg.Log.Error("a sign-in link was not sent: no mail relay is configured", "request_id", id, "user_id", req.User.ID.String())

		return
	}

	link := a.cfg.BaseURL + linkPath + "?t=" + url.QueryEscape(req.Secret)
	if next != "" {
		link += "&next=" + url.QueryEscape(next)
	}

	// Plain text, short, and the link on a line of its own so no mail app
	// wraps it. It says what to do if it was not you, because the most
	// likely reader of an unexpected one is a steward wondering why.
	text := "Someone asked to sign in to the garden stewards' app as " + req.User.Email.String() + ".\r\n\r\n" +
		"Open this link and press the button to sign in:\r\n\r\n" +
		link + "\r\n\r\n" +
		"The link works once, for fifteen minutes.\r\n\r\n" +
		"If this was not you, nothing has happened and you can delete this message.\r\n\r\n" +
		"-- The garden stewards\r\n"

	if err := a.cfg.Mail.Send(r.Context(), mail.Message{
		To:      req.User.Email.String(),
		Subject: "Your sign-in link for the garden stewards",
		Text:    text,
	}); err != nil {
		a.cfg.Log.Error("a sign-in link could not be sent", "request_id", id, "user_id", req.User.ID.String(), "error", err)
	}
}

// ------------------------------------------------------------------ redeeming it

type linkView struct {
	Token   string
	Next    string
	Problem types.Text
}

// confirmLink is the page the emailed link opens. It redeems nothing.
func (a app) confirmLink(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	if q.Get("t") == "" {
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "link", linkView{Problem: sayLinkBroken})

		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "link", linkView{Token: q.Get("t"), Next: mid.SafeNext(q.Get("next"))})
}

func (a app) redeemLink(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "link", linkView{Problem: sayCannotRead})

		return
	}

	next := mid.SafeNext(r.PostFormValue("next"))
	u, cookie, err := a.cfg.Users.SignIn(r.Context(), r.PostFormValue("token"))

	a.finish(w, r, u, cookie, err, next, func(status int) {
		a.cfg.Render.Render(w, r, status, "link", linkView{Problem: sayLinkFailed})
	})
}

// ------------------------------------------------------------------ the bootstrap

type firstView struct {
	Email   string
	Problem types.Text
	Spent   bool
}

// bootstrapForm offers the one-time secret, or says it is gone.
//
// The secret stays in config.toml after it is spent, so this route stays
// mounted, and a form that collects a secret and refuses it reads as a typo.
// Saying it is spent reveals nothing that the app's existence does not; the
// person who most needs to hear it is a steward locked out at the wrong
// moment, who should look for another door.
func (a app) bootstrapForm(w http.ResponseWriter, r *http.Request) {
	// A failure to ask is not a reason to refuse the page: the form is still
	// the truthful thing to show, and the claim it posts to is atomic.
	spent, err := a.cfg.Users.BootstrapSpent(r.Context())
	if err != nil {
		a.cfg.Log.Error("whether the bootstrap is spent could not be read", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "first", firstView{Spent: spent})
}

func (a app) redeemBootstrap(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "first", firstView{Problem: sayCannotRead})

		return
	}

	view := firstView{Email: r.PostFormValue("email")}

	email, err := types.ParseEmail(view.Email)
	if err != nil {
		view.Problem = sayNotAnEmail
		a.cfg.Render.Render(w, r, http.StatusBadRequest, "first", view)

		return
	}

	u, cookie, err := a.cfg.Users.Bootstrap(r.Context(), a.cfg.Bootstrap, r.PostFormValue("secret"), email)

	a.finish(w, r, u, cookie, err, "", func(status int) {
		view.Problem = sayFirstFail
		a.cfg.Render.Render(w, r, status, "first", view)
	})
}

// ------------------------------------------------------------------ the end of each

// finish is the shared tail of every sign-in: set the cookie and go on, or
// show the page again with its own refusal. The sentence is the caller's,
// because userbus gives one error for every failure on purpose.
func (a app) finish(w http.ResponseWriter, r *http.Request, u userbus.User, cookie string, err error, next string, refuse func(status int)) {
	switch {
	case errors.Is(err, userbus.ErrDenied):
		// 401, so a run of refusals shows in the request log.
		refuse(http.StatusUnauthorized)

		return
	case err != nil:
		a.cfg.Log.Error("a sign-in failed", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		http.Error(w, sayOurEnd.EN, http.StatusInternalServerError)

		return
	}

	// The cookie ends when the session does. The session's own expiry is
	// what counts; this only saves the browser sending a dead cookie.
	mid.SetSession(w, cookie, time.Now().Add(userbus.SessionLife))
	http.Redirect(w, r, orHome(next), http.StatusSeeOther)
}

func (a app) signOut(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(mid.SessionCookie); err == nil {
		if err := a.cfg.Users.SignOut(r.Context(), c.Value); err != nil {
			a.cfg.Log.Error("a session could not be ended", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		}
	}

	// Cleared whatever happened above: a cookie left behind after "sign
	// out" is the worse of the two failures.
	mid.ClearSession(w)
	http.Redirect(w, r, home, http.StatusSeeOther)
}

func orHome(next string) string {
	if next == "" {
		return landing
	}

	return next
}
