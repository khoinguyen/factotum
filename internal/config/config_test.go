package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	return path
}

func emptyEnv(string) string { return "" }

func TestLoadMissingReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(Input{
		UserPath:    filepath.Join(dir, "user.toml"),
		ProjectPath: filepath.Join(dir, "project.toml"),
		Getenv:      emptyEnv,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Store.Backend != "memory" {
		t.Fatalf("Backend = %q, want memory", cfg.Store.Backend)
	}
	if cfg.Project != "" {
		t.Fatalf("Project = %q, want empty", cfg.Project)
	}
}

func TestLoadUserConfigSuppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
default_project = "kloobot"
default_actor = "khoi"

[store]
backend = "jsonfile"

[store.options]
path = "~/factotum.json"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "kloobot" {
		t.Fatalf("Project = %q, want kloobot", cfg.Project)
	}
	if cfg.DefaultActor != "khoi" {
		t.Fatalf("DefaultActor = %q, want khoi", cfg.DefaultActor)
	}
	if cfg.Store.Backend != "jsonfile" {
		t.Fatalf("Backend = %q, want jsonfile", cfg.Store.Backend)
	}
	if want := ExpandPath("~/factotum.json"); cfg.Store.Options["path"] != want {
		t.Fatalf("path = %q, want %q", cfg.Store.Options["path"], want)
	}
}

func TestProjectFileOverridesUser(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
default_project = "kloobot"
default_actor = "khoi"

[store]
backend = "jsonfile"
`)
	project := writeConfig(t, dir, "project.toml", `
project = "other"
default_actor = "agent"

[store]
backend = "sqlite"

[store.options]
path = ".factotum/other.db"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "other" {
		t.Fatalf("Project = %q, want other", cfg.Project)
	}
	if cfg.DefaultActor != "agent" {
		t.Fatalf("DefaultActor = %q, want agent", cfg.DefaultActor)
	}
	if cfg.Store.Backend != "sqlite" || cfg.Store.Options["path"] != ".factotum/other.db" {
		t.Fatalf("Store = %+v, want sqlite/.factotum/other.db", cfg.Store)
	}
}

func TestProjectRegistrySelectsDBPath(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
default_project = "kloobot"

[projects.kloobot]
db_path = "~/.factotum/kloobot.db"

[projects.kloobot.store]
backend = "sqlite"

[projects.kloobot.store.options]
cache = "shared"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Store.Backend != "sqlite" {
		t.Fatalf("Backend = %q, want sqlite", cfg.Store.Backend)
	}
	if want := ExpandPath("~/.factotum/kloobot.db"); cfg.Store.Options["path"] != want {
		t.Fatalf("path = %q, want %q", cfg.Store.Options["path"], want)
	}
	if cfg.Store.Options["cache"] != "shared" {
		t.Fatalf("cache = %q, want shared", cfg.Store.Options["cache"])
	}
}

func TestStorePathInterpolatesProject(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
default_project = "kloobot"

[store]
backend = "sqlite"

[store.options]
path = "~/.factotum/{project}.db"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := ExpandPath("~/.factotum/kloobot.db"); cfg.Store.Options["path"] != want {
		t.Fatalf("path = %q, want %q", cfg.Store.Options["path"], want)
	}
}

