package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// newIdeaCommand is the user-facing idea surface: a PO captures and manages
// ideas here, over the same TicketService storage the task tree uses. Ideas are
// non-executable captures, so this tree offers no lifecycle or assignment verbs.
func newIdeaCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "idea", Short: "Capture and manage ideas (non-executable)"}
	cmd.AddCommand(
		newIdeaCreateCommand(deps),
		newIdeaListCommand(deps),
		newIdeaSearchCommand(deps),
		newIdeaGetCommand(deps),
		newIdeaPromoteCommand(deps),
	)
	return cmd
}

func newIdeaCreateCommand(deps *Deps) *cobra.Command {
	var projectID, repo, title, body, bodyFile string
	var labels []string

	create := &cobra.Command{
		Use:   "create",
		Short: "Capture an idea (non-executable)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireFlags(cmd, "title"); err != nil {
				return err
			}
			project := deps.resolveProject(projectID)
			if err := requireProject(cmd, project); err != nil {
				return err
			}
			if bodyFile != "" {
				data, err := os.ReadFile(bodyFile)
				if err != nil {
					return fmt.Errorf("read %s: %w", bodyFile, err)
				}
				body = string(data)
			}
			idea, err := deps.Tasks.Add(cmd.Context(), app.TicketInput{
				ProjectID:   project,
				Repo:        repo,
				Kind:        core.KindIdea,
				Title:       title,
				Description: body,
				Labels:      labels,
			})
			if err != nil {
				return err
			}
			deps.warnDuplicateTitle(cmd.Context(), idea)
			return deps.emit(idea, func() {
				deps.printFields(deps.taskFields(idea, f("created", true))...)
			}, ideaCreateHints(idea)...)
		},
	}
	create.Flags().StringVarP(&projectID, "project", "p", "", "project id (required)")
	create.Flags().StringVarP(&repo, "repo", "r", "", "repository name within the project")
	create.Flags().StringVarP(&title, "title", "t", "", "idea title (required)")
	create.Flags().StringVarP(&body, "body", "b", "", "idea body (the context to keep)")
	create.Flags().StringVarP(&bodyFile, "body-file", "f", "", "read the idea body from a file")
	create.Flags().StringArrayVar(&labels, "label", nil, "label (repeatable)")
	return create
}

func newIdeaListCommand(deps *Deps) *cobra.Command {
	var projectID, repo string
	var statuses, labels []string

	list := &cobra.Command{
		Use:   "list",
		Short: "List ideas",
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := string(deps.resolveProject(projectID))
			ideas, err := deps.Tasks.List(cmd.Context(), ideaFilter(project, repo, statuses, labels))
			if err != nil {
				return err
			}
			return deps.emit(ideaListEntries(ideas), func() {
				deps.printTable(ideaTableHeader(), ideaTableRows(deps, ideas))
			}, ideaListHints(project, ideas)...)
		},
	}
	list.Flags().StringVarP(&projectID, "project", "p", "", "filter by project id")
	list.Flags().StringVarP(&repo, "repo", "r", "", "filter by repository name")
	list.Flags().StringArrayVarP(&statuses, "status", "s", nil, "filter by status (repeatable)")
	list.Flags().StringArrayVarP(&labels, "label", "l", nil, "filter by label (repeatable; all must match)")
	return list
}

func newIdeaSearchCommand(deps *Deps) *cobra.Command {
	var projectID, repo string
	var statuses, labels []string
	var noRerank bool

	search := &cobra.Command{
		Use:   "search <query>",
		Short: "Search idea titles, bodies, and notes",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := string(deps.resolveProject(projectID))
			ideas, err := deps.Tasks.Search(cmd.Context(), ideaFilter(project, repo, statuses, labels), args[0])
			if err != nil {
				return err
			}
			ideas = deps.maybeRerankTasks(cmd, args[0], ideas, noRerank)
			return deps.emit(ideaListEntries(ideas), func() {
				deps.printTable(ideaTableHeader(), ideaTableRows(deps, ideas))
			}, ideaSearchHints(ideas, project)...)
		},
	}
	search.Flags().StringVarP(&projectID, "project", "p", "", "filter by project id")
	search.Flags().StringVarP(&repo, "repo", "r", "", "filter by repository name")
	search.Flags().StringArrayVarP(&statuses, "status", "s", nil, "filter by status (repeatable)")
	search.Flags().StringArrayVarP(&labels, "label", "l", nil, "filter by label (repeatable; all must match)")
	search.Flags().BoolVar(&noRerank, "no-rerank", false, "keep lexical order instead of reranking by meaning")
	return search
}

// newIdeaGetCommand reuses the task detail view: an idea is a task of kind
// idea, so its document, checks, notes, and dependents render identically.
func newIdeaGetCommand(deps *Deps) *cobra.Command {
	cmd := newTaskGetCommand(deps)
	cmd.Use = "get <idea>"
	cmd.Short = "Get an idea"
	return cmd
}

// newIdeaPromoteCommand reuses the task promotion: it creates an executable
// task linked to the idea as its origin and keeps the idea as history. The
// canonical verb for a human; `ft task promote` stays as its peer.
func newIdeaPromoteCommand(deps *Deps) *cobra.Command {
	cmd := newTaskPromoteCommand(deps)
	cmd.Use = "promote <idea>"
	cmd.Short = "Promote an idea to an executable task, keeping the idea as history"
	return cmd
}

// ideaFilter scopes a read to the project's kind=idea captures, mirroring the
// task-list filters so `ft idea list` matches `ft task list -k idea`.
func ideaFilter(projectID, repo string, statuses, labels []string) store.TicketFilter {
	kind := core.KindIdea
	filter := store.TicketFilter{ProjectID: core.ProjectID(projectID), Kind: &kind, Labels: labels}
	if repo != "" {
		filter.Repo = &repo
	}
	for _, status := range statuses {
		filter.Statuses = append(filter.Statuses, core.TicketStatus(status))
	}
	return filter
}

func ideaListEntries(ideas []*core.Ticket) []taskListEntry {
	entries := make([]taskListEntry, 0, len(ideas))
	for _, idea := range ideas {
		entries = append(entries, taskListEntryFrom(idea))
	}
	return entries
}

func ideaTableHeader() []string {
	return []string{"ID", "KIND", "TITLE", "STATUS", "PROJECT", "REPO"}
}

func ideaTableRows(deps *Deps, ideas []*core.Ticket) [][]string {
	rows := make([][]string, 0, len(ideas))
	for _, idea := range ideas {
		rows = append(rows, []string{
			string(idea.ID), string(idea.Kind), idea.Title, string(idea.Status),
			string(idea.ProjectID), deps.repoValue(idea.Repo),
		})
	}
	return rows
}
