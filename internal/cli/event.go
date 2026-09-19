package cli

import (
	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

func newEventCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "event", Short: "Inspect the audit log"}

	var projectID, taskID string
	var kinds []string
	var limit int

	list := &cobra.Command{
		Use:   "list",
		Short: "List events, newest first",
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectID = string(deps.resolveProject(projectID))
			filter := store.EventFilter{ProjectID: core.ProjectID(projectID), Limit: limit}
			if taskID != "" {
				id := core.TaskID(taskID)
				filter.TaskID = &id
			}
			for _, kind := range kinds {
				filter.Kinds = append(filter.Kinds, core.EventKind(kind))
			}
			events, err := deps.Backend.Events().List(cmd.Context(), filter)
			if err != nil {
				return err
			}
			return deps.emit(events, func() {
				rows := make([][]string, 0, len(events))
				for _, event := range events {
					rows = append(rows, []string{event.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), string(event.Kind), event.Summary})
				}
				deps.printTable([]string{"TIME", "KIND", "SUMMARY"}, rows)
			}, eventListHints(projectID, taskID)...)
		},
	}
	list.Flags().StringVarP(&projectID, "project", "p", "", "filter by project id")
	list.Flags().StringVarP(&taskID, "task", "t", "", "filter by task id")
	list.Flags().StringArrayVarP(&kinds, "kind", "k", nil, "filter by event kind (repeatable)")
	list.Flags().IntVarP(&limit, "limit", "n", 20, "maximum number of events")

	cmd.AddCommand(list)
	return cmd
}
