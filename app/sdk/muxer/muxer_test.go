package muxer_test

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jroedel/stewards/app/sdk/muxer"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
	"github.com/jroedel/stewards/business/domain/photo/stores/photofs"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// /healthz is what the deploy rolls a release back on, so both of its answers
// matter: 200 on a database that matches, and 503 on one that does not.
func TestHealthzChecksTheSchema(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "stewards.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatalf("Init: %v", err)
	}

	for name, tc := range map[string]struct {
		want sqldb.Expected
		code int
	}{
		"a matching schema": {sqldb.Infrastructure, http.StatusOK},

		// A binary that expects a table this database does not have: the
		// shape of a rollback onto a newer schema, seen from the other side.
		"a schema missing a table": {sqldb.Expected{"places": {"id"}}, http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			h, err := muxer.New(muxer.Config{
				Log:      slog.New(slog.DiscardHandler),
				DB:       db,
				Expected: tc.want,
				Places:   placebus.NewBusiness(placedb.NewStore(db), nil),
				Species:  speciesbus.NewBusiness(speciesdb.NewStore(db), nil), Listings: listingbus.NewBusiness(listingdb.NewStore(db), nil), Photos: photos(t, db),
				Users: userbus.NewBusiness(slog.New(slog.DiscardHandler), userdb.NewStore(db), nil),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

			if rec.Code != tc.code {
				t.Errorf("status %d, want %d", rec.Code, tc.code)
			}

			// The policy reaches every response, the health check included.
			if rec.Header().Get("Content-Security-Policy") == "" {
				t.Error("no Content-Security-Policy on /healthz")
			}
		})
	}
}

// photos is the photo rules over this test's database and a directory of its
// own.
func photos(t *testing.T, db *sql.DB) *photobus.Business {
	t.Helper()

	files, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	return photobus.NewBusiness(photodb.NewStore(db), files, nil)
}
