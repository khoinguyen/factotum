package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/rank"
	"github.com/khoinguyen/factotum/pkg/store"
)

func newTaskCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Manage the task graph"}

	cmd.AddCommand(
		newTaskCreateCommand(deps),
		newTaskListCommand(deps),
		newTaskGetCommand(deps),
		newTaskUpdateCommand(deps),
		newTaskSetCommand(deps),
		newTaskApplyCommand(deps),
		newTaskEditCommand(deps),
		newTaskDepCommand(deps),
		newTaskAssignCommand(deps),
		newTaskNoteCommand(deps),
		newTaskNextCommand(deps),
		newTaskDeleteCommand(deps),
		statusCommand(deps, "start", core.StatusInProgress, "Mark a task in progress"),
		statusCommand(deps, "review", core.StatusReadyForReview, "Mark a task ready for review"),
		statusCommand(deps, "done", core.StatusDone, "Mark a task done"),
		statusCommand(deps, "reopen", core.StatusTodo, "Return a task to todo"),
		statusCommand(deps, "block", core.StatusBlocked, "Mark a task blocked"),
		statusCommand(deps, "cancel", core.StatusCancelled, "Cancel a task"),
	)
	return cmd
}

func newTaskCreateCommand(deps *Deps) *cobra.Command {
	var projectID, repo, kind, title, body, bodyFile, id string
	var priority int
	var labels, depIDs []string

	add := &cobra.Command{
		Use:   "create",
		Short: "Create a task or milestone",
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
			input := app.TaskInput{
				ProjectID:   project,
				Repo:        repo,
				Kind:        core.TaskKind(kind),
				Title:       title,
				Description: body,
				Priority:    priority,
				Labels:      labels,
			}
			if id != "" {
				customID := core.TaskID(id)
				input.ID = &customID
			}
			task, err := deps.Tasks.Add(cmd.Context(), input)
			if err != nil {
				return err
			}
			for _, dep := range depIDs {
				if _, err := deps.Tasks.AddDep(cmd.Context(), task.ID, core.TaskID(dep)); err != nil {
					return err
				}
			}
			hints := []hint{
				{Command: fmt.Sprintf("ft task get %s", task.ID), About: "inspect the task"},
				{Command: fmt.Sprintf("ft task next --project %s", task.ProjectID), About: "see what to start"},
			}
			return deps.emit(task, func() {
				deps.printFields(deps.taskFields(task, f("created", true))...)
			}, hints...)
		},
	}
	add.Flags().StringVarP(&projectID, "project", "p", "", "project id (required)")
	add.Flags().StringVar(&id, "id", "", "explicit task id (for imports)")
	add.Flags().StringVarP(&repo, "repo", "r", "", "repository name within the project")
	add.Flags().StringVarP(&kind, "kind", "k", string(core.KindTask), "task kind: task or milestone")
	add.Flags().StringVarP(&title, "title", "t", "", "task title (required)")
	add.Flags().StringVarP(&body, "body", "b", "", "task body (description)")
	add.Flags().StringVarP(&bodyFile, "body-file", "f", "", "read the task body from a file")
	add.Flags().IntVar(&priority, "priority", 0, "task priority (higher is more important)")
	add.Flags().StringArrayVar(&labels, "label", nil, "label (repeatable)")
	add.Flags().StringArrayVarP(&depIDs, "dep", "d", nil, "dependency task id (repeatable)")
	return add
}

func newTaskListCommand(deps *Deps) *cobra.Command {
	var projectID, repo string
	var statuses, kinds []string

	list := &cobra.Command{
		Use:   "list",
		Short: "List tasks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectID = string(deps.resolveProject(projectID))
			filter := store.TaskFilter{ProjectID: core.ProjectID(projectID)}
			if repo != "" {
				filter.Repo = &repo
			}
			for _, status := range statuses {
				filter.Statuses = append(filter.Statuses, core.TaskStatus(status))
			}
			if len(kinds) > 0 {
				kind := core.TaskKind(kinds[0])
				filter.Kind = &kind
			}
			tasks, err := deps.Tasks.List(cmd.Context(), filter)
			if err != nil {
				return err
			}
			return deps.emit(tasks, func() {
				rows := make([][]string, 0, len(tasks))
				for _, task := range tasks {
					rows = append(rows, []string{string(task.ID), string(task.Kind), task.Title, string(task.Status), string(task.ProjectID), deps.repoValue(task.Repo)})
				}
				deps.printTable([]string{"ID", "KIND", "TITLE", "STATUS", "PROJECT", "REPO"}, rows)
			}, taskListHints(tasks, projectID)...)
		},
	}
	list.Flags().StringVarP(&projectID, "project", "p", "", "filter by project id")
	list.Flags().StringVarP(&repo, "repo", "r", "", "filter by repository name")
	list.Flags().StringArrayVarP(&statuses, "status", "s", nil, "filter by status (repeatable)")
	list.Flags().StringArrayVarP(&kinds, "kind", "k", nil, "filter by kind")
	return list
}

