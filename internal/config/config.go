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
	Projects       map[string]projectEntry `toml:"projects"`
}

type projectFile struct {
	Project      string    `toml:"project"`
	DefaultActor string    `toml:"default_actor"`
	NoHints      *bool     `toml:"no_hints"`
	Store        fileStore `toml:"store"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Store: Store{
			Backend: "memory",
			Options: map[string]string{},
		},
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
