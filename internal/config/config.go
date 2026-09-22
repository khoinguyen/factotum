// Package config resolves Factotum configuration from two scopes:
//
//   - the machine-scoped user file (~/.factotum/config.toml): a registry of
//     projects plus machine-local paths (database, checkouts);
//   - the project-scoped file (./.factotum/config.toml): committable project
//     intent (which project this directory belongs to, actor, hints).
//
// Precedence is env > project file > user file > built-in defaults. The
// entrypoint may apply flag overrides on top of the resolved result.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	// DefaultPath is the project-scoped config path.
	DefaultPath = ".factotum/config.toml"

	// UserConfigEnv names the environment variable that overrides the
	// machine-scoped config path.
	UserConfigEnv = "FACTOTUM_USER_CONFIG"

	userDir      = ".factotum"
	userFileName = "config.toml"
)

// Config is the effective configuration after merging both scopes.
type Config struct {
	Project      string
	DefaultActor string
	NoHints      bool
	Store        Store
	// Judge selects the judge provider; TypeSafe is the default. Provider settings,
	// including the API key, live in the provider's own config table.
	Judge Judge
	// Embed selects the optional embedding provider for vector memory recall. An
	// empty provider means Disabled: retrieval stays lexical. Options are generic so
	// a new provider needs no new field here.
	Embed Embed
	// Agent selects the provider that breaks a free-form prompt into tasks. The
	// default (command) runs a configured agent CLI; with no command it is
	// Disabled, so `ft prompt` reports that no agent is configured. Options are
	// generic so a new provider needs no new field here.
	Agent Agent
}

// Agent configures the inference provider behind `ft prompt`. Agent is the
// capability; a provider such as command is one implementation, and its settings
// live under the [agent] table.
type Agent struct {
	Provider string
	Options  map[string]string
}

// Embed configures the optional embedding provider. It is the capability; a
// provider such as ollama is one implementation, and its settings live under the
// [embed] table. An empty Provider disables vector recall.
type Embed struct {
	Provider string
	Options  map[string]string
}

// Judge selects the model-backed judge. Judge is the capability; a provider such as
// TypeSafe is one implementation, and its settings live under a top-level table named
// after the provider. Options is generic so a new provider needs no new field here.
type Judge struct {
	Provider string
	Options  map[string]string
}

type Store struct {
	Backend string
	Options map[string]string
}

// Input names the two config files to merge; either may be empty or missing.
type Input struct {
	UserPath    string
	ProjectPath string
	Getenv      func(string) string
}

type fileStore struct {
	Backend string            `toml:"backend"`
	Options map[string]string `toml:"options"`
}

type judgeFile struct {
	Provider string `toml:"provider"`
}

type projectEntry struct {
	DBPath       string    `toml:"db_path"`
	DefaultActor string    `toml:"default_actor"`
	NoHints      *bool     `toml:"no_hints"`
	Store        fileStore `toml:"store"`
}

type userFile struct {
	DefaultProject string                  `toml:"default_project"`
	DefaultActor   string                  `toml:"default_actor"`
	NoHints        *bool                   `toml:"no_hints"`
	Store          fileStore               `toml:"store"`
	Judge          judgeFile               `toml:"judge"`
	Projects       map[string]projectEntry `toml:"projects"`
}

type projectFile struct {
	Project      string    `toml:"project"`
	DefaultActor string    `toml:"default_actor"`
	NoHints      *bool     `toml:"no_hints"`
	Store        fileStore `toml:"store"`
	Judge        judgeFile `toml:"judge"`
}

// Default returns the built-in configuration. TypeSafe is the default judge provider.
func Default() Config {
	return Config{
		Store: Store{
			Backend: "memory",
			Options: map[string]string{},
		},
		Judge: Judge{Provider: "typesafe", Options: map[string]string{}},
		Embed: Embed{Options: map[string]string{}},
		Agent: Agent{Provider: "command", Options: map[string]string{}},
	}
}

// Load merges the machine-scoped and project-scoped files, then applies the
// environment. Missing files are not an error.
func Load(in Input) (Config, error) {
	getenv := in.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	var user userFile
	if err := decode(in.UserPath, &user); err != nil {
		return Config{}, err
	}
	var project projectFile
	if err := decode(in.ProjectPath, &project); err != nil {
		return Config{}, err
	}

	cfg := Default()
	cfg.Project = resolveProject(getenv, project.Project, user.DefaultProject)
	entry := user.Projects[cfg.Project]
	cfg.Store = resolveStore(cfg.Project, project.Store, entry, user.Store)
	cfg.Judge.Provider = firstNonEmpty(project.Judge.Provider, user.Judge.Provider, Default().Judge.Provider)
	// Provider settings are machine-scoped: they are read from the user file only.
	// The environment still overrides them at the call site.
	options, err := providerOptions(in.UserPath, cfg.Judge.Provider)
	if err != nil {
		return Config{}, err
	}
	cfg.Judge.Options = options
	embedOptions, err := providerOptions(in.UserPath, "embed")
	if err != nil {
		return Config{}, err
	}
	cfg.Embed.Options = embedOptions
	cfg.Embed.Provider = strings.TrimSpace(embedOptions["provider"])
	applyEmbedEnv(&cfg.Embed, getenv)
	agentOptions, err := providerOptions(in.UserPath, "agent")
	if err != nil {
		return Config{}, err
	}
	cfg.Agent.Options = agentOptions
	cfg.Agent.Provider = firstNonEmpty(strings.TrimSpace(agentOptions["provider"]), Default().Agent.Provider)
	applyAgentEnv(&cfg.Agent, getenv)
	cfg.DefaultActor = firstNonEmpty(project.DefaultActor, entry.DefaultActor, user.DefaultActor)
	cfg.NoHints = boolAt(user.NoHints, false)
	cfg.NoHints = boolAt(entry.NoHints, cfg.NoHints)
	cfg.NoHints = boolAt(project.NoHints, cfg.NoHints)

	if backend := getenv("FACTOTUM_STORE"); backend != "" {
		cfg.Store.Backend = backend
	}
	applyStoreOptions(&cfg.Store, getenv("FACTOTUM_STORE_OPTS"))
	if actor := getenv("FACTOTUM_DEFAULT_ACTOR"); actor != "" {
		cfg.DefaultActor = actor
	}
	if truthy(getenv("FACTOTUM_NO_HINTS")) {
		cfg.NoHints = true
	}
	return cfg, nil
}

