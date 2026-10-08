package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// captureSurface parameterizes the human capture command trees (`ft idea`,
// `ft bug`). Both share one model - a non-executable Ticket recorded for
// refinement into a linked task - and differ only in their noun, the verb a
// human uses to refine them, and the copy they print.
type captureSurface struct {
	kind    core.TicketKind
	noun    string
	article string
	refine  string
	refined string
	// refineShort is the refinement command's one-line summary, written per
	// surface so each reads naturally ("promote an idea" / "triage a bug").
	refineShort string
}

var (
	ideaCapture = captureSurface{
		kind:        core.KindIdea,
		noun:        "idea",
		article:     "an",
		refine:      "promote",
		refined:     "promoted",
		refineShort: "Promote an idea to an executable task, keeping the idea as history",
	}
	bugCapture = captureSurface{
		kind:        core.KindBug,
		noun:        "bug",
		article:     "a",
		refine:      "triage",
		refined:     "triaged",
		refineShort: "Triage a bug into an executable task, keeping the bug as history",
	}
)

// captureFor maps a capture kind to its surface, reporting false for a kind that
// is not a human capture.
func captureFor(kind core.TicketKind) (captureSurface, bool) {
	switch kind {
	case core.KindIdea:
		return ideaCapture, true
	case core.KindBug:
		return bugCapture, true
	default:
		return captureSurface{}, false
	}
}

// newCaptureCommand builds the user-facing surface for one capture kind: a human
// captures and manages them here, over the same TicketService storage the task
// tree uses. Captures are non-executable, so the tree offers no lifecycle or
// assignment verbs.
func newCaptureCommand(deps *Deps, s captureSurface) *cobra.Command {
	cmd := &cobra.Command{Use: s.noun, Short: "Capture and manage " + s.noun + "s (non-executable)"}
	cmd.AddCommand(
		newCaptureCreateCommand(deps, s),
		newCaptureListCommand(deps, s),
		newCaptureSearchCommand(deps, s),
		newCaptureGetCommand(deps, s),
		newCaptureRefineCommand(deps, s),
	)
	return cmd
}

func newCaptureCreateCommand(deps *Deps, s captureSurface) *cobra.Command {
	var projectID, repo, title, body, bodyFile string
	var labels []string

	create := &cobra.Command{
		Use:   "create",
		Short: "Capture " + s.article + " " + s.noun + " (non-executable)",
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
			capture, err := deps.Tasks.Add(cmd.Context(), app.TicketInput{
				ProjectID:   project,
				Repo:        repo,
				Kind:        s.kind,
				Title:       title,
				Description: body,
				Labels:      labels,
			})
			if err != nil {
				return err
			}
			deps.warnDuplicateTitle(cmd.Context(), capture)
			return deps.emit(capture, func() {
				deps.printFields(deps.taskFields(capture, f("created", true))...)
			}, captureCreateHints(s, capture)...)
		},
	}
	create.Flags().StringVarP(&projectID, "project", "p", "", "project id (required)")
	create.Flags().StringVarP(&repo, "repo", "r", "", "repository name within the project")
	create.Flags().StringVarP(&title, "title", "t", "", s.noun+" title (required)")
	create.Flags().StringVarP(&body, "body", "b", "", s.noun+" body (the context to keep)")
	create.Flags().StringVarP(&bodyFile, "body-file", "f", "", "read the "+s.noun+" body from a file")
	create.Flags().StringArrayVar(&labels, "label", nil, "label (repeatable)")
	return create
}

func newCaptureListCommand(deps *Deps, s captureSurface) *cobra.Command {
	var projectID, repo string
	var statuses, labels []string

	list := &cobra.Command{
		Use:   "list",
		Short: "List " + s.noun + "s",
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := string(deps.resolveProject(projectID))
			captures, err := deps.Tasks.List(cmd.Context(), captureFilter(s.kind, project, repo, statuses, labels))
			if err != nil {
				return err
			}
			return deps.emit(captureListEntries(captures), func() {
				deps.printTable(captureTableHeader(), captureTableRows(deps, captures))
			}, captureListHints(s, project, captures)...)
		},
	}
	list.Flags().StringVarP(&projectID, "project", "p", "", "filter by project id")
	list.Flags().StringVarP(&repo, "repo", "r", "", "filter by repository name")
	list.Flags().StringArrayVarP(&statuses, "status", "s", nil, "filter by status (repeatable)")
	list.Flags().StringArrayVarP(&labels, "label", "l", nil, "filter by label (repeatable; all must match)")
	return list
}