func newTaskGetCommand(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "get <task>",
		Short: "Get a task",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.Get(cmd.Context(), core.TaskID(args[0]))
			if err != nil {
				return err
			}
			return deps.emit(taskDocFrom(task), func() {
				actors := deps.actorResolver(cmd.Context())
				deps.printf("(%s) %s: %s\n", task.Status, task.ID, task.Title)
				deps.printf("project: %s\n", task.ProjectID)
				if task.Kind == core.KindMilestone {
					deps.printf("kind: milestone\n")
				}
				if task.Repo != "" {
					deps.printf("repo: %s\n", deps.repoValue(task.Repo))
				}
				if task.AssigneeID != nil {
					deps.printf("assignee: %s\n", actorLabel(actors, *task.AssigneeID))
				}
				for _, actor := range task.WaitingOn {
					deps.printf("waiting_on: %s\n", actorLabel(actors, actor))
				}
				if len(task.Deps) > 0 {
					ids := make([]string, 0, len(task.Deps))
					for _, dep := range task.Deps {
						ids = append(ids, string(dep))
					}
					deps.printf("deps: %s\n", strings.Join(ids, ", "))
				}
				if task.Description != "" {
					deps.printf("\n=== Description ===\n%s\n", wrapText(task.Description, textWidth()))
				}
				if len(task.Notes) > 0 {
					notes := append([]core.Note(nil), task.Notes...)
					sort.SliceStable(notes, func(i, j int) bool {
						if !notes[i].CreatedAt.Equal(notes[j].CreatedAt) {
							return notes[i].CreatedAt.Before(notes[j].CreatedAt)
						}
						return notes[i].ID < notes[j].ID
					})
					deps.printf("\n=== Notes ===\n")
					for _, note := range notes {
						author := "-"
						if note.Author != "" {
							author = actorName(actors, note.Author)
						}
						deps.printf("%s (%s)\n\n", author, note.CreatedAt.UTC().Format(time.RFC3339))
						body := note.Body
						for _, link := range note.Links {
							body += fmt.Sprintf("\n[%s] %s", link.Kind, link.URL)
						}
						deps.printf("%s\n", wrapText(body, textWidth()))
						deps.printf("---\n")
					}
				}
			}, deps.taskGetHints(cmd.Context(), task)...)
		},
	}
}

func newTaskDepCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "dep", Short: "Manage task dependencies"}
	add := &cobra.Command{
		Use:   "create <task> <depends-on>",
		Short: "Create a dependency (rejects cycles)",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.AddDep(cmd.Context(), core.TaskID(args[0]), core.TaskID(args[1]))
			if err != nil {
				return err
			}
			return deps.emit(task, func() {
				deps.printFields(f("task_id", task.ID), f("dependency", args[1]), f("added", true), f("project", task.ProjectID), f("repo", deps.repoValue(task.Repo)))
			}, hint{Command: fmt.Sprintf("ft task get %s", args[0]), About: "see the updated graph"})
		},
	}
	rm := &cobra.Command{
		Use:   "delete <task> <depends-on>",
		Short: "Delete a dependency",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.RemoveDep(cmd.Context(), core.TaskID(args[0]), core.TaskID(args[1]))
			if err != nil {
				return err
			}
			return deps.emit(task, func() {
				deps.printFields(f("task_id", task.ID), f("dependency", args[1]), f("removed", true), f("project", task.ProjectID), f("repo", deps.repoValue(task.Repo)))
			}, hint{Command: fmt.Sprintf("ft task get %s", args[0]), About: "see the updated graph"})
		},
	}
	cmd.AddCommand(add, rm)
	return cmd
}