func TestProjectResolutionPrecedence(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `default_project = "b"`)
	project := writeConfig(t, dir, "project.toml", `project = "a"`)

	cfg, err := Load(Input{UserPath: user, ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "a" {
		t.Fatalf("Project = %q, want a (project file wins)", cfg.Project)
	}

	cfg, err = Load(Input{
		UserPath:    user,
		ProjectPath: project,
		Getenv: func(key string) string {
			if key == "FACTOTUM_PROJECT" {
				return "c"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "c" {
		t.Fatalf("Project = %q, want c (env wins)", cfg.Project)
	}
}

func TestEnvOverridesFiles(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
default_project = "kloobot"
default_actor = "khoi"

[store]
backend = "jsonfile"
`)
	cfg, err := Load(Input{
		UserPath:    user,
		ProjectPath: filepath.Join(dir, "none.toml"),
		Getenv: func(key string) string {
			switch key {
			case "FACTOTUM_STORE":
				return "sqlite"
			case "FACTOTUM_STORE_OPTS":
				return "path=/tmp/x.db, cache=shared"
			case "FACTOTUM_DEFAULT_ACTOR":
				return "codex"
			case "FACTOTUM_NO_HINTS":
				return "1"
			default:
				return ""
			}
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Store.Backend != "sqlite" || cfg.Store.Options["path"] != "/tmp/x.db" || cfg.Store.Options["cache"] != "shared" {
		t.Fatalf("Store = %+v, want env override", cfg.Store)
	}
	if cfg.DefaultActor != "codex" {
		t.Fatalf("DefaultActor = %q, want codex", cfg.DefaultActor)
	}
	if !cfg.NoHints {
		t.Fatal("NoHints = false, want true")
	}
}

func TestNoHintsFalseyEnv(t *testing.T) {
	for _, value := range []string{"", "0", "false", "no"} {
		cfg, err := Load(Input{
			Getenv: func(key string) string {
				if key == "FACTOTUM_NO_HINTS" {
					return value
				}
				return ""
			},
		})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.NoHints {
			t.Fatalf("FACTOTUM_NO_HINTS=%q set NoHints", value)
		}
	}
}

func TestNoHintsFromProjectFile(t *testing.T) {
	dir := t.TempDir()
	project := writeConfig(t, dir, "project.toml", "no_hints = true\n")
	cfg, err := Load(Input{ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.NoHints {
		t.Fatal("NoHints = false, want true")
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", "not = = toml")
	if _, err := Load(Input{UserPath: user, Getenv: emptyEnv}); err == nil {
		t.Fatal("Load() error = nil, want parse error")
	}
}

func TestUserPath(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"explicit", map[string]string{"FACTOTUM_USER_CONFIG": "/tmp/custom.toml"}, "/tmp/custom.toml"},
		{"home", map[string]string{"HOME": "/home/khoi"}, "/home/khoi/.factotum/config.toml"},
		{"unknown", map[string]string{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UserPath(func(key string) string { return tc.env[key] })
			if got != tc.want {
				t.Fatalf("UserPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if got, want := ExpandPath("~/.factotum/x.db"), filepath.Join(home, ".factotum", "x.db"); got != want {
		t.Fatalf("ExpandPath() = %q, want %q", got, want)
	}
	if got := ExpandPath("/abs/path"); got != "/abs/path" {
		t.Fatalf("ExpandPath() = %q, want unchanged", got)
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

func TestLoadReadsJudgeProviderAndOptions(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[judge]
provider = "typesafe"

[typesafe]
model = "jev-preview"
base_url = "https://staging.typesafe.ai"
secret_api_key = "sk-123"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Judge.Provider != "typesafe" {
		t.Fatalf("Provider = %q, want typesafe", cfg.Judge.Provider)
	}
	if cfg.Judge.Options["model"] != "jev-preview" {
		t.Fatalf("model = %q, want jev-preview", cfg.Judge.Options["model"])
	}
	if cfg.Judge.Options["secret_api_key"] != "sk-123" {
		t.Fatalf("secret_api_key = %q, want sk-123", cfg.Judge.Options["secret_api_key"])
	}
}

func TestJudgeProviderDefaultsToTypeSafe(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(Input{UserPath: filepath.Join(dir, "none.toml"), ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Judge.Provider != "typesafe" {
		t.Fatalf("Provider = %q, want the typesafe default", cfg.Judge.Provider)
	}
	if cfg.Judge.Options == nil {
		t.Fatal("Options = nil, want an empty map")
	}
}

func TestProviderOptionsAreGeneric(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[judge]
provider = "example"

[example]
endpoint = "https://example.test"
retries = 4
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Judge.Options["endpoint"] != "https://example.test" {
		t.Fatalf("endpoint = %q", cfg.Judge.Options["endpoint"])
	}
	if cfg.Judge.Options["retries"] != "4" {
		t.Fatalf("retries = %q, want 4", cfg.Judge.Options["retries"])
	}
}

func TestLoadReadsEmbedOptions(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[embed]
provider = "ollama"
endpoint = "http://localhost:11434"
model = "nomic-embed-text"
command = "llamafile --embedding"
timeout = "5s"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Embed.Provider != "ollama" {
		t.Fatalf("Embed.Provider = %q, want ollama", cfg.Embed.Provider)
	}
	if cfg.Embed.Options["endpoint"] != "http://localhost:11434" {
		t.Fatalf("endpoint = %q", cfg.Embed.Options["endpoint"])
	}
	if cfg.Embed.Options["model"] != "nomic-embed-text" {
		t.Fatalf("model = %q", cfg.Embed.Options["model"])
	}
	if cfg.Embed.Options["timeout"] != "5s" {
		t.Fatalf("timeout = %q", cfg.Embed.Options["timeout"])
	}
}

func TestEmbedDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(Input{UserPath: filepath.Join(dir, "none.toml"), ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Embed.Provider != "" {
		t.Fatalf("Embed.Provider = %q, want empty (disabled)", cfg.Embed.Provider)
	}
	if cfg.Embed.Options == nil {
		t.Fatal("Embed.Options = nil, want an empty map")
	}
}

func TestEmbedEnvOverridesFiles(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[embed]
provider = "ollama"
endpoint = "http://from-file"
model = "from-file"
`)
	cfg, err := Load(Input{
		UserPath:    user,
		ProjectPath: filepath.Join(dir, "none.toml"),
		Getenv: func(key string) string {
			switch key {
			case "FACTOTUM_EMBED_PROVIDER":
				return "openai"
			case "FACTOTUM_EMBED_ENDPOINT":
				return "http://from-env"
			case "FACTOTUM_EMBED_MODEL":
				return "from-env"
			case "FACTOTUM_EMBED_COMMAND":
				return "embed-cmd"
			default:
				return ""
			}
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Embed.Provider != "openai" {
		t.Fatalf("Embed.Provider = %q, want openai", cfg.Embed.Provider)
	}
	if cfg.Embed.Options["endpoint"] != "http://from-env" || cfg.Embed.Options["model"] != "from-env" || cfg.Embed.Options["command"] != "embed-cmd" {
		t.Fatalf("Embed.Options = %+v, want env overrides", cfg.Embed.Options)
	}
}

func TestProjectProviderOverridesUser(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", "[judge]\nprovider = \"user-provider\"\n")
	project := writeConfig(t, dir, "project.toml", "project = \"p\"\n[judge]\nprovider = \"project-provider\"\n")
	cfg, err := Load(Input{UserPath: user, ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Judge.Provider != "project-provider" {
		t.Fatalf("Provider = %q, want project-provider", cfg.Judge.Provider)
	}
}