// UserPath returns the machine-scoped config path: $FACTOTUM_USER_CONFIG when
// set, else $HOME/.factotum/config.toml. It returns "" when neither is known.
func UserPath(getenv func(string) string) string {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if path := getenv(UserConfigEnv); path != "" {
		return path
	}
	home := getenv("HOME")
	if home == "" {
		return ""
	}
	return filepath.Join(home, userDir, userFileName)
}

// ExpandPath expands a leading ~ into the user's home directory.
func ExpandPath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Store.Backend) == "" {
		return fmt.Errorf("store backend is required")
	}
	return nil
}

func resolveProject(getenv func(string) string, projectScoped, userDefault string) string {
	if value := getenv("FACTOTUM_PROJECT"); value != "" {
		return value
	}
	return firstNonEmpty(projectScoped, userDefault)
}

func resolveStore(project string, projectStore fileStore, entry projectEntry, userStore fileStore) Store {
	switch {
	case projectStore.Backend != "":
		return buildStore(projectStore, project)
	case entry.DBPath != "" || entry.Store.Backend != "":
		store := Store{Backend: entry.Store.Backend, Options: map[string]string{}}
		if store.Backend == "" {
			store.Backend = "sqlite"
		}
		for key, value := range entry.Store.Options {
			store.Options[key] = expand(value, project)
		}
		if entry.DBPath != "" {
			store.Options["path"] = expand(entry.DBPath, project)
		}
		return store
	case userStore.Backend != "":
		return buildStore(userStore, project)
	default:
		return Default().Store
	}
}

func buildStore(store fileStore, project string) Store {
	options := make(map[string]string, len(store.Options))
	for key, value := range store.Options {
		options[key] = expand(value, project)
	}
	return Store{Backend: store.Backend, Options: options}
}

// expand interpolates {project} and expands a leading ~.
func expand(value, project string) string {
	if project != "" {
		value = strings.ReplaceAll(value, "{project}", project)
	}
	return ExpandPath(value)
}

func decode(path string, target any) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}

// applyEmbedEnv overlays the FACTOTUM_EMBED_* environment variables on the [embed]
// table, so an embedding provider can be selected without editing a config file.
func applyEmbedEnv(embed *Embed, getenv func(string) string) {
	if embed.Options == nil {
		embed.Options = map[string]string{}
	}
	if provider := getenv("FACTOTUM_EMBED_PROVIDER"); provider != "" {
		embed.Provider = provider
	}
	overrides := map[string]string{
		"FACTOTUM_EMBED_ENDPOINT": "endpoint",
		"FACTOTUM_EMBED_MODEL":    "model",
		"FACTOTUM_EMBED_COMMAND":  "command",
	}
	for env, key := range overrides {
		if value := getenv(env); value != "" {
			embed.Options[key] = value
		}
	}
}

// applyAgentEnv overlays the FACTOTUM_AGENT_* environment variables on the [agent]
// table, so an agent CLI can be selected without editing a config file.
func applyAgentEnv(agent *Agent, getenv func(string) string) {
	if agent.Options == nil {
		agent.Options = map[string]string{}
	}
	if provider := getenv("FACTOTUM_AGENT_PROVIDER"); provider != "" {
		agent.Provider = provider
	}
	if command := getenv("FACTOTUM_AGENT_COMMAND"); command != "" {
		agent.Options["command"] = command
	}
}

func applyStoreOptions(store *Store, raw string) {
	if raw == "" {
		return
	}
	if store.Options == nil {
		store.Options = map[string]string{}
	}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		store.Options[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
}

// providerOptions reads the top-level table named after the provider (for example
// [typesafe]) as a string map. The table is generic, so a new provider is a new table
// and needs no change here.
func providerOptions(path, provider string) (map[string]string, error) {
	options := map[string]string{}
	if path == "" || provider == "" {
		return options, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return options, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	table, ok := raw[provider].(map[string]any)
	if !ok {
		return options, nil
	}
	for key, value := range table {
		switch typed := value.(type) {
		case string:
			options[key] = typed
		case int64:
			options[key] = strconv.FormatInt(typed, 10)
		case float64:
			options[key] = strconv.FormatFloat(typed, 'g', -1, 64)
		case bool:
			options[key] = strconv.FormatBool(typed)
		}
	}
	return options, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func boolAt(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

// truthy reports whether an environment value means "on".
func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
