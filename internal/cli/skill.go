package cli

import (
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/skills"
)

type skillEntry struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
}

type skillDoc struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Body        string `json:"body" yaml:"body"`
}

// renderSkillDoc writes doc exactly as `ft skill get` prints it: the raw body
// for text output (with the trailing newline when the body lacks one), and the
// whole document for json/yaml. The output-bound guard renders through this
// function, so a skill the guard says fits is one a caller receives whole in
// every format - json escapes the body and yaml indents it, so each shape has
// its own size.
func renderSkillDoc(w io.Writer, format string, doc skillDoc) error {
	switch format {
	case "json":
		return writeJSON(w, doc)
	case "yaml":
		return writeYAML(w, doc)
	}
	if _, err := io.WriteString(w, doc.Body); err != nil {
		return err
	}
	if strings.HasSuffix(doc.Body, "\n") {
		return nil
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func newSkillCommand(deps *Deps) *cobra.Command {
	// Skills are embedded content; reading them never touches the caller's
	// store, so `ft skill` must not open (and thus migrate) one.
	cmd := &cobra.Command{Use: "skill", Short: "Print the usage skills embedded in ft", Annotations: map[string]string{annotationNoCallerStore: "true"}}

	list := &cobra.Command{
		Use:   "list",
		Short: "List embedded skills",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := skills.All()
			if err != nil {
				return err
			}
			entries := make([]skillEntry, 0, len(all))
			for _, skill := range all {
				entries = append(entries, skillEntry{Name: skill.Name, Description: skill.Description})
			}
			return deps.emit(entries, func() {
				rows := make([][]string, 0, len(entries))
				for _, entry := range entries {
					rows = append(rows, []string{entry.Name, entry.Description})
				}
				deps.printTable([]string{"NAME", "DESCRIPTION"}, rows)
			})
		},
	}

	get := &cobra.Command{
		Use:   "get [name]",
		Short: "Print a skill as raw markdown",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageError(cmd, "expected at most one skill name")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := skills.DefaultName
			if len(args) == 1 {
				name = args[0]
			}
			skill, err := skills.Get(name)
			if err != nil {
				return err
			}
			doc := skillDoc{Name: skill.Name, Description: skill.Description, Body: skill.Body}
			return renderSkillDoc(deps.Out, deps.OutputFormat, doc)
		},
	}

	cmd.AddCommand(list, get, newSkillLintCommand(deps))
	return requireSubcommand(cmd)
}
