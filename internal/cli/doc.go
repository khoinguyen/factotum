package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

func newDocCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "doc", Short: "Manage specs, docs, and memory"}

	var projectID, kind, title, body, path, taskID string
	add := &cobra.Command{
		Use:   "add",
		Short: "Add an artifact (spec, doc, or memory)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			content := body
			if path != "" {
				data, err := os.ReadFile(path)
				if err != nil {
					return fmt.Errorf("read %s: %w", path, err)
				}
				content = string(data)
			}
			input := app.ArtifactInput{
				ProjectID: core.ProjectID(projectID),
				Kind:      core.ArtifactKind(kind),
				Title:     title,
				Body:      content,
				Path:      path,
			}
			if taskID != "" {
				id := core.TaskID(taskID)
				input.TaskID = &id
			}
			artifact, err := deps.Artifacts.Add(cmd.Context(), input)
			if err != nil {
				return err
			}
			return deps.emit(artifact, func() { deps.printf("%s\t%s\t%s\n", artifact.ID, artifact.Kind, artifact.Title) })
		},
	}
	add.Flags().StringVar(&projectID, "project", "", "project id (required)")
	add.Flags().StringVar(&kind, "kind", string(core.ArtifactDoc), "artifact kind: spec, doc, or memory")
	add.Flags().StringVar(&title, "title", "", "artifact title (required)")
	add.Flags().StringVar(&body, "body", "", "inline content")
	add.Flags().StringVar(&path, "file", "", "read content from a file")
	add.Flags().StringVar(&taskID, "task", "", "attach to a task")
	_ = add.MarkFlagRequired("project")
	_ = add.MarkFlagRequired("title")

	var listProject, listKind string
	list := &cobra.Command{
		Use:   "list",
		Short: "List artifacts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter := store.ArtifactFilter{ProjectID: core.ProjectID(listProject)}
			if listKind != "" {
				kind := core.ArtifactKind(listKind)
				filter.Kind = &kind
			}
			artifacts, err := deps.Artifacts.List(cmd.Context(), filter)
			if err != nil {
				return err
			}
			return deps.emit(artifacts, func() {
				rows := make([][]string, 0, len(artifacts))
				for _, artifact := range artifacts {
					rows = append(rows, []string{string(artifact.ID), string(artifact.Kind), artifact.Title})
				}
				deps.printTable([]string{"ID", "KIND", "TITLE"}, rows)
			})
		},
	}
	list.Flags().StringVar(&listProject, "project", "", "filter by project id")
	list.Flags().StringVar(&listKind, "kind", "", "filter by kind")

	var searchProject string
	search := &cobra.Command{
		Use:   "search <query>",
		Short: "Search artifact titles and bodies",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			artifacts, err := deps.Artifacts.Search(cmd.Context(), core.ProjectID(searchProject), args[0])
			if err != nil {
				return err
			}
			return deps.emit(artifacts, func() {
				rows := make([][]string, 0, len(artifacts))
				for _, artifact := range artifacts {
					rows = append(rows, []string{string(artifact.ID), string(artifact.Kind), artifact.Title})
				}
				deps.printTable([]string{"ID", "KIND", "TITLE"}, rows)
			})
		},
	}
	search.Flags().StringVar(&searchProject, "project", "", "filter by project id")

	cmd.AddCommand(add, list, search)
	return cmd
}
