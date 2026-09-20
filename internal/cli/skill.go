package cli

import (
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

func newSkillCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "skill", Short: "Print the usage skills embedded in ft"}

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
			return deps.emit(doc, func() {
				deps.printf("%s", skill.Body)
				if !strings.HasSuffix(skill.Body, "\n") {
					deps.printf("\n")
				}
			})
		},
	}

	cmd.AddCommand(list, get)
	return cmd
}
