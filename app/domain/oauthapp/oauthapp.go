// Package oauthapp is how a program signs in as a steward through OAuth:
// the discovery document, the page where a steward agrees, and the token
// endpoint that hands the program its key.
//
// It exists for Claude on claude.ai and its phone app (a "custom connector"),
// which can only reach a server that signs in this way: there is no file
// there to keep a key in, and the request-header alternative is a beta most
// accounts do not have. What the program is given is an ordinary API key
// (userbus/oauth.go), so the API it then uses is exactly the one a key from
// the keys screen reaches, with the same rules.
//
// # Who may ask
//
// Programs are known by a Client ID Metadata Document (foundation/oauth): the
// client_id is an HTTPS URL, and the document there says where the code may
// be sent. Only documents on Anthropic's hosts are read. Any program could
// publish a document, and a steward who is shown "Let some-app.example work
// as you?" by a link in a message is the phishing this page would otherwise
// be. Claude is the program this was built for; another is a decision for
// the stewards, and a line in trustedHosts.
//
// # The two kinds of refusal
//
// RFC 6749 is particular about this, and it is a security rule rather than a
// style. A request whose program or redirect cannot be trusted is answered
// here, on our own page, and never sent anywhere: redirecting with an error
// to an unchecked redirect_uri is an open redirect with our name on it. Every
// other refusal -- a missing PKCE challenge, the steward saying no -- goes
// back to the program at its checked redirect, which is how it finds out.
package oauthapp

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/oauth"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// The paths. The discovery document's is fixed by RFC 8414 for an issuer
// with no path, which this site's origin is.
const (
	MetadataPath  = "/.well-known/oauth-authorization-server"
	AuthorizePath = "/oauth/authorize"
	TokenPath     = "/oauth/token"
)

// trustedHosts are the hosts whose metadata documents are read, with their
// subdomains. Claude's are on claude.ai today (Claude Code's is
// https://claude.ai/oauth/claude-code-client-metadata); the other two are
// Anthropic's own, so that Claude moving its document between them is not a
// failed connection to debug.
var trustedHosts = []string{"claude.ai", "claude.com", "anthropic.com"}

// Users is the slice of userbus this app uses.
type Users interface {
	GrantAccess(ctx context.Context, userID types.ID, clientID, name, redirect, challenge string) (string, error)
	RedeemGrant(ctx context.Context, presented, clientID, redirect, verifier string) (userbus.APIKey, string, error)
}

// Clients reads a program's metadata document.
type Clients interface {
	Fetch(ctx context.Context, clientID string) (oauth.Client, error)
}

// Config is what this app needs.
type Config struct {
	Log     *slog.Logger
	Render  *page.Renderer
	Users   Users
	Clients Clients

	// BaseURL is the public origin, which is the issuer: the discovery
	// document and the iss on every answer say it, and a program checks
	// that they agree.
	BaseURL string

	// Now is for tests; nil means the wall clock. Only the token's
	// expires_in reads it.
	Now func() time.Time
}

type app struct {
	cfg  Config
	meta oauth.ServerMetadata
}

// Routes mounts the app. The page where a steward agrees is behind guard,
// so that a steward who is not signed in is sent to sign in and brought
// back; the discovery document and the token endpoint are for programs, and
// are not.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	a := app{cfg: cfg, meta: oauth.NewServerMetadata(cfg.BaseURL, AuthorizePath, TokenPath)}

	mux.HandleFunc("GET "+MetadataPath, a.metadata)
	mux.Handle("GET "+AuthorizePath, guard(http.HandlerFunc(a.ask)))
	mux.Handle("POST "+AuthorizePath, guard(http.HandlerFunc(a.answer)))
	mux.HandleFunc("POST "+TokenPath, a.token)
}

func (a app) metadata(w http.ResponseWriter, _ *http.Request) {
	web.WriteJSON(w, http.StatusOK, a.meta)
}

// ------------------------------------------------------------------ the request

// request is an authorization request, from the query on the way in and from
// the form's hidden fields on the way out. Both are read the same way and
// checked the same way: the hidden fields are only the query carried across
// one page, and a steward's browser can change them as easily.
type request struct {
	ClientID, Redirect, State, Challenge, Method, ResponseType string
}