func newCaptureSearchCommand(deps *Deps, s captureSurface) *cobra.Command {
	var projectID, repo string
	var statuses, labels []string
	var noRerank bool

	search := &cobra.Command{
		Use:   "search <query>",
		Short: "Search " + s.noun + " titles, bodies, and notes",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := string(deps.resolveProject(projectID))
			captures, err := deps.Tasks.Search(cmd.Context(), captureFilter(s.kind, project, repo, statuses, labels), args[0])
			if err != nil {
				return err
			}
			captures = deps.maybeRerankTasks(cmd, args[0], captures, noRerank)
			return deps.emit(captureListEntries(captures), func() {
				deps.printTable(captureTableHeader(), captureTableRows(deps, captures))
			}, captureSearchHints(s, captures, project)...)
		},
	}
	search.Flags().StringVarP(&projectID, "project", "p", "", "filter by project id")
	search.Flags().StringVarP(&repo, "repo", "r", "", "filter by repository name")
	search.Flags().StringArrayVarP(&statuses, "status", "s", nil, "filter by status (repeatable)")
	search.Flags().StringArrayVarP(&labels, "label", "l", nil, "filter by label (repeatable; all must match)")
	search.Flags().BoolVar(&noRerank, "no-rerank", false, "keep lexical order instead of reranking by meaning")
	return search
}

// newCaptureGetCommand reuses the task detail view: a capture is a ticket of its
// kind, so its document, checks, notes, and dependents render identically.
func newCaptureGetCommand(deps *Deps, s captureSurface) *cobra.Command {
	cmd := newTaskGetCommand(deps)
	cmd.Use = "get <" + s.noun + ">"
	cmd.Short = "Get " + s.article + " " + s.noun
	return cmd
}

// newCaptureRefineCommand reuses the task promotion: it creates an executable
// task linked to the capture as its origin and keeps the capture as history. The
// verb is the kind's refine verb ("promote" for an idea, "triage" for a bug);
// `ft task promote` stays as their peer.
func newCaptureRefineCommand(deps *Deps, s captureSurface) *cobra.Command {
	cmd := newPromoteCommand(deps,
		fmt.Sprintf("%s <%s>", s.refine, s.noun),
		s.refineShort,
		s.refined)
	return cmd
}

// captureFilter scopes a read to the project's captures of one kind, mirroring
// the task-list filters so `ft idea list` matches `ft task list -k idea`.
func captureFilter(kind core.TicketKind, projectID, repo string, statuses, labels []string) store.TicketFilter {
	filter := store.TicketFilter{ProjectID: core.ProjectID(projectID), Kind: &kind, Labels: labels}
	if repo != "" {
		filter.Repo = &repo
	}
	for _, status := range statuses {
		filter.Statuses = append(filter.Statuses, core.TicketStatus(status))
	}
	return filter
}

func captureListEntries(captures []*core.Ticket) []taskListEntry {
	entries := make([]taskListEntry, 0, len(captures))
	for _, capture := range captures {
		entries = append(entries, taskListEntryFrom(capture))
	}
	return entries
}

func captureTableHeader() []string {
	return []string{"ID", "KIND", "TITLE", "STATUS", "PROJECT", "REPO"}
}

func captureTableRows(deps *Deps, captures []*core.Ticket) [][]string {
	rows := make([][]string, 0, len(captures))
	for _, capture := range captures {
		rows = append(rows, []string{
			string(capture.ID), string(capture.Kind), capture.Title, string(capture.Status),
			string(capture.ProjectID), deps.repoValue(capture.Repo),
		})
	}
	return rows
}
