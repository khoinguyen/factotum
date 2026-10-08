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

func TestLoadReadsTechStack(t *testing.T) {
	dir := t.TempDir()
	project := writeConfig(t, dir, "project.toml", "project = \"acme\"\ntech_stack = \"go\"\n")
	cfg, err := Load(Input{ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TechStack != "go" {
		t.Fatalf("TechStack = %q, want go", cfg.TechStack)
	}
}

func TestLoadTechStackEmptyWhenUnset(t *testing.T) {
	dir := t.TempDir()
	project := writeConfig(t, dir, "project.toml", "project = \"acme\"\n")
	cfg, err := Load(Input{ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TechStack != "" {
		t.Fatalf("TechStack = %q, want empty", cfg.TechStack)
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

func TestProjectRoot(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		path string
		want string
	}{
		{"conventional", filepath.Join(root, ".factotum", "config.toml"), root},
		{"custom name", filepath.Join(root, "project.toml"), root},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProjectRoot(tc.path); got != tc.want {
				t.Fatalf("ProjectRoot(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestProjectRootMakesRelativeAbsolute(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ProjectRoot(".factotum/config.toml"), cwd; got != want {
		t.Fatalf("ProjectRoot() = %q, want the absolute cwd %q", got, want)
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

func TestAgentDefaultsToCommandProvider(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(Input{
		UserPath:    filepath.Join(dir, "user.toml"),
		ProjectPath: filepath.Join(dir, "project.toml"),
		Getenv:      emptyEnv,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Agent.Provider != "command" {
		t.Fatalf("Agent.Provider = %q, want command", cfg.Agent.Provider)
	}
	if cfg.Agent.Options == nil {
		t.Fatal("Agent.Options = nil, want an empty map")
	}
}

func TestAgentOptionsFromFile(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[agent]
provider = "command"
command = "claude -p"
timeout = "30s"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Agent.Provider != "command" {
		t.Fatalf("Agent.Provider = %q, want command", cfg.Agent.Provider)
	}
	if cfg.Agent.Options["command"] != "claude -p" || cfg.Agent.Options["timeout"] != "30s" {
		t.Fatalf("Agent.Options = %+v, want the file command and timeout", cfg.Agent.Options)
	}
}

func TestLoadReadsRunOptions(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
default_project = "acme"

[run]
sandbox = "local"
harness = "opencode"
workspace = "~/ws/{project}"
 model = "openrouter/x"
 allow_host = true
 refresh = true
 args = ["--auto"]
 provider = "openrouter"
 credential_env = "OPENROUTER_API_KEY"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "local" || cfg.Run.Harness != "opencode" {
		t.Fatalf("Run = %+v, want local/opencode", cfg.Run)
	}
	if !cfg.Run.Refresh {
		t.Fatal("Run.Refresh = false, want true")
	}
	if want := ExpandPath("~/ws/acme"); cfg.Run.Workspace != want {
		t.Fatalf("Run.Workspace = %q, want %q", cfg.Run.Workspace, want)
	}
	if cfg.Run.Model != "openrouter/x" {
		t.Fatalf("Run.Model = %q, want openrouter/x", cfg.Run.Model)
	}
	if !cfg.Run.AllowHost {
		t.Fatal("Run.AllowHost = false, want true")
	}
	if len(cfg.Run.Args) != 1 || cfg.Run.Args[0] != "--auto" {
		t.Fatalf("Run.Args = %v, want [--auto]", cfg.Run.Args)
	}
	if cfg.Run.Provider != "openrouter" {
		t.Fatalf("Run.Provider = %q, want openrouter", cfg.Run.Provider)
	}
	if cfg.Run.CredentialEnvVar != "OPENROUTER_API_KEY" {
		t.Fatalf("Run.CredentialEnvVar = %q, want OPENROUTER_API_KEY", cfg.Run.CredentialEnvVar)
	}
}

func TestRunEnvOverridesFiles(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
backend = "local"
harness = "opencode"
`)
	cfg, err := Load(Input{
		UserPath:    user,
		ProjectPath: filepath.Join(dir, "none.toml"),
		Getenv: func(key string) string {
			switch key {
			case "FACTOTUM_RUN_SANDBOX":
				return "openshell"
			case "FACTOTUM_RUN_HARNESS":
				return "pi"
			case "FACTOTUM_RUN_MODEL":
				return "openrouter/y"
			case "FACTOTUM_RUN_ALLOW_HOST":
				return "1"
			case "FACTOTUM_RUN_REFRESH":
				return "1"
			case "FACTOTUM_RUN_PROVIDER":
				return "openrouter"
			case "FACTOTUM_RUN_CREDENTIAL_ENV":
				return "OPENROUTER_API_KEY"
			default:
				return ""
			}
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "openshell" || cfg.Run.Harness != "pi" || cfg.Run.Model != "openrouter/y" || !cfg.Run.AllowHost || !cfg.Run.Refresh {
		t.Fatalf("Run = %+v, want env overrides", cfg.Run)
	}
	if cfg.Run.Provider != "openrouter" || cfg.Run.CredentialEnvVar != "OPENROUTER_API_KEY" {
		t.Fatalf("Run = %+v, want provider/credential env overrides", cfg.Run)
	}
}

func TestRunAllowHostIgnoredFromProjectFile(t *testing.T) {
	dir := t.TempDir()
	project := writeConfig(t, dir, "project.toml", `
project = "acme"

[run]
sandbox = "local"
allow_host = true
workspace = "/host/path/{project}"
`)
	cfg, err := Load(Input{UserPath: filepath.Join(dir, "none.toml"), ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "local" {
		t.Fatalf("Run.Sandbox = %q, want local (project may select the sandbox)", cfg.Run.Sandbox)
	}
	if cfg.Run.AllowHost {
		t.Fatal("AllowHost from the committed project file must be ignored")
	}
	if cfg.Run.Workspace != "" {
		t.Fatalf("Run.Workspace = %q, want empty (machine-scoped field must be ignored)", cfg.Run.Workspace)
	}
}

func TestAgentEnvOverridesFiles(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[agent]
provider = "command"
command = "from-file"
`)
	cfg, err := Load(Input{
		UserPath:    user,
		ProjectPath: filepath.Join(dir, "none.toml"),
		Getenv: func(key string) string {
			switch key {
			case "FACTOTUM_AGENT_PROVIDER":
				return "custom"
			case "FACTOTUM_AGENT_COMMAND":
				return "from-env"
			default:
				return ""
			}
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Agent.Provider != "custom" {
		t.Fatalf("Agent.Provider = %q, want custom", cfg.Agent.Provider)
	}
	if cfg.Agent.Options["command"] != "from-env" {
		t.Fatalf("Agent.Options[command] = %q, want from-env", cfg.Agent.Options["command"])
	}
}

// TestServeTokenIsMachineScoped proves the shared capture token is read from the
// user (machine-scoped) config: it is a secret, so a committed project file must
// never carry it.
func TestServeTokenIsMachineScoped(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[serve]
token = "s3cret"
`)
	project := writeConfig(t, dir, "project.toml", `
project = "factotum"

[serve]
token = "committed-should-be-ignored"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Serve.Token != "s3cret" {
		t.Fatalf("Serve.Token = %q, want s3cret from the user file", cfg.Serve.Token)
	}
}

// TestServeURLIsMachineScoped proves the messaging hub URL is read from the
// machine-scoped [serve] table, like the token, so a host names the hub its
// launched receivers reach. The environment supplies it for a one-off run.
func TestServeURLIsMachineScoped(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[serve]
url = "http://hub:8484"
`)
	project := writeConfig(t, dir, "project.toml", `
project = "factotum"

[serve]
url = "http://committed-should-be-ignored"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Serve.URL != "http://hub:8484" {
		t.Fatalf("Serve.URL = %q, want the user file's hub URL", cfg.Serve.URL)
	}
}

func TestServeURLEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[serve]
url = "http://from-file"
`)
	cfg, err := Load(Input{
		UserPath:    user,
		ProjectPath: filepath.Join(dir, "none.toml"),
		Getenv: func(key string) string {
			if key == "FACTOTUM_MSG_URL" {
				return "http://from-env"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Serve.URL != "http://from-env" {
		t.Fatalf("Serve.URL = %q, want from-env", cfg.Serve.URL)
	}
}

func TestServeTokenEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[serve]
token = "from-file"
`)
	cfg, err := Load(Input{
		UserPath:    user,
		ProjectPath: filepath.Join(dir, "none.toml"),
		Getenv: func(key string) string {
			if key == "FACTOTUM_SERVE_TOKEN" {
				return "from-env"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Serve.Token != "from-env" {
		t.Fatalf("Serve.Token = %q, want from-env", cfg.Serve.Token)
	}
}
