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

// emailOnlySchema is the list as it stood until 2026-10-21, written out
// literally rather than derived from the new one: email NOT NULL, the double
// opt-in's columns, and the welcome-mail record beside it.
const emailOnlySchema = `
CREATE TABLE subscribers (
    id                  TEXT    PRIMARY KEY,
    email               TEXT    NOT NULL UNIQUE,
    lang                TEXT    NOT NULL,
    unsubscribe         TEXT    NOT NULL UNIQUE,
    confirm_hash        BLOB,
    confirm_expires_at  INTEGER,
    confirmed_at        INTEGER,
    created_at          INTEGER NOT NULL
) STRICT;

CREATE TABLE subscriber_mails (
    email    TEXT    NOT NULL,
    sent_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX subscriber_mails_email ON subscriber_mails (email, sent_at);
CREATE INDEX subscriber_mails_sent ON subscriber_mails (sent_at);
`

func TestInitRebuildsTheEmailOnlyList(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.ExecContext(t.Context(), emailOnlySchema); err != nil {
		t.Fatal(err)
	}

	on := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	walker := types.NewID()

	// One on the list, in Spanish; one left pending by the double opt-in;
	// and a day's welcome record.
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO subscribers (id, email, lang, unsubscribe, confirmed_at, created_at) VALUES (?, 'walker@example.invalid', 'es', 'token-walker', ?, ?)`,
			[]any{walker.String(), on.UnixMilli(), on.UnixMilli()}},
		{`INSERT INTO subscribers (id, email, lang, unsubscribe, confirm_hash, confirm_expires_at, created_at) VALUES (?, 'pending@example.invalid', 'en', 'token-pending', x'00', ?, ?)`,
			[]any{types.NewID().String(), on.UnixMilli(), on.UnixMilli()}},
		{`INSERT INTO subscriber_mails (email, sent_at) VALUES ('walker@example.invalid', ?)`, []any{on.UnixMilli()}},
	} {
		if _, err := db.ExecContext(t.Context(), row.sql, row.args...); err != nil {
			t.Fatal(err)
		}
	}

	for range 2 { // the rebuild, then a startup that finds nothing to do
		if err := subscriberdb.Init(t.Context(), db); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, subscriberdb.Expected); err != nil {
		t.Fatalf("CheckSchema: %v", err)
	}

	store := subscriberdb.NewStore(db)

	all, err := store.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(all) != 1 {
		t.Fatalf("after the rebuild: %+v, want walker alone", all)
	}

	w := all[0]
	if w.ID != walker || w.Contact.Email.String() != "walker@example.invalid" || !w.Contact.Phone.Zero() ||
		w.Lang != types.Spanish || w.Unsubscribe != "token-walker" || !w.CreatedAt.Equal(on) {
		t.Errorf("walker came through as %+v", w)
	}

	var old int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE name IN ('subscriber_mails', 'subscribers_rebuilt')`).Scan(&old); err != nil || old != 0 {
		t.Errorf("%d old tables left, %v", old, err)
	}

	// The new shape works: a phone added to walker, and a phone-only row.
	phone, _ := types.ParsePhone("512 555 0123")
	if ok, err := store.Extend(t.Context(), walker, types.Contact{Phone: phone}); !ok || err != nil {
		t.Errorf("Extend: %v %v", ok, err)
	}

	other, _ := types.ParsePhone("512 555 0124")
	if ok, err := store.Add(t.Context(), subscriberbus.Subscriber{
		ID: types.NewID(), Contact: types.Contact{Phone: other}, Lang: types.English, CreatedAt: on, Unsubscribe: "token-other",
	}); !ok || err != nil {
		t.Errorf("Add phone only: %v %v", ok, err)
	}
}

func TestTheStoreKeepsEachHalfUnique(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := subscriberdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	store := subscriberdb.NewStore(db)
	ana, _ := types.ParseContact("ana@example.invalid", "512 555 0123")
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

	add := func(c types.Contact, token string) (bool, error) {
		return store.Add(t.Context(), subscriberbus.Subscriber{ID: types.NewID(), Contact: c, Lang: types.English, CreatedAt: now, Unsubscribe: token})
	}

	if ok, err := add(ana, "t1"); !ok || err != nil {
		t.Fatalf("Add: %v %v", ok, err)
	}

	// The phone again, beside another address: refused, not an error.
	clash, _ := types.ParseContact("other@example.invalid", "512 555 0123")
	if ok, err := add(clash, "t2"); ok || err != nil {
		t.Errorf("a phone on the list twice: %v %v", ok, err)
	}

	// Neither half: the CHECK.
	if _, err := add(types.Contact{}, "t3"); err == nil {
		t.Error("a row with neither half was stored")
	}

	// A STOP on a person with both keeps them, by email.
	if err := store.Stopped(t.Context(), ana.Phone); err != nil {
		t.Fatal(err)
	}

	got, _ := store.ByContact(t.Context(), types.Contact{Email: ana.Email})
	if len(got) != 1 || !got[0].Contact.Phone.Zero() {
		t.Errorf("after STOP: %+v", got)
	}

	// And on a person with only a phone, takes them off.
	solo, _ := types.ParseContact("", "512 555 0125")
	if _, err := add(solo, "t4"); err != nil {
		t.Fatal(err)
	}

	if err := store.Stopped(t.Context(), solo.Phone); err != nil {
		t.Fatal(err)
	}

	if all, _ := store.All(t.Context()); len(all) != 1 {
		t.Errorf("after the phone-only STOP: %+v", all)
	}
}