func requestFrom(v url.Values) request {
	return request{
		ClientID:     v.Get("client_id"),
		Redirect:     v.Get("redirect_uri"),
		State:        v.Get("state"),
		Challenge:    v.Get("code_challenge"),
		Method:       v.Get("code_challenge_method"),
		ResponseType: v.Get("response_type"),
	}
}

// client reads and checks the program and its redirect: the checks whose
// failure is answered on our own page. The sentence is for the steward.
func (a app) client(ctx context.Context, req request) (oauth.Client, types.Text, error) {
	if !trusted(req.ClientID) {
		a.cfg.Log.Warn("an OAuth request from a program that is not trusted", "client_id", req.ClientID)

		return oauth.Client{}, sayNotClaude, errUntrusted
	}

	c, err := a.cfg.Clients.Fetch(ctx, req.ClientID)
	if err != nil {
		a.cfg.Log.Warn("an OAuth program's metadata document could not be used", "client_id", req.ClientID, "error", err)

		return oauth.Client{}, sayCannotReadClient, err
	}

	if !c.RedirectAllowed(req.Redirect) {
		a.cfg.Log.Warn("an OAuth request asked to go back somewhere its program does not list", "client_id", req.ClientID, "redirect_uri", req.Redirect)

		return oauth.Client{}, sayBadRedirect, errBadRedirect
	}

	return c, types.Text{}, nil
}

var (
	errUntrusted   = errors.New("the client is not on a trusted host")
	errBadRedirect = errors.New("the redirect is not one the client lists")
)

func trusted(clientID string) bool {
	u, err := url.Parse(clientID)
	if err != nil || u.Scheme != "https" {
		return false
	}

	host := u.Hostname()
	for _, t := range trustedHosts {
		if host == t || strings.HasSuffix(host, "."+t) {
			return true
		}
	}

	return false
}

// refusal is what a request that passed client() lacks, as the OAuth error
// to send back, or "" for nothing.
func (req request) refusal() (code, description string) {
	switch {
	case req.ResponseType != "code":
		return "unsupported_response_type", "only the authorization code flow is offered"
	case req.Method != "S256" || !oauth.ValidChallenge(req.Challenge):
		return "invalid_request", "a PKCE code_challenge made with S256 is required"
	}

	return "", ""
}

// back sends the steward to the program with params, and the state and
// issuer every answer carries.
func (a app) back(w http.ResponseWriter, r *http.Request, req request, params url.Values) {
	params.Set("state", req.State)
	params.Set("iss", a.meta.Issuer)

	to, err := oauth.RedirectWith(req.Redirect, params)
	if err != nil {
		// Checked against the program's document already, so this is a
		// document listing something that is not a URL.
		a.refuse(w, r, http.StatusBadRequest, sayBadRedirect)

		return
	}

	http.Redirect(w, r, to, http.StatusSeeOther)
}

// ------------------------------------------------------------------ the page

// The words on these pages. English only for now, as for sign-in: Spanish
// comes when the screens' own words are translated.
var (
	sayNotClaude        = types.Text{EN: "Only Claude can connect to the garden stewards' app this way. If you did not start this, close the page: nothing has happened."}
	sayCannotReadClient = types.Text{EN: "We could not check who is asking to connect. Go back to Claude and try connecting again in a minute."}
	sayBadRedirect      = types.Text{EN: "This connection asked to send you somewhere the program does not list as its own, so we stopped it. Go back to Claude and try connecting again."}
	sayCannotRead       = types.Text{EN: "We could not read that. Go back to Claude and try connecting again."}
)

type connectView struct {
	Request request

	Name    string // what the document calls itself
	Host    string // who is asking, as the client_id says
	KeyName string // what the keys screen will list
	BackTo  string // where Allow sends the steward
	Email   string

	Problem types.Text
}

// ask is the page where a steward agrees, or not.
func (a app) ask(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.URL.Query())

	c, say, err := a.client(r.Context(), req)
	if err != nil {
		a.refuse(w, r, http.StatusBadRequest, say)

		return
	}

	if code, desc := req.refusal(); code != "" {
		a.back(w, r, req, url.Values{"error": {code}, "error_description": {desc}})

		return
	}

	a.show(w, r, http.StatusOK, c, req, types.Text{})
}

