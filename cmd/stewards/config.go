package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// config is the whole runtime configuration, read from a TOML file.
//
// A file rather than environment variables, because the deploy target is a
// shared hosting account with no systemd and therefore no EnvironmentFile: the
// process is started by a shell script from cron, and a file it reads is the
// only place a secret can live that is not the command line. The secrets are
// the mail relay's password and the one-time bootstrap secret, and they reach
// the server from a person's machine over ssh (make deploy-send-secrets),
// never through GitHub.
//
// Every field is tagged explicitly. The decoder matches a key to a field name
// case-insensitively, so `shutdown_grace` does not find `ShutdownGrace` -- and
// because an unrecognised key is a startup error here, an untagged field turns
// a correct config file into a refusal to start.
type config struct {
	Server struct {
		Addr string `toml:"addr"`

		// BaseURL is the public https address: the origin every write must
		// come from, and the start of the link in a sign-in email. From
		// here and never from the request, because behind konsoleH's proxy
		// the Host header is the loopback. Empty means sign-in is off.
		BaseURL       string   `toml:"base_url"`
		ShutdownGrace duration `toml:"shutdown_grace"`
	} `toml:"server"`
	DB struct {
		Path string `toml:"path"`
	} `toml:"db"`
	Log struct {
		Level string `toml:"level"`
		File  string `toml:"file"`
	} `toml:"log"`

	Auth struct {
		// BootstrapSecret lets the first steward in without email, once.
		// Empty means its page is not served.
		BootstrapSecret string `toml:"bootstrap_secret"`
	} `toml:"auth"`

	// Mail is the relay sign-in links go out through. Without it the app
	// runs, and nobody can sign in except by the bootstrap: -check says so
	// in capitals.
	Mail struct {
		Host     string `toml:"host"`
		Port     int    `toml:"port"`
		User     string `toml:"user"`
		Password string `toml:"password"`
		From     string `toml:"from"`
		FromName string `toml:"from_name"`
	} `toml:"mail"`
}

// minBootstrap is the shortest bootstrap secret the binary accepts. It mints
// a session with no email, so it is a password to the whole app for as long as
// it is unspent; make bootstrap-secret prints one of 48.
const minBootstrap = 32

// defaultAddr is a loopback port no sibling project on the same account uses:
// dropin-forms has 8410 and 8411, mass-intentions 8431.
const defaultAddr = "127.0.0.1:8451"

// duration is a time.Duration that TOML can read as "15s".
type duration struct{ time.Duration }

func (d *duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("%q is not a duration; write it like 15s or 2m", text)
	}

	d.Duration = parsed

	return nil
}

// loadConfig reads and validates the file, or explains what is wrong with it.
//
// An unrecognised key is an error rather than a warning. A mistyped key in a
// file that is silently accepted is a setting that looks configured and is not,
// and the way that surfaces is a production incident rather than a startup
// message.
func loadConfig(path string) (config, error) {
	var cfg config

	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("there is no config file at %s; copy config.example.toml to %s and edit it", path, path)
		}

		return cfg, fmt.Errorf("reading %s: %w", path, err)
	}

	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}

		return cfg, fmt.Errorf("%s has settings this binary does not know: %s. Check them for a typo", path, strings.Join(names, ", "))
	}

	if cfg.Server.Addr == "" {
		cfg.Server.Addr = defaultAddr
	}

	if cfg.Server.ShutdownGrace.Duration == 0 {
		cfg.Server.ShutdownGrace.Duration = 15 * time.Second
	}

	if cfg.DB.Path == "" {
		return cfg, fmt.Errorf("%s needs [db] path, the file the garden's records are kept in", path)
	}

	// Optional, and sign-in is off without it. Not required, because the
	// config.toml already on the server predates it: a binary that refused
	// to start without base_url would fail the deploy's pre-flight, and a
	// config.toml sent first would be refused by the binary still running,
	// which rejects keys it does not know. So the binary goes first, and
	// the config after it -- see the PR that added this.
	if cfg.Server.BaseURL != "" {
		base, err := url.Parse(cfg.Server.BaseURL)

		switch {
		case err != nil || base.Host == "" || (base.Path != "" && base.Path != "/") || base.RawQuery != "":
			return cfg, fmt.Errorf("%s has [server] base_url %q; write only the scheme and host, such as https://stewards.schoenstatt-fathers.us", path, cfg.Server.BaseURL)
		case base.Scheme != "https" && !(base.Scheme == "http" && isLoopback(base.Hostname())):
			// http only for a developer's own machine. Anywhere else the
			// session cookie, which is Secure, would never come back.
			return cfg, fmt.Errorf("%s has [server] base_url %q; it must start https:// unless it is this machine", path, cfg.Server.BaseURL)
		}

		cfg.Server.BaseURL = base.Scheme + "://" + base.Host
	}

	// Checked as a set. A host with no password is a relay somebody stopped
	// filling in halfway, and it would fail at the first sign-in rather than
	// here, which is the worse moment by far.
	if cfg.Mail.Host != "" {
		for field, value := range map[string]string{
			"user": cfg.Mail.User, "password": cfg.Mail.Password, "from": cfg.Mail.From,
		} {
			if value == "" {
				return cfg, fmt.Errorf("%s sets [mail] host but not %s; sign-in links would fail at the first attempt", path, field)
			}
		}

		if cfg.Mail.Port == 0 {
			cfg.Mail.Port = 587
		}

		if cfg.Mail.FromName == "" {
			cfg.Mail.FromName = "Garden stewards"
		}
	}

	if n := len(cfg.Auth.BootstrapSecret); n > 0 && n < minBootstrap {
		return cfg, fmt.Errorf("%s has an [auth] bootstrap_secret of %d characters; it signs somebody in with no email, so it needs at least %d. Run make bootstrap-secret for one", path, n, minBootstrap)
	}

	return cfg, nil
}

// summary is what -check prints. It names every setting that decides
// behaviour, and will never name one that is a secret.
func (c config) summary() string {
	var b strings.Builder

	fmt.Fprintf(&b, "listening on   %s\n", c.Server.Addr)
	fmt.Fprintf(&b, "public address %s\n", orElse(c.Server.BaseURL, "NOT SET - sign-in is off"))
	fmt.Fprintf(&b, "database       %s\n", c.DB.Path)
	fmt.Fprintf(&b, "log level      %s\n", orElse(c.Log.Level, "info"))
	fmt.Fprintf(&b, "log file       %s\n", orElse(c.Log.File, "(stderr)"))
	fmt.Fprintf(&b, "shutdown grace %s\n", c.Server.ShutdownGrace.Duration)

	mail := "NOT CONFIGURED - no steward can be sent a sign-in link"
	if c.Mail.Host != "" {
		mail = fmt.Sprintf("%s:%d, from %s", c.Mail.Host, c.Mail.Port, c.Mail.From)
	}

	bootstrap := "no secret"
	if c.Auth.BootstrapSecret != "" {
		bootstrap = "secret set"
	}

	fmt.Fprintf(&b, "outgoing mail  %s\n", mail)
	fmt.Fprintf(&b, "bootstrap      %s\n", bootstrap)

	return b.String()
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}

	return s
}

// openLog returns the destination for log lines, and whether the caller owns
// it. Empty means stderr, which is what the supervisor on the server redirects
// into a file -- so the process never has to know it is being supervised.
func openLog(path string) (*os.File, bool, error) {
	if path == "" {
		return os.Stderr, false, nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("opening the log file %s: %w", path, err)
	}

	return f, true, nil
}
