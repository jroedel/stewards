package page

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// assets holds the shared chrome: the layout, the stylesheet, the brand faces
// and the logo.
//
//go:embed assets
var assets embed.FS

// Renderer turns a named template into a response. Adapted from
// mass-intentions, less its second surface and its script.
//
// Templates are parsed once, at startup, from the binary. A parse error is a
// startup failure rather than a 500 in front of somebody, and the binary is a
// deployable artefact on its own with no directory of templates to keep in
// step with it.
//
// # One template set per page, not one set for everything
//
// Every page defines "content", so parsing them all into one set would have
// each overwrite the last, silently, and serve the wrong page. The layout is
// parsed once and cloned per page, which is what lets {{block "content"}} mean
// something different for each.
type Renderer struct {
	log   *slog.Logger
	pages map[string]*template.Template

	// The stylesheet is served under a path holding a hash of its content, so
	// it can be cached for ever and still change the moment it is edited --
	// which matters more here than usual, because a volunteer's phone in the
	// woods keeps whatever it last fetched.
	css     []byte
	cssPath string
	cssETag string

	// Fonts and images, by file name, from fixed paths. A font or a logo
	// changes only when the brand does, and then under a new file name: the
	// name is the version.
	files map[string]asset
}

type asset struct {
	body []byte
	etag string
	kind string
}

// Shell is what the layout is executed against. The page's own data is under
// Data, so a field a handler adds can never shadow one the layout needs.
type Shell struct {
	Stylesheet string

	// Lang is the language the page is in, and Other the one the toggle
	// offers, with the link that switches to it.
	Lang      types.Lang
	OtherLang types.Lang
	OtherURL  string

	// Steward is the address of the steward signed in, or "" for everybody
	// else -- which on most pages is everybody. The footer shows it with a
	// way to sign out, because a phone passed between stewards is a phone
	// signed in as whoever had it last.
	Steward string

	Data any
}

// NewRenderer parses the layout and every page template in the given
// filesystems, each holding templates/*.html.
//
// A page name defined twice is a startup error rather than a silent win for
// whichever was parsed last; two apps are far more apt to both want a page
// called "index" than one is to define it twice.
func NewRenderer(log *slog.Logger, own ...fs.FS) (*Renderer, error) {
	if len(own) == 0 {
		return nil, errors.New("a renderer needs at least one app's templates")
	}

	chrome, err := fs.Sub(assets, "assets/app")
	if err != nil {
		return nil, fmt.Errorf("the chrome is missing from the binary: %w", err)
	}

	base, err := template.New("base").Funcs(Funcs).ParseFS(chrome, "*.html")
	if err != nil {
		return nil, fmt.Errorf("the layout could not be read: %w", err)
	}

	pages := map[string]*template.Template{}

	for _, fsys := range own {
		names, err := fs.Glob(fsys, "templates/*.html")
		if err != nil {
			return nil, fmt.Errorf("the page templates could not be listed: %w", err)
		}

		// Partials are pieces an app's pages share -- the fields both the
		// add and the edit form of a photo ask for -- in templates/partials.
		// Parsed into each of that app's pages and no other app's, so two
		// apps can each have a "fields" without either seeing the other's.
		partials, err := fs.Glob(fsys, "templates/partials/*.html")
		if err != nil {
			return nil, fmt.Errorf("the partial templates could not be listed: %w", err)
		}

		for _, name := range names {
			set, err := base.Clone()
			if err != nil {
				return nil, fmt.Errorf("the layout could not be copied: %w", err)
			}

			if len(partials) > 0 {
				if set, err = set.ParseFS(fsys, partials...); err != nil {
					return nil, fmt.Errorf("the partials for %s could not be read: %w", name, err)
				}
			}

			if set, err = set.ParseFS(fsys, name); err != nil {
				return nil, fmt.Errorf("%s could not be read: %w", name, err)
			}

			page := strings.TrimSuffix(path.Base(name), ".html")

			if _, taken := pages[page]; taken {
				return nil, fmt.Errorf("two apps both define a %s page", page)
			}

			pages[page] = set
		}
	}

	if len(pages) == 0 {
		return nil, errors.New("there are no page templates to read")
	}

	css, err := fs.ReadFile(chrome, "app.css")
	if err != nil {
		return nil, fmt.Errorf("the stylesheet could not be read: %w", err)
	}

	sum := sha256.Sum256(css)
	digest := hex.EncodeToString(sum[:])[:12]

	rn := &Renderer{
		log:     log,
		pages:   pages,
		css:     css,
		cssPath: "/static/app." + digest + ".css",
		cssETag: `"` + digest + `"`,
		files:   map[string]asset{},
	}

	for pattern, kind := range map[string]string{
		"fonts/*.woff2": "font/woff2",
		"img/*.svg":     "image/svg+xml",
		"img/*.png":     "image/png",
	} {
		names, err := fs.Glob(chrome, pattern)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", pattern, err)
		}

		for _, name := range names {
			body, err := fs.ReadFile(chrome, name)
			if err != nil {
				return nil, fmt.Errorf("%s could not be read: %w", name, err)
			}

			sum := sha256.Sum256(body)
			rn.files[name] = asset{body: body, etag: `"` + hex.EncodeToString(sum[:])[:12] + `"`, kind: kind}
		}
	}

	return rn, nil
}