// answer is the steward's yes or no.
func (a app) answer(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.StewardFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		a.refuse(w, r, http.StatusBadRequest, sayCannotRead)

		return
	}

	req := requestFrom(r.PostForm)

	c, say, err := a.client(r.Context(), req)
	if err != nil {
		a.refuse(w, r, http.StatusBadRequest, say)

		return
	}

	if code, desc := req.refusal(); code != "" {
		a.back(w, r, req, url.Values{"error": {code}, "error_description": {desc}})

		return
	}

	if r.PostForm.Get("answer") != "allow" {
		a.cfg.Log.Info("a steward said no to an OAuth program", "user_id", me.ID.String(), "client_id", req.ClientID)
		a.back(w, r, req, url.Values{"error": {"access_denied"}, "error_description": {"the steward said no"}})

		return
	}

	code, err := a.cfg.Users.GrantAccess(r.Context(), me.ID, req.ClientID, keyName(c), req.Redirect, req.Challenge)

	invalid, isInvalid := errors.AsType[userbus.Invalid](err)

	switch {
	case isInvalid:
		a.show(w, r, http.StatusUnprocessableEntity, c, req, types.Text{EN: page.Sentence(invalid.Problem)})

		return
	case err != nil:
		a.cfg.Log.Error("an OAuth grant could not be made", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		a.back(w, r, req, url.Values{"error": {"server_error"}})

		return
	}

	a.back(w, r, req, url.Values{"code": {code}})
}

func (a app) show(w http.ResponseWriter, r *http.Request, status int, c oauth.Client, req request, problem types.Text) {
	me, _ := mid.StewardFrom(r.Context())

	back, _ := url.Parse(req.Redirect) // checked in client()

	// The form's answer is a redirect to the program, which form-action
	// must allow; see page.AllowFormTo.
	page.AllowFormTo(w.Header(), back.Scheme+"://"+back.Host)

	a.cfg.Render.Render(w, r, status, "connect", connectView{
		Request: req,
		Name:    cmp.Or(c.Name, c.Host()),
		Host:    c.Host(),
		KeyName: keyName(c),
		BackTo:  back.Host,
		Email:   me.Email.String(),
		Problem: problem,
	})
}

func (a app) refuse(w http.ResponseWriter, r *http.Request, status int, say types.Text) {
	a.cfg.Render.Render(w, r, status, "connect", connectView{Problem: say})
}

// keyName is what the keys screen lists the program's key as: its own name,
// and the host its document is on, since the name is only what the document
// says about itself.
func keyName(c oauth.Client) string {
	if c.Name == "" {
		return c.Host()
	}

	return c.Name + " (" + c.Host() + ")"
}

// ------------------------------------------------------------------ the token

// token trades a code for the key. A program's request, not a person's: the
// answers are the RFC's JSON, never a page.
func (a app) token(w http.ResponseWriter, r *http.Request) {
	// A token is a credential. Neither it nor a refusal may be cached by
	// anything between here and the program.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")

	if err := r.ParseForm(); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, oauth.TokenError{Code: oauth.InvalidRequest, Description: "the body could not be read as a form"})

		return
	}

	f := r.PostForm

	if f.Get("grant_type") != "authorization_code" {
		web.WriteJSON(w, http.StatusBadRequest, oauth.TokenError{Code: oauth.UnsupportedGrantType, Description: "only authorization_code is offered"})

		return
	}

	for _, name := range []string{"code", "client_id", "redirect_uri", "code_verifier"} {
		if f.Get(name) == "" {
			web.WriteJSON(w, http.StatusBadRequest, oauth.TokenError{Code: oauth.InvalidRequest, Description: name + " is required"})

			return
		}
	}

	k, key, err := a.cfg.Users.RedeemGrant(r.Context(), f.Get("code"), f.Get("client_id"), f.Get("redirect_uri"), f.Get("code_verifier"))

	switch {
	case errors.Is(err, userbus.ErrDenied):
		web.WriteJSON(w, http.StatusBadRequest, oauth.TokenError{Code: oauth.InvalidGrant, Description: "the code is not valid: it may have been used, have expired, or belong to another request"})

		return
	case err != nil:
		a.cfg.Log.Error("an OAuth code could not be traded for a key", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		web.WriteJSON(w, http.StatusInternalServerError, oauth.TokenError{Code: "server_error"})

		return
	}

	web.WriteJSON(w, http.StatusOK, oauth.Token{
		AccessToken: key,
		TokenType:   "Bearer",
		ExpiresIn:   int64(k.ExpiresAt.Sub(a.cfg.Now()) / time.Second),
	})
}