func newTaskAssignCommand(deps *Deps) *cobra.Command {
	var actorRef string
	var unassign bool

	cmd := &cobra.Command{
		Use:   "assign <task>",
		Short: "Assign a task to an actor",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var actorID *core.ActorID
			switch {
			case unassign:
			case actorRef != "":
				actor, err := deps.Actors.Resolve(cmd.Context(), actorRef)
				if err != nil {
					return err
				}
				actorID = &actor.ID
			default:
				return fmt.Errorf("provide --actor <ref> or --unassign")
			}
			task, err := deps.Tasks.Assign(cmd.Context(), core.TaskID(args[0]), actorID)
			if err != nil {
				return err
			}
			assignee := "none"
			if task.AssigneeID != nil {
				assignee = string(*task.AssigneeID)
			}
			deps.printFields(f("task_id", task.ID), f("assignee", assignee), f("project", task.ProjectID), f("repo", deps.repoValue(task.Repo)))
			if task.AssigneeID != nil {
				deps.suggest(hint{Command: fmt.Sprintf("ft task start %s", task.ID), About: "begin work"})
			} else {
				deps.suggest(hint{Command: fmt.Sprintf("ft task next --project %s", task.ProjectID), About: "pick up another task"})
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&actorRef, "actor", "a", "", "actor id or name")
	cmd.Flags().BoolVar(&unassign, "unassign", false, "clear the assignee")
	return cmd
}

func statusCommand(deps *Deps, use string, status core.TaskStatus, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <task>",
		Short: short,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.SetStatus(cmd.Context(), core.TaskID(args[0]), status)
			if err != nil {
				return err
			}
			deps.printFields(deps.taskFields(task)...)
			deps.suggest(deps.taskGetHints(cmd.Context(), task)...)
			return nil
		},
	}
}

// statusShortcut exposes a task status transition as a top-level command, so
// `ft done <task>` == `ft task done <task>` == `ft task set <task> status=done`.
func statusShortcut(use string, status core.TaskStatus, short string) CommandFactory {
	return func(deps *Deps) *cobra.Command {
		return statusCommand(deps, use, status, short)
	}
}

func newTaskNoteCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "note", Short: "Manage task notes"}
	var body string
	var linkSpecs []string

	add := &cobra.Command{
		Use:   "create <task>",
		Short: "Create a note on a task",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireFlags(cmd, "body"); err != nil {
				return err
			}
			links := make([]core.Link, 0, len(linkSpecs))
			for _, spec := range linkSpecs {
				kind, url, ok := strings.Cut(spec, "=")
				if !ok {
					return fmt.Errorf("invalid --link %q, want kind=url", spec)
				}
				links = append(links, core.Link{Kind: core.LinkKind(kind), URL: url})
			}
			author := deps.currentActorID(cmd.Context())
			task, err := deps.Tasks.AddNote(cmd.Context(), core.TaskID(args[0]), app.NoteInput{Body: body, Links: links, Author: author})
			if err != nil {
				return err
			}
			deps.printFields(f("task_id", task.ID), f("noted", true), f("project", task.ProjectID), f("repo", deps.repoValue(task.Repo)))
			deps.suggest(hint{Command: fmt.Sprintf("ft task get %s", task.ID), About: "review the note"})
			return nil
		},
	}
	add.Flags().StringVarP(&body, "body", "b", "", "note body (required)")
	add.Flags().StringArrayVar(&linkSpecs, "link", nil, "link kind=url (repeatable)")

	cmd.AddCommand(add)
	return cmd
}