// StylesheetPath is where the stylesheet is served, including its hash.
func (rn *Renderer) StylesheetPath() string { return rn.cssPath }

// Render writes a page, in the language the request asked for.
//
// Executed into a buffer first, and the status written only once that
// succeeded: a template error halfway down would otherwise be a half page
// under a 200, which looks like it worked.
func (rn *Renderer) Render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	set, ok := rn.pages[name]
	if !ok {
		rn.log.Error("a handler asked for a page that does not exist",
			"id", web.RequestIDFrom(r.Context()), "template", name)
		http.Error(w, "Something went wrong on our end. Try once more in a minute; if it happens again, tell the garden stewards what you were doing.", http.StatusInternalServerError)

		return
	}

	lang := mid.LangFrom(r.Context())
	other := types.Spanish
	if lang == types.Spanish {
		other = types.English
	}

	var steward string
	if u, ok := mid.StewardFrom(r.Context()); ok {
		steward = u.Email.String()
	}

	var buf bytes.Buffer

	if err := set.ExecuteTemplate(&buf, "base", Shell{
		Stylesheet: rn.cssPath,
		Lang:       lang,
		OtherLang:  other,
		OtherURL:   mid.SwitchURL(r, other),
		Steward:    steward,
		Data:       data,
	}); err != nil {
		rn.log.Error("a page could not be rendered",
			"id", web.RequestIDFrom(r.Context()), "template", name, "err", err)
		http.Error(w, "Something went wrong on our end. Try once more in a minute; if it happens again, tell the garden stewards what you were doing.", http.StatusInternalServerError)

		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")

	// The language is chosen from a cookie and Accept-Language, so a cache
	// between here and the phone must not hand one person's Spanish page to
	// the next person's English request.
	h.Add("Vary", "Cookie, Accept-Language")
	h.Set("Content-Language", string(lang))
	w.WriteHeader(status)

	if _, err := buf.WriteTo(w); err != nil {
		rn.log.Warn("a page was cut off while being sent",
			"id", web.RequestIDFrom(r.Context()), "template", name, "err", err)
	}
}

// Stylesheet serves the stylesheet, cacheable for ever because its path
// carries its hash. That overrides the page policy's no-store, which is right
// for a steward's edit screen and wrong for a file compiled into the binary.
func (rn *Renderer) Stylesheet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/css; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("ETag", rn.cssETag)

		http.ServeContent(w, r, "app.css", startup, bytes.NewReader(rn.css))
	}
}

// Files serves the fonts and images, from /static/fonts/{file} and
// /static/img/{file}. A name that is not ours is a plain 404.
//
// Not behind any sign-in: these are files in the binary rather than anybody's
// data, and the sign-in page is drawn with them before there is a session.
func (rn *Renderer) Files(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := rn.files[dir+"/"+r.PathValue("file")]
		if !ok {
			http.NotFound(w, r)

			return
		}

		h := w.Header()
		h.Set("Content-Type", f.kind)
		h.Set("Cache-Control", "public, max-age=31536000")
		h.Set("ETag", f.etag)

		http.ServeContent(w, r, r.PathValue("file"), startup, bytes.NewReader(f.body))
	}
}

// startup is the modification time reported for embedded files. embed.FS
// records none, and a zero time makes ServeContent skip conditional requests.
var startup = time.Now()
