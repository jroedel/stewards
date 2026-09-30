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

func TestAMissingFileSaysWhatToCopy(t *testing.T) {
	_, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err == nil || !strings.Contains(err.Error(), "config.example.toml") {
		t.Errorf("got %v, want advice to copy config.example.toml", err)
	}
}
