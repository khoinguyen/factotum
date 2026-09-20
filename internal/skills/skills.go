// Package skills holds the read-only usage skills embedded in the ft binary.
// Each skill is a markdown document an agent can print with `ft skill get`.
package skills

import (
	"embed"
	"fmt"
	"sort"

	"github.com/khoinguyen/factotum/pkg/core"
)

// DefaultName is the skill printed when `ft skill get` is called with no name.
const DefaultName = "ft"

//go:embed content/*.md
var content embed.FS

// Skill is one embedded usage document.
type Skill struct {
	Name        string
	Description string
	Body        string
}

// catalog is the manifest of embedded skills; the body is content/<name>.md.
var catalog = []struct {
	name        string
	description string
}{
	{DefaultName, "How to use ft: lifecycle, core commands, and conventions"},
}

// All returns every skill, sorted by name.
func All() ([]Skill, error) {
	out := make([]Skill, 0, len(catalog))
	for _, entry := range catalog {
		skill, err := load(entry.name, entry.description)
		if err != nil {
			return nil, err
		}
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the named skill, or core.ErrNotFound when it does not exist.
func Get(name string) (Skill, error) {
	for _, entry := range catalog {
		if entry.name == name {
			return load(entry.name, entry.description)
		}
	}
	return Skill{}, fmt.Errorf("%w: skill %q", core.ErrNotFound, name)
}

func load(name, description string) (Skill, error) {
	data, err := content.ReadFile("content/" + name + ".md")
	if err != nil {
		return Skill{}, fmt.Errorf("read skill %q: %w", name, err)
	}
	return Skill{Name: name, Description: description, Body: string(data)}, nil
}
