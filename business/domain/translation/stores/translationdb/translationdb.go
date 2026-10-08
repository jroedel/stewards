// Package translationdb stores the translation memory in SQLite.
//
// One row per original, keyed by translationbus.Key of its text. The text
// itself is kept beside the key, so the memory can be read and checked by a
// person without a way to reverse a hash.
package translationdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of translationbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ translationbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"translations": {"key", "source", "source_lang", "translation", "origin", "checked", "created_at", "updated_at", "note"},
}

// Init creates the table. Idempotent, run at every startup. It references
// nothing, and nothing references it: a translation belongs to words, not to
// a record, so a record removed leaves its words' translation for any other
// record that says the same.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS translations (
    key          TEXT    PRIMARY KEY,
    source       TEXT    NOT NULL,

    -- 'en' or 'es': the language source is in.
    source_lang  TEXT    NOT NULL CHECK (source_lang IN ('en', 'es')),
    translation  TEXT    NOT NULL,

    -- One of translationbus's Origins.
    origin       TEXT    NOT NULL,
    checked      INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the translations table: %w", err)
	}

	// What a steward asks Claude to look at again, when they send a
	// translation back: "" for none. Added after the table first shipped,
	// so it is here and not in the CREATE.
	if err := sqldb.AddColumn(ctx, db, "translations", "note", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	return nil
}

const columns = `key, source, source_lang, translation, origin, checked, created_at, updated_at, note`

// Put inserts the translation, or replaces everything but when it was first
// made.
func (s *Store) Put(ctx context.Context, t translationbus.Translation) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO translations (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (key) DO UPDATE SET
    source = excluded.source, source_lang = excluded.source_lang,
    translation = excluded.translation, origin = excluded.origin,
    checked = excluded.checked, updated_at = excluded.updated_at,
    note = excluded.note`,
		args(t)...)
	if err != nil {
		return fmt.Errorf("saving the translation: %w", err)
	}

	return nil
}

// Add inserts the translation unless one with its key is there already.
func (s *Store) Add(ctx context.Context, t translationbus.Translation) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO translations (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (key) DO NOTHING`, args(t)...)
	if err != nil {
		return false, fmt.Errorf("adding the translation: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting the rows added: %w", err)
	}

	return n == 1, nil
}

// All is every translation.
func (s *Store) All(ctx context.Context) ([]translationbus.Translation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM translations ORDER BY created_at, key`)
	if err != nil {
		return nil, fmt.Errorf("reading the translations: %w", err)
	}
	defer rows.Close()

	var out []translationbus.Translation

	for rows.Next() {
		var (
			t                translationbus.Translation
			lang, origin     string
			checked          int64
			created, updated int64
		)

		if err := rows.Scan(&t.Key, &t.Source, &lang, &t.Translated, &origin, &checked, &created, &updated, &t.Note); err != nil {
			return nil, fmt.Errorf("reading a translation: %w", err)
		}

		if t.From, err = types.ParseLang(lang); err != nil {
			return nil, fmt.Errorf("the translation %s: %w", t.Key, err)
		}

		t.By = translationbus.Origin(origin)
		t.Checked = checked != 0
		t.CreatedAt = time.UnixMilli(created).UTC()
		t.UpdatedAt = time.UnixMilli(updated).UTC()

		out = append(out, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the translations: %w", err)
	}

	return out, nil
}

func args(t translationbus.Translation) []any {
	checked := int64(0)
	if t.Checked {
		checked = 1
	}

	return []any{
		t.Key, t.Source, string(t.From), t.Translated, string(t.By), checked,
		t.CreatedAt.UnixMilli(), t.UpdatedAt.UnixMilli(), t.Note,
	}
}
