package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Store.Backend != "memory" {
		t.Fatalf("Backend = %q, want memory", cfg.Store.Backend)
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `default_actor = "claude"

[store]
backend = "jsonfile"

[store.options]
path = ".factotum/factotum.json"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Store.Backend != "jsonfile" {
		t.Fatalf("Backend = %q, want jsonfile", cfg.Store.Backend)
	}
	if cfg.Store.Options["path"] != ".factotum/factotum.json" {
		t.Fatalf("Options[path] = %q", cfg.Store.Options["path"])
	}
	if cfg.DefaultActor != "claude" {
		t.Fatalf("DefaultActor = %q, want claude", cfg.DefaultActor)
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("not = = toml"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want parse error")
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	cfg := Default()
	cfg.ApplyEnv(func(key string) string {
		switch key {
		case "FACTOTUM_STORE":
			return "sqlite"
		case "FACTOTUM_DEFAULT_ACTOR":
			return "codex"
		case "FACTOTUM_STORE_OPTS":
			return "path=/tmp/x.db, cache=shared"
		case "FACTOTUM_NO_HINTS":
			return "1"
		default:
			return ""
		}
	})
	if cfg.Store.Backend != "sqlite" {
		t.Fatalf("Backend = %q, want sqlite", cfg.Store.Backend)
	}
	if cfg.DefaultActor != "codex" {
		t.Fatalf("DefaultActor = %q, want codex", cfg.DefaultActor)
	}
	if cfg.Store.Options["path"] != "/tmp/x.db" || cfg.Store.Options["cache"] != "shared" {
		t.Fatalf("Options = %v", cfg.Store.Options)
	}
	if !cfg.NoHints {
		t.Fatal("NoHints = false, want true")
	}
}

func TestApplyEnvNoHintsFalsey(t *testing.T) {
	for _, value := range []string{"", "0", "false", "no"} {
		cfg := Default()
		cfg.ApplyEnv(func(key string) string {
			if key == "FACTOTUM_NO_HINTS" {
				return value
			}
			return ""
		})
		if cfg.NoHints {
			t.Fatalf("FACTOTUM_NO_HINTS=%q set NoHints", value)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() error = %v", err)
	}
	cfg := Default()
	cfg.Store.Backend = "  "
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}