func newTaskNextCommand(deps *Deps) *cobra.Command {
	var projectID, forRef, repo, toward, rankerName string
	var limit int
	var all bool

	cmd := &cobra.Command{
		Use:   "next",
		Short: "Rank the startable tasks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all && cmd.Flags().Changed("project") {
				return usageError(cmd, "only one of --project or --all may be set")
			}
			if !all && projectID == "" {
				projectID = deps.Config.Project
			}
			if !all && projectID == "" {
				return usageError(cmd, "one of --project or --all is required, or set default_project")
			}
			var snapshot *app.Snapshot
			var err error
			if all {
				snapshot, err = app.LoadAllSnapshot(cmd.Context(), deps.Backend)
			} else {
				snapshot, err = app.LoadSnapshot(cmd.Context(), deps.Backend, core.ProjectID(projectID))
			}
			if err != nil {
				return err
			}

			candidates := unionIDs(snapshot.Ready.Agent, snapshot.Ready.Human)
			if forRef != "" {
				actor, err := deps.Actors.Resolve(cmd.Context(), forRef)
				if err != nil {
					return err
				}
				candidates = filterByActor(snapshot, candidates, actor)
			}

			ranker, err := deps.Rankers.MustLookup(rankerName)
			if err != nil {
				return err
			}
			scored, err := ranker.Rank(cmd.Context(), rank.Request{
				Graph:      snapshot.Graph,
				Tasks:      snapshot.Tasks,
				Candidates: candidates,
				Toward:     core.TaskID(toward),
				Repo:       optionalString(repo),
			})
			if err != nil {
				return err
			}
			if limit > 0 && len(scored) > limit {
				scored = scored[:limit]
			}
			var top *core.Task
			if len(scored) > 0 {
				if task, ok := snapshot.Graph.Task(scored[0].TaskID); ok {
					top = &task
				}
			}
			hintProject := projectID
			if all && top != nil {
				hintProject = string(top.ProjectID)
			}
			return deps.emit(scored, func() {
				rows := make([][]string, 0, len(scored))
				for _, entry := range scored {
					task, _ := snapshot.Graph.Task(entry.TaskID)
					rows = append(rows, []string{fmt.Sprintf("%.2f", entry.Score), string(entry.TaskID), task.Title, string(task.ProjectID), deps.repoValue(task.Repo)})
				}
				deps.printTable([]string{"SCORE", "TASK", "TITLE", "PROJECT", "REPO"}, rows)
			}, taskNextHints(hintProject, top)...)
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (required unless --all)")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "rank ready tasks across all projects")
	cmd.Flags().StringVar(&forRef, "for", "", "restrict to an actor (id or name)")
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "restrict to a repository name")
	cmd.Flags().StringVar(&toward, "toward", "", "prefer tasks on the path to this task")
	cmd.Flags().StringVar(&rankerName, "rank", "composite", "ranker: composite, unblock, milestone, or toward")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "maximum number of tasks (0 means all)")
	return cmd
}

func newTaskDeleteCommand(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <task>",
		Short: "Delete a task",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.Get(cmd.Context(), core.TaskID(args[0]))
			if err != nil {
				return err
			}
			if err := deps.Tasks.Delete(cmd.Context(), task.ID); err != nil {
				return err
			}
			deps.printFields(f("task_id", task.ID), f("deleted", true), f("project", task.ProjectID), f("repo", deps.repoValue(task.Repo)))
			deps.suggest(hint{Command: fmt.Sprintf("ft task list --project %s", task.ProjectID), About: "review the remaining tasks"})
			return nil
		},
	}
}

func newTaskUpdateCommand(deps *Deps) *cobra.Command {
	var title, body, bodyFile, repo, kind string
	var priority int
	var labels []string

	cmd := &cobra.Command{
		Use:   "update <task>",
		Short: "Update a task's fields",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := app.TaskUpdate{}
			if cmd.Flags().Changed("kind") {
				taskKind := core.TaskKind(kind)
				patch.Kind = &taskKind
			}
			if cmd.Flags().Changed("title") {
				patch.Title = &title
			}
			if cmd.Flags().Changed("body") {
				patch.Description = &body
			}
			if cmd.Flags().Changed("body-file") {
				data, err := os.ReadFile(bodyFile)
				if err != nil {
					return fmt.Errorf("read %s: %w", bodyFile, err)
				}
				value := string(data)
				patch.Description = &value
			}
			if cmd.Flags().Changed("priority") {
				patch.Priority = &priority
			}
			if cmd.Flags().Changed("repo") {
				patch.Repo = &repo
			}
			if cmd.Flags().Changed("label") {
				patch.Labels = labels
			}
			task, err := deps.Tasks.Update(cmd.Context(), core.TaskID(args[0]), patch)
			if err != nil {
				return err
			}
			return deps.emit(task, func() {
				deps.printFields(deps.taskFields(task, f("updated", true))...)
			},
				hint{Command: fmt.Sprintf("ft task get %s", task.ID), About: "inspect the updated task"})
		},
	}
	cmd.Flags().StringVarP(&title, "title", "t", "", "task title")
	cmd.Flags().StringVarP(&kind, "kind", "k", "", "task kind: task or milestone")
	cmd.Flags().StringVarP(&body, "body", "b", "", "task body (description)")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "f", "", "read the task body from a file")
	cmd.Flags().IntVar(&priority, "priority", 0, "task priority")
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "repository name within the project")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "label (repeatable; replaces existing)")
	return cmd
}

func newTaskSetCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <task> field=value [field=value...]",
		Short: "Set task fields (status, priority, kind, repo, title, body, labels)",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return usageError(cmd, "expected <task> and at least one field=value assignment")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			set, err := parseTaskSet(args[1:])
			if err != nil {
				return err
			}
			task, err := deps.Tasks.Set(cmd.Context(), core.TaskID(args[0]), set)
			if err != nil {
				return err
			}
			return deps.emit(task, func() {
				deps.printFields(deps.taskFields(task, f("updated", true))...)
			},
				hint{Command: fmt.Sprintf("ft task get %s", task.ID), About: "inspect the updated task"})
		},
	}
	return cmd
}

// parseTaskSet turns `field=value` assignments into a TaskSet. Long text
// fields accept `@path` (read from a file) or `-` (read from stdin).
func parseTaskSet(assignments []string) (app.TaskSet, error) {
	var set app.TaskSet
	for _, assignment := range assignments {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok {
			return app.TaskSet{}, fmt.Errorf("invalid assignment %q, want field=value", assignment)
		}
		switch key {
		case "status":
			status := core.TaskStatus(value)
			if !status.Valid() {
				return app.TaskSet{}, fmt.Errorf("invalid status %q", value)
			}
			set.Status = &status
		case "priority":
			priority, err := strconv.Atoi(value)
			if err != nil {
				return app.TaskSet{}, fmt.Errorf("invalid priority %q, want an integer", value)
			}
			set.Priority = &priority
		case "kind":
			kind := core.TaskKind(value)
			if !kind.Valid() {
				return app.TaskSet{}, fmt.Errorf("invalid kind %q, want task or milestone", value)
			}
			set.Kind = &kind
		case "repo":
			set.Repo = &value
		case "title":
			set.Title = &value
		case "body", "description":
			text, err := readFieldValue(value)
			if err != nil {
				return app.TaskSet{}, err
			}
			set.Description = &text
		case "labels":
			set.Labels = parseLabels(value)
		default:
			return app.TaskSet{}, fmt.Errorf("unknown field %q, want status, priority, kind, repo, title, body, or labels", key)
		}
	}
	return set, nil
}

func readFieldValue(value string) (string, error) {
	switch {
	case value == "-":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), nil
	case strings.HasPrefix(value, "@"):
		path := strings.TrimPrefix(value, "@")
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		return string(data), nil
	default:
		return value, nil
	}
}

// parseLabels splits a comma-separated list. An empty value yields an empty,
// non-nil slice, which clears the task's labels.
func parseLabels(value string) []string {
	labels := []string{}
	for _, label := range strings.Split(value, ",") {
		if label = strings.TrimSpace(label); label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}

func newTaskApplyCommand(deps *Deps) *cobra.Command {
	var filename, format string

	cmd := &cobra.Command{
		Use:   "apply -f <file>",
		Short: "Apply a task document (json or yaml)",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireFlags(cmd, "filename"); err != nil {
				return err
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				return fmt.Errorf("read %s: %w", filename, err)
			}
			doc, err := parseTaskDoc(data, docFormat(filename, format))
			if err != nil {
				return err
			}
			if doc.ID == nil || *doc.ID == "" {
				return fmt.Errorf("%w: document is missing id", core.ErrInvalid)
			}
			task, err := deps.Tasks.Get(cmd.Context(), core.TaskID(*doc.ID))
			if err != nil {
				return err
			}
			set, err := doc.taskSet(task)
			if err != nil {
				return err
			}
			if set.Empty() {
				return deps.emit(taskDocFrom(task), func() {
					deps.printFields(deps.taskFields(task, f("updated", false))...)
				})
			}
			updated, err := deps.Tasks.Set(cmd.Context(), task.ID, set)
			if err != nil {
				return err
			}
			return deps.emit(taskDocFrom(updated), func() {
				deps.printFields(deps.taskFields(updated, f("updated", true))...)
			}, hint{Command: fmt.Sprintf("ft task get %s", updated.ID), About: "inspect the applied task"})
		},
	}
	cmd.Flags().StringVarP(&filename, "filename", "f", "", "task document file (required)")
	cmd.Flags().StringVar(&format, "format", "", "document format: json or yaml (default by extension)")
	return cmd
}

