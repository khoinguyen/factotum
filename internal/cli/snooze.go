package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

func newTaskSnoozeCommand(deps *Deps) *cobra.Command {
	var until, untilTask string
	var indefinite bool

	cmd := &cobra.Command{
		Use:   "snooze <task>",
		Short: "Park a task out of ranking until a date, a task, or indefinitely",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conditions := 0
			for _, name := range []string{"until", "until-task"} {
				if cmd.Flags().Changed(name) {
					conditions++
				}
			}
			if indefinite {
				conditions++
			}
			if conditions != 1 {
				return usageError(cmd, "provide exactly one of --until, --until-task, or --indefinite")
			}

			var snooze core.Snooze
			switch {
			case cmd.Flags().Changed("until"):
				when, err := parseNotBefore(until, deps.Clock.Now())
				if err != nil {
					return usageError(cmd, "invalid --until %q: %s", until, err)
				}
				snooze.Until = &when
			case cmd.Flags().Changed("until-task"):
				id := core.TaskID(untilTask)
				snooze.UntilTask = &id
			default:
				snooze.Indefinite = true
			}

			task, err := deps.Tasks.Snooze(cmd.Context(), core.TaskID(args[0]), snooze)
			if err != nil {
				return err
			}
			return deps.emit(taskDocFrom(task), func() {
				deps.printFields(deps.taskFields(task, f("snoozed", app.SnoozeDescription(snooze)))...)
			}, hint{Command: fmt.Sprintf("ft task unsnooze %s", task.ID), About: "wake it up"})
		},
	}
	cmd.Flags().StringVar(&until, "until", "", "snooze until a date (YYYY-MM-DD), RFC3339, or +duration")
	cmd.Flags().StringVar(&untilTask, "until-task", "", "snooze until this task resolves")
	cmd.Flags().BoolVar(&indefinite, "indefinite", false, "snooze until explicitly unsnoozed")
	return cmd
}

func newTaskUnsnoozeCommand(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "unsnooze <task>",
		Short: "Remove a task's snooze",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.Unsnooze(cmd.Context(), core.TaskID(args[0]))
			if err != nil {
				return err
			}
			return deps.emit(taskDocFrom(task), func() {
				deps.printFields(deps.taskFields(task, f("unsnoozed", true))...)
			}, hint{Command: fmt.Sprintf("ft task next --project %s", task.ProjectID), About: "see it in ranking"})
		},
	}
}
