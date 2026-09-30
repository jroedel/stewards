package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	return path
}

// The example file is the documentation, so it has to be a file this binary
// accepts. A setting added to the code and not to the example, or the other
// way round, fails here rather than on somebody's first run.
func TestTheExampleConfigLoads(t *testing.T) {
	cfg, err := loadConfig(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatalf("config.example.toml: %v", err)
	}

	if cfg.Server.Addr != defaultAddr {
		t.Errorf("the example listens on %s, want the default %s", cfg.Server.Addr, defaultAddr)
	}
}

func TestDefaultsFillWhatIsLeftOut(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, "[db]\npath = \"stewards.db\"\n"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.Server.Addr != defaultAddr {
		t.Errorf("addr = %q, want %q", cfg.Server.Addr, defaultAddr)
	}

	if cfg.Server.ShutdownGrace.Duration != 15*time.Second {
		t.Errorf("shutdown_grace = %s, want 15s", cfg.Server.ShutdownGrace.Duration)
	}
}

// Each of these is a config somebody could plausibly write, and each must stop
// the binary with a sentence naming the problem.
func TestABadConfigIsRefusedWithAReason(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"a mistyped key":      {"[db]\npath = \"s.db\"\npaht = \"s.db\"\n", "db.paht"},
		"no database path":    {"[server]\naddr = \"127.0.0.1:1\"\n", "[db] path"},
		"a duration in words": {"[db]\npath = \"s.db\"\n[server]\nshutdown_grace = \"soon\"\n", "15s"},

		// The public address is the origin of every form and the start of
		// every sign-in link, so a wrong one is refused rather than tidied.
		"a base_url with a path":   {db + "[server]\nbase_url = \"https://stewards.example.org/app\"\n", "only the scheme and host"},
		"a base_url on plain http": {db + "[server]\nbase_url = \"http://stewards.example.org\"\n", "https://"},
		"a base_url with no host":  {db + "[server]\nbase_url = \"stewards.example.org\"\n", "only the scheme and host"},

		// A relay somebody stopped filling in halfway fails here, not at the
		// first sign-in.
		"a relay with no password": {db + "[mail]\nhost = \"smtp.example.org\"\nuser = \"u\"\nfrom = \"f@example.org\"\n", "not password"},
		"a relay with no from":     {db + "[mail]\nhost = \"smtp.example.org\"\nuser = \"u\"\npassword = \"p\"\n", "not from"},

		"a short bootstrap secret": {db + "[auth]\nbootstrap_secret = \"too-short\"\n", "make bootstrap-secret"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadConfig(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("accepted")
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error does not mention %q: %v", tc.want, err)
			}
		})
	}
}

const db = "[db]\npath = \"s.db\"\n"

// The config already on the server has no base_url, so the binary must start
// without one -- with sign-in off, and -check saying so.
func TestWithNoBaseURLSignInIsOffAndSaysSo(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, db))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if !strings.Contains(cfg.summary(), "NOT SET - sign-in is off") {
		t.Errorf("the summary does not say sign-in is off:\n%s", cfg.summary())
	}
}

func TestAGoodSignInConfigIsTidiedAndSummarisedWithoutSecrets(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, db+`
[server]
base_url = "https://stewards.example.org/"

[auth]
bootstrap_secret = "abcdefghijklmnopqrstuvwxyz0123456789"

[mail]
host = "smtp.example.org"
user = "stewards@example.org"
password = "a-relay-password"
from = "stewards@example.org"
`))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	switch {
	case cfg.Server.BaseURL != "https://stewards.example.org":
		t.Errorf("base_url = %q, want the trailing slash gone", cfg.Server.BaseURL)
	case cfg.Mail.Port != 587:
		t.Errorf("port = %d, want the default 587", cfg.Mail.Port)
	case cfg.Mail.FromName != "Garden stewards":
		t.Errorf("from_name = %q, want the default", cfg.Mail.FromName)
	}

	summary := cfg.summary()
	for _, secret := range []string{"a-relay-password", "abcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(summary, secret) {
			t.Errorf("the summary prints a secret:\n%s", summary)
		}
	}

	if !strings.Contains(summary, "smtp.example.org:587") || !strings.Contains(summary, "secret set") {
		t.Errorf("the summary does not describe mail and the bootstrap:\n%s", summary)
	}
}

// http is fine for a developer's own machine, where the Secure cookie is still
// accepted, and nowhere else.
func TestHTTPIsAllowedOnlyOnThisMachine(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8451", "http://localhost:8451", "http://[::1]:8451"} {
		if _, err := loadConfig(writeConfig(t, db+"[server]\nbase_url = \""+u+"\"\n")); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestAMissingFileSaysWhatToCopy(t *testing.T) {
	_, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err == nil || !strings.Contains(err.Error(), "config.example.toml") {
		t.Errorf("got %v, want advice to copy config.example.toml", err)
	}
}
