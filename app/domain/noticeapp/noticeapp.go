// Package noticeapp is the stewards' screen of every message the app sends
// to volunteers, each as it goes by email and as it goes by text, in English
// and in Spanish.
//
// The stewards asked to see what will go out both ways before it does
// (2026-10-21). The messages a steward sends by pressing a button get a
// preview of their own, with the real day in it, at the moment of sending;
// this screen is the standing answer to "what does the welcome say?", and
// the place to notice that a Spanish message is still waiting for its
// translation before anyone receives it in English.
//
// Every app that writes to volunteers lists its messages as notices.Kind,
// with an invented sample, and the muxer gathers them here; this app imports
// none of them.
package noticeapp

import (
	"embed"
	"log/slog"
	"net/http"

	"github.com/jroedel/stewards/app/sdk/notices"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// Path is the screen.
const Path = "/steward/messages"

// Config is what this app needs. Texts is whether texts can be sent at all,
// which the screen says when they cannot.
type Config struct {
	Log    *slog.Logger
	Render *page.Renderer
	Kinds  []notices.Kind
	Texts  bool
}

// Routes mounts the screen behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	mux.Handle("GET "+Path, guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.Render.Render(w, r, http.StatusOK, "steward-messages", view(cfg))
	})))
}

type kindView struct {
	Name, When types.Text
	Previews   []notices.Preview
}

type listView struct {
	Kinds []kindView
	Texts bool
}

func view(cfg Config) listView {
	v := listView{Texts: cfg.Texts}

	for _, k := range cfg.Kinds {
		kv := kindView{Name: k.Name, When: k.When}

		for _, l := range []types.Lang{types.English, types.Spanish} {
			kv.Previews = append(kv.Previews, notices.PreviewOf(cfg.Render, l, k.Words, k.Sample(l)))
		}

		v.Kinds = append(v.Kinds, kv)
	}

	return v
}
