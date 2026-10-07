package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// RunDefaults are the harness and sandbox a run resolves when neither a flag nor
// config supplies them. An empty field is left unwritten.
type RunDefaults struct {
	Sandbox string
	Harness string
}

// WriteRunDefaults persists the resolved harness and sandbox into the [run]
// table of the config at path, creating the file when missing. project selects
// the scope: a project config pins projectID (the committable subset only),
// while a machine config is created from its template. The rest of the file -
// comments, tables, and existing keys - is preserved.
func WriteRunDefaults(path string, project bool, projectID string, defaults RunDefaults) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is empty")
	}
	mode := os.FileMode(0o600)
	if project {
		mode = 0o644
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
		switch {
		case project && projectID != "":
			data = []byte(projectConfigTemplate + fmt.Sprintf("project = %q\n", projectID))
		case project:
			data = []byte(projectConfigTemplate)
		default:
			data = []byte(machineConfigTemplate)
		}
	} else if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}

	keys := map[string]string{}
	if defaults.Sandbox != "" {
		keys["sandbox"] = defaults.Sandbox
	}
	if defaults.Harness != "" {
		keys["harness"] = defaults.Harness
	}
	text := upsertRunKeys(string(data), keys)
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// upsertRunKeys sets key = "value" entries inside the [run] table of a TOML
// document, creating the table at the end when absent. It preserves every other
// line, so hand-written comments and tables survive. Missing keys are inserted
// in sorted order so the write is deterministic.
func upsertRunKeys(text string, keys map[string]string) string {
	if len(keys) == 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		if isRunHeader(line) {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(strings.TrimSpace(line), "[") {
			end = i
			break
		}
	}
	if start < 0 {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		var b strings.Builder
		b.WriteString(text)
		b.WriteString("\n[run]\n")
		for _, key := range sortedKeys(keys) {
			fmt.Fprintf(&b, "%s = %q\n", key, keys[key])
		}
		return b.String()
	}

	present := map[string]bool{}
	for i := start + 1; i < end; i++ {
		key, ok := keyName(lines[i])
		if !ok {
			continue
		}
		if value, ok := keys[key]; ok {
			lines[i] = fmt.Sprintf("%s = %q", key, value)
			present[key] = true
		}
	}
	var missing []string
	for key := range keys {
		if !present[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) == 0 {
		return strings.Join(lines, "\n")
	}
	add := make([]string, 0, len(missing))
	for _, key := range missing {
		add = append(add, fmt.Sprintf("%s = %q", key, keys[key]))
	}
	lines = append(lines[:end], append(add, lines[end:]...)...)
	return strings.Join(lines, "\n")
}

// isRunHeader reports whether a line is the [run] table header, tolerating the
// TOML variants that name the same table: an inline comment ('[run] # note') or
// inner whitespace ('[ run ]'). A dotted child table ('[run.x]') or an array of
// tables ('[[run]]') is not the header and returns false.
func isRunHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	if i := strings.IndexByte(trimmed, '#'); i >= 0 {
		trimmed = strings.TrimSpace(trimmed[:i])
	}
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return false
	}
	return strings.TrimSpace(trimmed[1:len(trimmed)-1]) == "run"
}

// keyName returns the bare key of a simple `key = value` TOML line, ignoring
// comments, blank lines, and table headers. ok is false for anything else.
func keyName(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	key, _, found := strings.Cut(trimmed, "=")
	if !found {
		return "", false
	}
	return strings.TrimSpace(key), true
}

func sortedKeys(keys map[string]string) []string {
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
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
