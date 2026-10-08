// Command stewards serves the garden steward app for the Trail of the Saints.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/jroedel/stewards/app/sdk/muxer"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/inbox/stores/inboxdb"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/domain/nursery/stores/nurserydb"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
	"github.com/jroedel/stewards/business/domain/photo/stores/photofs"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/domain/subscriber/stores/subscriberdb"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/domain/translation/stores/translationdb"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/foundation/logger"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sqldb"
	"github.com/jroedel/stewards/foundation/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stewards:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "config.toml", "path to the configuration file")
		check      = flag.Bool("check", false, "load and validate the configuration, print a summary, and exit")
	)

	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}

	// -check exists for the deploy script, which runs the *new* binary against
	// the *new* config while the old one is still serving. A config the new
	// binary will not accept is then a deploy that swaps nothing, rather than a
	// process that dies after the rename with the previous one already gone.
	if *check {
		fmt.Print(cfg.summary())

		return nil
	}

	logFile, shouldClose, err := openLog(cfg.Log.File)
	if err != nil {
		return err
	}
	if shouldClose {
		defer logFile.Close()
	}

	level, err := logger.Level(cfg.Log.Level)
	if err != nil {
		return fmt.Errorf("%s: %w", *configPath, err)
	}

	log := logger.New(logFile, level)

	db, err := sqldb.Open(cfg.DB.Path)
	if err != nil {
		return fmt.Errorf("opening the database at %s: %w", cfg.DB.Path, err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := prepare(ctx, db); err != nil {
		return err
	}

	// After prepare, because the file has to exist before its mode can be set
	// and prepare is what creates it on a fresh install.
	if err := sqldb.Restrict(cfg.DB.Path); err != nil {
		return err
	}

	// At startup, so a directory that cannot be made stops the deploy's
	// health check rather than the first steward's upload.
	photoFiles, err := photofs.NewStore(cfg.Photos.Dir)
	if err != nil {
		return err
	}

	// The inbox's photos in a directory of their own inside it: the same
	// tree to back up, and no route that serves a plant's photo can ever
	// be handed one of these, which nobody has looked at yet.
	inboxFiles, err := photofs.NewStore(filepath.Join(cfg.Photos.Dir, "inbox"))
	if err != nil {
		return err
	}

	expected := expectedSchema()

	if err := sqldb.CheckSchema(ctx, db, expected); err != nil {
		return fmt.Errorf("the database does not match this binary: %w", err)
	}

	// A nil Sender rather than one that discards, when no relay is set, so
	// that a missing relay is logged where the link went missing instead of
	// looking like a delivery. Declared as the interface and assigned only
	// on success: a nil *SMTP inside a Sender is not a nil Sender.
	var sender mail.Sender
	if cfg.Mail.Host != "" {
		smtp, err := mail.NewSMTP(mail.Config{
			Host: cfg.Mail.Host, Port: cfg.Mail.Port,
			User: cfg.Mail.User, Password: cfg.Mail.Password,
			From: cfg.Mail.From, FromName: cfg.Mail.FromName,
		})
		if err != nil {
			return fmt.Errorf("%s [mail]: %w", *configPath, err)
		}

		sender = smtp
	}

	translations, err := translationbus.NewBusiness(ctx, translationdb.NewStore(db), nil)
	if err != nil {
		return err
	}

	places := placebus.NewBusiness(placedb.NewStore(db), translations, nil)
	species := speciesbus.NewBusiness(speciesdb.NewStore(db), translations, nil)
	workdays := workdaybus.NewBusiness(workdaydb.NewStore(db), translations, nil)
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)
	photos := photobus.NewBusiness(photodb.NewStore(db), photoFiles, nil)
	listings := listingbus.NewBusiness(listingdb.NewStore(db), translations, nil)

	if err := moveTranslations(ctx, log, places, species, listings, workdays); err != nil {
		return err
	}

	translations.ReadFrom(places, species, listings, workdays)

	nursery := nurserybus.NewBusiness(nurserydb.NewStore(db), nil)
	inbox := inboxbus.NewBusiness(inboxdb.NewStore(db), inboxFiles, inboxbus.Deps{Photos: photos, Listings: listings, Stock: nursery}, nil)
	subscribers := subscriberbus.NewBusiness(subscriberdb.NewStore(db), nil)
	go prune(ctx, log, users, subscribers, inbox)

	handler, err := muxer.New(muxer.Config{
		Log:      log,
		DB:       db,
		Expected: expected,
		Places:   places,
		Species:  species,
		Listings: listings,
		Photos:   photos,
		Users:    users,
		Workdays: workdays,
		Inbox:    inbox,
		Nursery:  nursery,

		Translations: translations,

		Subscribers: subscribers,
		BaseURL:     cfg.Server.BaseURL,
		Mail:        sender,
		Bootstrap:   cfg.Auth.BootstrapSecret,
	})
	if err != nil {
		return err
	}

	log.Info("starting", "addr", cfg.Server.Addr, "db", cfg.DB.Path, "photos", cfg.Photos.Dir, "memory_limit_mb", setMemoryLimit(os.Getenv("GOMEMLIMIT"))>>20)

	return web.Serve(ctx, log, cfg.Server.ShutdownGrace.Duration, cfg.Server.Addr, handler)
}

