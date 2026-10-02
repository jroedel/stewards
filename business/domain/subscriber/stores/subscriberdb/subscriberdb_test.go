package subscriberdb_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/subscriber/stores/subscriberdb"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// A pending row as the double opt-in wrote it, from the few days the list
// had one: confirm_hash and confirm_expires_at set, confirmed_at NULL.
// Written out literally, as that version's INSERT did, not through Add.
func TestARowLeftPendingIsNotOnTheListUntilAskedAgain(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return subscriberdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, subscriberdb.Expected); err != nil {
		t.Fatalf("CheckSchema: %v", err)
	}

	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	id := types.NewID()

	for _, pending := range []struct {
		id, email, token string
		expires          time.Time
	}{
		{id.String(), "walker@example.org", "token-walker", now.Add(6 * 24 * time.Hour)},
		{types.NewID().String(), "stale@example.org", "token-stale", now.Add(-time.Hour)},
	} {
		if _, err := db.ExecContext(t.Context(), `
INSERT INTO subscribers (id, email, lang, unsubscribe, confirm_hash, confirm_expires_at, created_at)
VALUES (?, ?, 'en', ?, x'00', ?, ?)`,
			pending.id, pending.email, pending.token, pending.expires.UnixMilli(), now.Add(-24*time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}

	store := subscriberdb.NewStore(db)

	if on, _ := store.All(t.Context()); len(on) != 0 {
		t.Fatalf("a pending row is on the list: %+v", on)
	}

	walker, _ := types.ParseEmail("walker@example.org")

	if _, err := store.ByEmail(t.Context(), walker); err == nil {
		t.Error("a pending row is found as if on the list")
	}

	// Signing up again puts it on the list, keeping its ID and token.
	got, added, err := store.Add(t.Context(), subscriberbus.Subscriber{
		ID: types.NewID(), Email: walker, Lang: types.English, CreatedAt: now, Unsubscribe: "a-new-token",
	})
	if err != nil || !added || got.ID != id || got.Unsubscribe != "token-walker" {
		t.Errorf("Add over a pending row: %+v added=%v %v", got, added, err)
	}

	// Pruning deletes the one whose old link has expired, and leaves the
	// list alone.
	if err := store.Prune(t.Context(), now, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	var rows int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM subscribers`).Scan(&rows); err != nil {
		t.Fatal(err)
	}

	if on, _ := store.All(t.Context()); rows != 1 || len(on) != 1 || on[0].ID != id {
		t.Errorf("after prune: %d rows, list %+v", rows, on)
	}
}
