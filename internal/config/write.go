package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// machineConfigTemplate is the initial content of a new machine-scoped config.
// It carries no default_project or [projects] entry: those are added when a
// project is registered.
const machineConfigTemplate = `# Machine-scoped Factotum config.
#
# Project databases and machine-local paths live here; project intent lives in
# each repository's .factotum/config.toml. Precedence is
# env > project file > this file > defaults.
`

// projectConfigTemplate is the initial content of a new project-scoped config.
const projectConfigTemplate = `# Project-scoped Factotum config (committed).
#
# Pins this repository to a project registered in ~/.factotum/config.toml.
# Machine-local paths (database, checkouts) do NOT belong here.

`

// EnsureMachineConfig creates the machine-scoped config at path when it does not
// exist, creating parent directories as needed. An existing file is left
// untouched. It reports whether it wrote a new file.
func EnsureMachineConfig(path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, errors.New("machine config path is empty")
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat config %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(machineConfigTemplate), 0o600); err != nil {
		return false, fmt.Errorf("write config %s: %w", path, err)
	}
	return true, nil
}

// AddProjectEntry adds a [projects.<id>] table with db_path to the machine
// config, and sets default_project when the file does not already set one. It
// preserves the rest of the file, so hand-written comments and other tables
// survive. It is idempotent: an existing entry for id is left untouched and
// created is false.
func AddProjectEntry(path, id, dbPath string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, errors.New("machine config path is empty")
	}
	if strings.TrimSpace(id) == "" {
		return false, errors.New("project id is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read config %s: %w", path, err)
	}
	var existing userFile
	if err := toml.Unmarshal(data, &existing); err != nil {
		return false, fmt.Errorf("parse config %s: %w", path, err)
	}
	if _, ok := existing.Projects[id]; ok {
		return false, nil
	}

	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if existing.DefaultProject == "" {
		text = insertTopLevel(text, fmt.Sprintf("default_project = %q", id))
	}
	text += fmt.Sprintf("\n[projects.%s]\ndb_path = %q\n", tomlKey(id), dbPath)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return false, fmt.Errorf("write config %s: %w", path, err)
	}
	return true, nil
}

// WriteProjectConfig writes project = "<id>" to the project-scoped config,
// creating the file when missing. An existing file is never rewritten: it is a
// no-op when it already pins id, an error when it pins a different project, and
// a non-destructive prepend when it sets no project. It reports whether it wrote.
func WriteProjectConfig(path, id string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, errors.New("project config path is empty")
	}
	if strings.TrimSpace(id) == "" {
		return false, errors.New("project id is required")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, fmt.Errorf("create config dir: %w", err)
		}
		body := projectConfigTemplate + fmt.Sprintf("project = %q\n", id)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return false, fmt.Errorf("write config %s: %w", path, err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read config %s: %w", path, err)
	}
	var existing projectFile
	if err := toml.Unmarshal(data, &existing); err != nil {
		return false, fmt.Errorf("parse config %s: %w", path, err)
	}
	switch {
	case existing.Project == id:
		return false, nil
	case existing.Project != "":
		return false, fmt.Errorf("project config %s already pins project %q", path, existing.Project)
	}
	text := fmt.Sprintf("project = %q\n", id) + string(data)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return false, fmt.Errorf("write config %s: %w", path, err)
	}
	return true, nil
}

// insertTopLevel inserts a top-level key line after any leading comment/blank
// block, so a file's header comment stays at the top and the key does not land
// inside a table.
func insertTopLevel(text, line string) string {
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}
		break
	}
	prefix := strings.Join(lines[:i], "\n")
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	return prefix + line + "\n" + strings.Join(lines[i:], "\n")
}

// tomlKey renders an id as a TOML table key: bare when it is a safe identifier,
// quoted otherwise, so an unusual id cannot produce invalid TOML.
func tomlKey(id string) string {
	if id == "" {
		return `""`
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return fmt.Sprintf("%q", id)
		}
	}
	return id
}
