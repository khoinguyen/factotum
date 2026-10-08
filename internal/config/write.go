package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// WriteProjectTechStack records the project's tech-stack choice in the
// project-scoped config at path, creating the file when missing. It is
// idempotent when the same stack is already recorded and replaces a differing
// one in place, preserving the rest of the file. It reports whether it wrote.
func WriteProjectTechStack(path, stack string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, errors.New("project config path is empty")
	}
	if strings.TrimSpace(stack) == "" {
		return false, errors.New("tech stack is required")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, fmt.Errorf("create config dir: %w", err)
		}
		body := projectConfigTemplate + fmt.Sprintf("tech_stack = %q\n", stack)
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
	if existing.TechStack == stack {
		return false, nil
	}
	text := setTopLevelKey(string(data), "tech_stack", strconv.Quote(stack))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return false, fmt.Errorf("write config %s: %w", path, err)
	}
	return true, nil
}

// setTopLevelKey sets a simple `key = value` (value already a TOML token) in the
// leading top-level region of a document, before any table header. It replaces
// an existing line or inserts one after the header comment, so it never lands
// inside a table and never duplicates the key. It tracks array nesting, so a
// hand-edited multi-line array before the key is not mistaken for a table
// header.
func setTopLevelKey(text, key, value string) string {
	lines := strings.Split(text, "\n")
	depth := 0
	for i, line := range lines {
		if depth == 0 {
			if strings.HasPrefix(strings.TrimSpace(line), "[") {
				break
			}
			if name, ok := keyName(line); ok && name == key {
				lines[i] = fmt.Sprintf("%s = %s", key, value)
				return strings.Join(lines, "\n")
			}
		}
		depth = arrayDepth(line, depth)
	}
	return insertTopLevel(text, fmt.Sprintf("%s = %s", key, value))
}

// arrayDepth returns the array-nesting depth after line, starting from depth. It
// counts `[` and `]` outside quoted strings and comments, so a bracket in a
// quoted value or an inline comment does not change the depth. Only simple
// single-line strings are recognized; a multi-line string containing brackets is
// out of scope.
func arrayDepth(line string, depth int) int {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if quote == '"' && c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '#':
			return depth
		case c == '"' || c == '\'':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return depth
}

// RunDefaults are the harness and sandbox a run resolves when neither a flag nor
// config supplies them, plus the host-scoped allow_host opt-in. An empty field
// (and a false AllowHost) is left unwritten.
type RunDefaults struct {
	Sandbox   string
	Harness   string
	AllowHost bool
}

// WriteRunDefaults persists the resolved harness and sandbox into the [run]
// table of the config at path, creating the file when missing. project selects
// the scope: a project config pins projectID (the committable subset only),
// while a machine config is created from its template. allow_host is
// host-scoped, so a project-scoped write rejects it rather than committing an
// opt-in. The rest of the file - comments, tables, and existing keys - is
// preserved.
func WriteRunDefaults(path string, project bool, projectID string, defaults RunDefaults) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is empty")
	}
	if project && defaults.AllowHost {
		return errors.New("allow_host is host-scoped and cannot be written to a project config")
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
		keys["sandbox"] = strconv.Quote(defaults.Sandbox)
	}
	if defaults.Harness != "" {
		keys["harness"] = strconv.Quote(defaults.Harness)
	}
	if defaults.AllowHost {
		keys["allow_host"] = "true"
	}
	text := upsertRunKeys(string(data), keys)
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// upsertRunKeys sets `key = value` entries inside the [run] table of a TOML
// document, creating the table at the end when absent. Each value is a literal
// TOML token (a quoted string or a bare bool), so the caller controls the type.
// It preserves every other line, so hand-written comments and tables survive.
// Missing keys are inserted in sorted order so the write is deterministic.
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
			fmt.Fprintf(&b, "%s = %s\n", key, keys[key])
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
			lines[i] = fmt.Sprintf("%s = %s", key, value)
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
		add = append(add, fmt.Sprintf("%s = %s", key, keys[key]))
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
