package userdb_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

var now = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

func open(t *testing.T) (*sql.DB, *userdb.Store) {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatalf("sqldb.Init: %v", err)
	}

	// Twice, because Init runs at every startup.
	for range 2 {
		if err := userdb.Init(t.Context(), db); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}

	return db, userdb.NewStore(db)
}

func steward(t *testing.T, s *userdb.Store, addr string) userbus.User {
	t.Helper()

	email, err := types.ParseEmail(addr)
	if err != nil {
		t.Fatal(err)
	}

	u := userbus.User{ID: types.NewID(), Email: email, Name: "A Steward", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateUser(t.Context(), u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	return u
}

func token(u userbus.User, at time.Time) userbus.Token {
	return userbus.Token{ID: types.NewID(), UserID: u.ID, Hash: []byte("hash"), CreatedAt: at, ExpiresAt: at.Add(userbus.LinkLife)}
}

func TestSchemaMatchesWhatTheStoreReads(t *testing.T) {
	db, _ := open(t)

	if err := sqldb.CheckSchema(t.Context(), db, userdb.Expected); err != nil {
		t.Errorf("CheckSchema: %v", err)
	}
}

func TestAnAccountRoundTripsAndAnAddressIsTakenOnce(t *testing.T) {
	_, s := open(t)
	u := steward(t, s, "steward@example.org")

	got, err := s.UserByEmail(t.Context(), u.Email)
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}

	if got != u {
		t.Errorf("read back %+v, want %+v", got, u)
	}

	again := u
	again.ID = types.NewID()
	if err := s.CreateUser(t.Context(), again); !errors.Is(err, userbus.ErrEmailTaken) {
		t.Errorf("a second account on one address: %v, want ErrEmailTaken", err)
	}

	if _, err := s.UserByID(t.Context(), types.NewID()); !errors.Is(err, userbus.ErrNotFound) {
		t.Errorf("an unknown account: %v, want ErrNotFound", err)
	}
}

// The cap counts links that could still be used, and nothing else: a spent
// link and an expired one are not held against the steward.
func TestTheCapCountsOnlyLiveLinks(t *testing.T) {
	_, s := open(t)
	u := steward(t, s, "steward@example.org")

	made := func(at time.Time) (userbus.Token, bool) {
		t.Helper()

		tok := token(u, at)
		ok, err := s.CreateToken(t.Context(), tok, 2)
		if err != nil {
			t.Fatalf("CreateToken: %v", err)
		}

		return tok, ok
	}

	first, ok1 := made(now)
	_, ok2 := made(now)
	_, ok3 := made(now)

	if !ok1 || !ok2 || ok3 {
		t.Fatalf("made = %v, %v, %v; want the third refused", ok1, ok2, ok3)
	}

	if _, err := s.UseToken(t.Context(), first.ID, now); err != nil {
		t.Fatal(err)
	}

	if _, ok := made(now); !ok {
		t.Error("a spent link still counted against the cap")
	}

	if _, ok := made(now.Add(userbus.LinkLife)); !ok {
		t.Error("links that had expired still counted against the cap")
	}
}

// Two requests holding one link, at once. Exactly one may spend it.
func TestALinkIsSpentExactlyOnce(t *testing.T) {
	_, s := open(t)
	tok := token(steward(t, s, "steward@example.org"), now)

	if _, err := s.CreateToken(t.Context(), tok, 3); err != nil {
		t.Fatal(err)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)

	for range 8 {
		wg.Go(func() {
			ok, err := s.UseToken(t.Context(), tok.ID, now)
			if err != nil {
				t.Error(err)
			}

			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("%d requests spent one link", wins)
	}

	got, err := s.TokenByID(t.Context(), tok.ID)
	if err != nil {
		t.Fatal(err)
	}

	if !got.UsedAt.Equal(now) {
		t.Errorf("used at %v, want %v", got.UsedAt, now)
	}
}

func TestTheBootstrapIsClaimedOnce(t *testing.T) {
	_, s := open(t)

	if spent, _ := s.BootstrapSpent(t.Context()); spent {
		t.Fatal("a fresh database says the bootstrap is spent")
	}

	first, err := s.ClaimBootstrap(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}

	second, err := s.ClaimBootstrap(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}

	if !first || second {
		t.Errorf("claims = %v, %v; want true then false", first, second)
	}

	if spent, _ := s.BootstrapSpent(t.Context()); !spent {
		t.Error("after a claim the bootstrap is not spent")
	}
}

func TestPruningKeepsWhatCanStillBeUsed(t *testing.T) {
	_, s := open(t)
	u := steward(t, s, "steward@example.org")

	old, live := token(u, now.Add(-time.Hour)), token(u, now)
	for _, tok := range []userbus.Token{old, live} {
		if _, err := s.CreateToken(t.Context(), tok, 3); err != nil {
			t.Fatal(err)
		}
	}

	session := userbus.Session{ID: types.NewID(), UserID: u.ID, Hash: []byte("h"), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	if err := s.PruneExpired(t.Context(), now); err != nil {
		t.Fatal(err)
	}

	if _, err := s.TokenByID(t.Context(), old.ID); !errors.Is(err, userbus.ErrNotFound) {
		t.Errorf("an expired link survived: %v", err)
	}

	if _, err := s.TokenByID(t.Context(), live.ID); err != nil {
		t.Errorf("a live link was pruned: %v", err)
	}

	if _, err := s.SessionByID(t.Context(), session.ID); err != nil {
		t.Errorf("a live session was pruned: %v", err)
	}

	if err := s.DeleteSession(t.Context(), session.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteSession(t.Context(), session.ID); !errors.Is(err, userbus.ErrNotFound) {
		t.Errorf("deleting a session twice: %v, want ErrNotFound", err)
	}
}