// memoryLimit is the heap size the garbage collector works to stay under: a
// soft limit, which the runtime meets by collecting sooner, never by refusing
// an allocation.
//
// The shared host kills the process, silently, at about 300 MB (2026-10-01).
// Go's default is to let the heap grow to twice what was live at the last
// collection, so between two photos of a batch the pixels of the one just
// finished can still be held while the next is decoded. Measured on four
// 48-megapixel photos in one inbox batch: a peak of 211 MB without a limit,
// 170 MB with this one, which is what a single photo that size needs while it
// is scaled; 12- and 24-megapixel batches are unchanged at 69 and 115 MB.
//
// Below that 170 it would only make the collector run continuously while
// a large photo is worked on, for no saving: a limit cannot free memory that
// is still in use.
const memoryLimit = 150 << 20

// setMemoryLimit applies memoryLimit and returns the limit in force. A
// GOMEMLIMIT in the environment wins: the runtime has already applied it, and
// somebody at a terminal set it on purpose.
func setMemoryLimit(env string) int64 {
	if env != "" {
		return debug.SetMemoryLimit(-1) // -1 reads the limit without changing it
	}

	debug.SetMemoryLimit(memoryLimit)

	return memoryLimit
}

// prepare runs every store's Init, in foreign-key order.
//
// A list rather than a loop over anything clever, because the order is the
// order the references point in. Places come first of the domains: every
// layer points at a place, and the listings of species at places reference
// both places and species, so they go after both; so do photos, for the same
// reason. The inbox names a place and the steward who sent each photo, so it
// follows the stewards. Stewardship days, the email list and the translation
// memory reference nothing, and go last. A store added in
// the wrong place fails at startup on a fresh database and nowhere else, which
// is the cheapest moment for it to fail.
func prepare(ctx context.Context, db *sql.DB) error {
	for _, step := range []struct {
		what string
		init func(context.Context, *sql.DB) error
	}{
		{"the infrastructure tables", sqldb.Init},
		{"places", placedb.Init},
		{"species", speciesdb.Init},
		{"plants listed at places", listingdb.Init},
		{"photos", photodb.Init},
		{"stewards", userdb.Init},
		{"the photo inbox", inboxdb.Init},
		{"nursery stock", nurserydb.Init},
		{"stewardship days", workdaydb.Init},
		{"the email list", subscriberdb.Init},
		{"the translations", translationdb.Init},
	} {
		if err := step.init(ctx, db); err != nil {
			return fmt.Errorf("preparing %s: %w", step.what, err)
		}
	}

	return nil
}

// expectedSchema is every column this binary reads, merged. Each store adds
// its Expected to the list as it lands.
//
// /healthz re-checks it on every call, so a binary rolled back onto a newer
// schema reports unhealthy rather than serving against a database it does not
// understand.
func expectedSchema() sqldb.Expected {
	expected := maps.Clone(sqldb.Infrastructure)

	for _, store := range []sqldb.Expected{
		placedb.Expected,
		speciesdb.Expected,
		listingdb.Expected,
		photodb.Expected,
		userdb.Expected,
		inboxdb.Expected,
		nurserydb.Expected,
		workdaydb.Expected,
		subscriberdb.Expected,
		translationdb.Expected,
	} {
		maps.Copy(expected, store)
	}

	return expected
}

// moveTranslations runs each domain's MoveTranslations: the Spanish written
// beside the English before the translation memory existed goes into the
// memory, and the record keeps its English alone.
//
// At every startup rather than once, because a binary rolled back to before
// the memory writes Spanish beside English again, and the next deploy should
// pick that up without anyone remembering to. After the first run each is a
// read of every record and no writes. A failure stops the start, as an Init's
// would, so that it is seen. What was moved before it stays moved: a record
// is written back only once its words are in the memory, so there is no
// half-moved record to clean up.
func moveTranslations(ctx context.Context, log *slog.Logger, domains ...interface {
	MoveTranslations(context.Context) (int, error)
}) error {
	moved := 0

	for _, d := range domains {
		n, err := d.MoveTranslations(ctx)
		if err != nil {
			return fmt.Errorf("moving Spanish into the translation memory: %w", err)
		}

		moved += n
	}

	if moved > 0 {
		log.Info("moved Spanish into the translation memory", "records", moved)
	}

	return nil
}

// prune clears expired sign-in links and sessions, sign-ups to the email list
// left unconfirmed by its first version, and nursery stock photos past
// inboxbus.StockKept, at startup and then every six hours.
// Housekeeping: nothing depends on it for correctness, since every check
// reads the expiry, so a failure is logged and the next round tries again.
func prune(ctx context.Context, log *slog.Logger, users *userbus.Business, subscribers *subscriberbus.Business, inbox *inboxbus.Business) {
	tick := time.NewTicker(6 * time.Hour)
	defer tick.Stop()

	for {
		if err := users.Prune(ctx); err != nil && ctx.Err() == nil {
			log.Warn("expired sign-ins could not be pruned", "error", err)
		}

		if err := subscribers.Prune(ctx); err != nil && ctx.Err() == nil {
			log.Warn("the email list could not be pruned", "error", err)
		}

		if err := inbox.PruneStock(ctx); err != nil && ctx.Err() == nil {
			log.Warn("old nursery stock photos could not be removed", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