func newTaskEditCommand(deps *Deps) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "edit <task>",
		Short: "Edit a task document in $EDITOR",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.Get(cmd.Context(), core.TaskID(args[0]))
			if err != nil {
				return err
			}
			original, err := marshalTaskDoc(taskDocFrom(task), format)
			if err != nil {
				return err
			}
			edited, err := editTask(original, format)
			if err != nil {
				return err
			}
			if bytes.Equal(bytes.TrimSpace(original), bytes.TrimSpace(edited)) {
				return deps.emit(taskDocFrom(task), func() {
					deps.printFields(deps.taskFields(task, f("updated", false))...)
				})
			}
			doc, err := parseTaskDoc(edited, format)
			if err != nil {
				return err
			}
			set, err := doc.taskSet(task)
			if err != nil {
				return err
			}
			if set.Empty() {
				return deps.emit(taskDocFrom(task), func() {
					deps.printFields(deps.taskFields(task, f("updated", false))...)
				})
			}
			updated, err := deps.Tasks.Set(cmd.Context(), task.ID, set)
			if err != nil {
				return err
			}
			return deps.emit(taskDocFrom(updated), func() {
				deps.printFields(deps.taskFields(updated, f("updated", true))...)
			}, hint{Command: fmt.Sprintf("ft task get %s", updated.ID), About: "inspect the edited task"})
		},
	}
	cmd.Flags().StringVar(&format, "format", "yaml", "document format: json or yaml")
	return cmd
}

// taskFields is the canonical single-result field set for a task: identity, an
// optional action field (created/updated), then kind/title/status/project, with
// repo last.
func (d *Deps) taskFields(task *core.Task, action ...field) []field {
	fields := []field{f("task_id", task.ID)}
	fields = append(fields, action...)
	return append(fields,
		f("kind", task.Kind), f("title", task.Title), f("status", task.Status),
		f("project", task.ProjectID), f("repo", d.repoValue(task.Repo)),
	)
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (d *Deps) actorResolver(ctx context.Context) map[core.ActorID]core.Actor {
	actors, err := d.Actors.List(ctx)
	if err != nil {
		return map[core.ActorID]core.Actor{}
	}
	byID := make(map[core.ActorID]core.Actor, len(actors))
	for _, actor := range actors {
		byID[actor.ID] = *actor
	}
	return byID
}

func actorLabel(actors map[core.ActorID]core.Actor, id core.ActorID) string {
	if actor, ok := actors[id]; ok {
		return fmt.Sprintf("(%s) %s", actor.Kind, actor.Name)
	}
	return string(id)
}

func actorName(actors map[core.ActorID]core.Actor, id core.ActorID) string {
	if actor, ok := actors[id]; ok {
		return actor.Name
	}
	return string(id)
}

func (d *Deps) currentActorID(ctx context.Context) *core.ActorID {
	if d.ActorRef == "" {
		return nil
	}
	actor, err := d.Actors.Resolve(ctx, d.ActorRef)
	if err != nil {
		return nil
	}
	return &actor.ID
}

func unionIDs(a, b []core.TaskID) []core.TaskID {
	seen := make(map[core.TaskID]struct{}, len(a)+len(b))
	out := make([]core.TaskID, 0, len(a)+len(b))
	for _, list := range [][]core.TaskID{a, b} {
		for _, id := range list {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func filterByActor(snapshot *app.Snapshot, candidates []core.TaskID, actor *core.Actor) []core.TaskID {
	out := make([]core.TaskID, 0, len(candidates))
	for _, id := range candidates {
		task, ok := snapshot.Graph.Task(id)
		if !ok {
			continue
		}
		if task.AssigneeID != nil && *task.AssigneeID == actor.ID {
			out = append(out, id)
			continue
		}
		if task.AssigneeID == nil && actor.Kind == core.ActorHuman {
			out = append(out, id)
		}
	}
	return out
}
