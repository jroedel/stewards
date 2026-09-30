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
	"syscall"
	"time"

	"github.com/jroedel/stewards/app/sdk/muxer"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
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

	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)
	go prune(ctx, log, users)

	handler, err := muxer.New(muxer.Config{
		Log:       log,
		DB:        db,
		Expected:  expected,
		Places:    placebus.NewBusiness(placedb.NewStore(db), nil),
		Users:     users,
		BaseURL:   cfg.Server.BaseURL,
		Mail:      sender,
		Bootstrap: cfg.Auth.BootstrapSecret,
	})
	if err != nil {
		return err
	}

	log.Info("starting", "addr", cfg.Server.Addr, "db", cfg.DB.Path)

	return web.Serve(ctx, log, cfg.Server.ShutdownGrace.Duration, cfg.Server.Addr, handler)
}

// prepare runs every store's Init, in foreign-key order.
//
// A list rather than a loop over anything clever, because the order is the
// order the references point in. Places come first of the domains: every
// layer points at a place, and when species-at-a-place arrives it will
// reference both places and species, so it goes after both. A store added in
// the wrong place fails at startup on a fresh database and nowhere else, which
// is the cheapest moment for it to fail.
func prepare(ctx context.Context, db *sql.DB) error {
	for _, step := range []struct {
		what string
		init func(context.Context, *sql.DB) error
	}{
		{"the infrastructure tables", sqldb.Init},
		{"places", placedb.Init},
		{"stewards", userdb.Init},
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
		userdb.Expected,
	} {
		maps.Copy(expected, store)
	}

	return expected
}

// prune clears expired sign-in links and sessions, at startup and then every
// six hours. Housekeeping: nothing depends on it for correctness, since every
// check reads the expiry, so a failure is logged and the next round tries
// again.
func prune(ctx context.Context, log *slog.Logger, users *userbus.Business) {
	tick := time.NewTicker(6 * time.Hour)
	defer tick.Stop()

	for {
		if err := users.Prune(ctx); err != nil && ctx.Err() == nil {
			log.Warn("expired sign-ins could not be pruned", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
