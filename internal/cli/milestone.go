package cli

import (
	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

func newMilestoneCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "milestone", Short: "Manage milestones (release gates)"}

	var projectID, title, description string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a milestone",
		RunE: func(cmd *cobra.Command, _ []string) error {
			task, err := deps.Tasks.Add(cmd.Context(), app.TaskInput{
				ProjectID:   core.ProjectID(projectID),
				Kind:        core.KindMilestone,
				Title:       title,
				Description: description,
			})
			if err != nil {
				return err
			}
			return deps.emit(task, func() { deps.printf("%s\t%s\n", task.ID, task.Title) })
		},
	}
	create.Flags().StringVar(&projectID, "project", "", "project id (required)")
	create.Flags().StringVar(&title, "title", "", "milestone title (required)")
	create.Flags().StringVar(&description, "description", "", "milestone description")
	_ = create.MarkFlagRequired("project")
	_ = create.MarkFlagRequired("title")

	var listProject string
	list := &cobra.Command{
		Use:   "list",
		Short: "List milestones",
		RunE: func(cmd *cobra.Command, _ []string) error {
			kind := core.KindMilestone
			tasks, err := deps.Tasks.List(cmd.Context(), store.TaskFilter{ProjectID: core.ProjectID(listProject), Kind: &kind})
			if err != nil {
				return err
			}
			return deps.emit(tasks, func() {
				rows := make([][]string, 0, len(tasks))
				for _, task := range tasks {
					rows = append(rows, []string{string(task.ID), string(task.Status), task.Title})
				}
				deps.printTable([]string{"ID", "STATUS", "TITLE"}, rows)
			})
		},
	}
	list.Flags().StringVar(&listProject, "project", "", "filter by project id")

	done := &cobra.Command{
		Use:   "done <milestone>",
		Short: "Mark a milestone done (unblocks dependents)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.SetStatus(cmd.Context(), core.TaskID(args[0]), core.StatusDone)
			if err != nil {
				return err
			}
			deps.printf("%s\t%s\n", task.ID, task.Status)
			return nil
		},
	}

	cmd.AddCommand(create, list, done)
	return cmd
}
