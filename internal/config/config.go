// Package config loads Factotum configuration with the precedence
// flags > environment > file > defaults.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

const DefaultPath = ".factotum/config.toml"

type Config struct {
	Store        Store  `toml:"store"`
	DefaultActor string `toml:"default_actor"`
}

type Store struct {
	Backend string            `toml:"backend"`
	Options map[string]string `toml:"options"`
}

func Default() Config {
	return Config{
		Store: Store{
			Backend: "memory",
			Options: map[string]string{},
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultPath
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) ApplyEnv(getenv func(string) string) {
	if value := getenv("FACTOTUM_STORE"); value != "" {
		c.Store.Backend = value
	}
	if value := getenv("FACTOTUM_DEFAULT_ACTOR"); value != "" {
		c.DefaultActor = value
	}
	if raw := getenv("FACTOTUM_STORE_OPTS"); raw != "" {
		if c.Store.Options == nil {
			c.Store.Options = map[string]string{}
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
			c.Store.Options[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Store.Backend) == "" {
		return fmt.Errorf("store backend is required")
	}
	return nil
}
