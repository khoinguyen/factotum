package cli

import (
	"fmt"

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
			if err := requireFlags(cmd, "title"); err != nil {
				return err
			}
			project := deps.resolveProject(projectID)
			if err := requireProject(cmd, project); err != nil {
				return err
			}
			task, err := deps.Tasks.Add(cmd.Context(), app.TaskInput{
				ProjectID:   project,
				Kind:        core.KindMilestone,
				Title:       title,
				Description: description,
			})
			if err != nil {
				return err
			}
			return deps.emit(task, func() {
				deps.printFields(deps.taskFields(task, f("created", true))...)
			},
				hint{Command: fmt.Sprintf("ft task dep create <task> %s", task.ID), About: "gate a task behind this milestone"},
				hint{Command: fmt.Sprintf("ft milestone list --project %s", task.ProjectID), About: "see all milestones"})
		},
	}
	create.Flags().StringVarP(&projectID, "project", "p", "", "project id (required)")
	create.Flags().StringVarP(&title, "title", "t", "", "milestone title (required)")
	create.Flags().StringVar(&description, "description", "", "milestone description")

	var listProject string
	list := &cobra.Command{
		Use:   "list",
		Short: "List milestones",
		RunE: func(cmd *cobra.Command, _ []string) error {
			listProject = string(deps.resolveProject(listProject))
			kind := core.KindMilestone
			tasks, err := deps.Tasks.List(cmd.Context(), store.TaskFilter{ProjectID: core.ProjectID(listProject), Kind: &kind})
			if err != nil {
				return err
			}
			return deps.emit(tasks, func() {
				rows := make([][]string, 0, len(tasks))
				for _, task := range tasks {
					rows = append(rows, []string{string(task.ID), task.Title, string(task.Status), string(task.ProjectID)})
				}
				deps.printTable([]string{"ID", "TITLE", "STATUS", "PROJECT"}, rows)
			}, milestoneListHints(listProject, tasks)...)
		},
	}
	list.Flags().StringVarP(&listProject, "project", "p", "", "filter by project id")

	done := &cobra.Command{
		Use:   "done <milestone>",
		Short: "Mark a milestone done (unblocks dependents)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.SetStatus(cmd.Context(), core.TaskID(args[0]), core.StatusDone)
			if err != nil {
				return err
			}
			deps.printFields(deps.taskFields(task)...)
			deps.suggest(deps.taskGetHints(cmd.Context(), task)...)
			return nil
		},
	}

	cmd.AddCommand(create, list, done)
	return cmd
}
