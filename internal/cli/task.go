package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
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
		newTaskAddCommand(deps),
		newTaskListCommand(deps),
		newTaskShowCommand(deps),
		newTaskUpdateCommand(deps),
		newTaskDepCommand(deps),
		newTaskAssignCommand(deps),
		newTaskNoteCommand(deps),
		newTaskNextCommand(deps),
		newTaskRmCommand(deps),
		statusCommand(deps, "start", core.StatusInProgress, "Mark a task in progress"),
		statusCommand(deps, "review", core.StatusReadyForReview, "Mark a task ready for review"),
		statusCommand(deps, "done", core.StatusDone, "Mark a task done"),
		statusCommand(deps, "reopen", core.StatusTodo, "Return a task to todo"),
		statusCommand(deps, "block", core.StatusBlocked, "Mark a task blocked"),
		statusCommand(deps, "cancel", core.StatusCancelled, "Cancel a task"),
	)
	return cmd
}

func newTaskAddCommand(deps *Deps) *cobra.Command {
	var projectID, repo, kind, title, body, bodyFile, id string
	var priority int
	var labels, depIDs []string

	add := &cobra.Command{
		Use:   "add",
		Short: "Add a task or milestone",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if bodyFile != "" {
				data, err := os.ReadFile(bodyFile)
				if err != nil {
					return fmt.Errorf("read %s: %w", bodyFile, err)
				}
				body = string(data)
			}
			input := app.TaskInput{
				ProjectID:   core.ProjectID(projectID),
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
				{Command: fmt.Sprintf("ft task show %s", task.ID), About: "inspect the task"},
				{Command: fmt.Sprintf("ft task next --project %s", task.ProjectID), About: "see what to start"},
			}
			return deps.emit(task, func() { deps.printf("%s\t%s\t%s\n", task.ID, task.Kind, task.Title) }, hints...)
		},
	}
	add.Flags().StringVar(&projectID, "project", "", "project id (required)")
	add.Flags().StringVar(&id, "id", "", "explicit task id (for imports)")
	add.Flags().StringVar(&repo, "repo", "", "repository name within the project")
	add.Flags().StringVar(&kind, "kind", string(core.KindTask), "task kind: task or milestone")
	add.Flags().StringVar(&title, "title", "", "task title (required)")
	add.Flags().StringVar(&body, "body", "", "task body (description)")
	add.Flags().StringVar(&bodyFile, "body-file", "", "read the task body from a file")
	add.Flags().IntVar(&priority, "priority", 0, "task priority (higher is more important)")
	add.Flags().StringArrayVar(&labels, "label", nil, "label (repeatable)")
	add.Flags().StringArrayVar(&depIDs, "dep", nil, "dependency task id (repeatable)")
	_ = add.MarkFlagRequired("project")
	_ = add.MarkFlagRequired("title")
	return add
}

func newTaskListCommand(deps *Deps) *cobra.Command {
	var projectID, repo string
	var statuses, kinds []string

	list := &cobra.Command{
		Use:   "list",
		Short: "List tasks",
		RunE: func(cmd *cobra.Command, _ []string) error {
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
					rows = append(rows, []string{string(task.ID), task.Repo, string(task.Kind), string(task.Status), task.Title})
				}
				deps.printTable([]string{"ID", "REPO", "KIND", "STATUS", "TITLE"}, rows)
			}, taskListHints(tasks, projectID)...)
		},
	}
	list.Flags().StringVar(&projectID, "project", "", "filter by project id")
	list.Flags().StringVar(&repo, "repo", "", "filter by repository name")
	list.Flags().StringArrayVar(&statuses, "status", nil, "filter by status (repeatable)")
	list.Flags().StringArrayVar(&kinds, "kind", nil, "filter by kind")
	return list
}

func newTaskShowCommand(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "show <task>",
		Short: "Show a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.Get(cmd.Context(), core.TaskID(args[0]))
			if err != nil {
				return err
			}
			return deps.emit(task, func() {
				actors := deps.actorResolver(cmd.Context())
				deps.printf("(%s) %s: %s\n", task.Status, task.ID, task.Title)
				if task.Kind == core.KindMilestone {
					deps.printf("kind: milestone\n")
				}
				if task.Repo != "" {
					deps.printf("repo: %s\n", task.Repo)
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
			}, deps.taskShowHints(cmd.Context(), task)...)
		},
	}
}

func newTaskDepCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "dep", Short: "Manage task dependencies"}
	add := &cobra.Command{
		Use:   "add <task> <depends-on>",
		Short: "Add a dependency (rejects cycles)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := deps.Tasks.AddDep(cmd.Context(), core.TaskID(args[0]), core.TaskID(args[1])); err != nil {
				return err
			}
			deps.suggest(hint{Command: fmt.Sprintf("ft task show %s", args[0]), About: "see the updated graph"})
			return nil
		},
	}
	rm := &cobra.Command{
		Use:   "rm <task> <depends-on>",
		Short: "Remove a dependency",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := deps.Tasks.RemoveDep(cmd.Context(), core.TaskID(args[0]), core.TaskID(args[1])); err != nil {
				return err
			}
			deps.suggest(hint{Command: fmt.Sprintf("ft task show %s", args[0]), About: "see the updated graph"})
			return nil
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
		Args:  cobra.ExactArgs(1),
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
			deps.printf("%s\tassigned\t%v\n", task.ID, task.AssigneeID)
			if task.AssigneeID != nil {
				deps.suggest(hint{Command: fmt.Sprintf("ft task start %s", task.ID), About: "begin work"})
			} else {
				deps.suggest(hint{Command: fmt.Sprintf("ft task next --project %s", task.ProjectID), About: "pick up another task"})
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&actorRef, "actor", "", "actor id or name")
	cmd.Flags().BoolVar(&unassign, "unassign", false, "clear the assignee")
	return cmd
}

func statusCommand(deps *Deps, use string, status core.TaskStatus, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <task>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.SetStatus(cmd.Context(), core.TaskID(args[0]), status)
			if err != nil {
				return err
			}
			deps.printf("%s\t%s\n", task.ID, task.Status)
			deps.suggest(deps.taskShowHints(cmd.Context(), task)...)
			return nil
		},
	}
}

func newTaskNoteCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "note", Short: "Manage task notes"}
	var body string
	var linkSpecs []string

	add := &cobra.Command{
		Use:   "add <task>",
		Short: "Add a note to a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			deps.printf("%s\tnotes=%d\n", task.ID, len(task.Notes))
			deps.suggest(hint{Command: fmt.Sprintf("ft task show %s", task.ID), About: "review the note"})
			return nil
		},
	}
	add.Flags().StringVar(&body, "body", "", "note body (required)")
	add.Flags().StringArrayVar(&linkSpecs, "link", nil, "link kind=url (repeatable)")
	_ = add.MarkFlagRequired("body")

	cmd.AddCommand(add)
	return cmd
}

func newTaskNextCommand(deps *Deps) *cobra.Command {
	var projectID, forRef, repo, toward, rankerName string
	var limit int

	cmd := &cobra.Command{
		Use:   "next",
		Short: "Rank the startable tasks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := app.LoadSnapshot(cmd.Context(), deps.Backend, core.ProjectID(projectID))
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
			return deps.emit(scored, func() {
				rows := make([][]string, 0, len(scored))
				for _, entry := range scored {
					task, _ := snapshot.Graph.Task(entry.TaskID)
					rows = append(rows, []string{fmt.Sprintf("%.2f", entry.Score), string(entry.TaskID), task.Title})
				}
				deps.printTable([]string{"SCORE", "TASK", "TITLE"}, rows)
			}, taskNextHints(projectID, top)...)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "project id (required)")
	cmd.Flags().StringVar(&forRef, "for", "", "restrict to an actor (id or name)")
	cmd.Flags().StringVar(&repo, "repo", "", "restrict to a repository name")
	cmd.Flags().StringVar(&toward, "toward", "", "prefer tasks on the path to this task")
	cmd.Flags().StringVar(&rankerName, "rank", "composite", "ranker: composite, unblock, milestone, or toward")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "maximum number of tasks (0 means all)")
	_ = cmd.MarkFlagRequired("project")
	return cmd
}

func newTaskRmCommand(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <task>",
		Short: "Delete a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := deps.Tasks.Get(cmd.Context(), core.TaskID(args[0]))
			if err != nil {
				return err
			}
			if err := deps.Tasks.Delete(cmd.Context(), task.ID); err != nil {
				return err
			}
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
		Args:  cobra.ExactArgs(1),
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
			return deps.emit(task, func() { deps.printf("%s\t%s\t%s\n", task.ID, task.Status, task.Title) },
				hint{Command: fmt.Sprintf("ft task show %s", task.ID), About: "inspect the updated task"})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "task title")
	cmd.Flags().StringVar(&kind, "kind", "", "task kind: task or milestone")
	cmd.Flags().StringVar(&body, "body", "", "task body (description)")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "read the task body from a file")
	cmd.Flags().IntVar(&priority, "priority", 0, "task priority")
	cmd.Flags().StringVar(&repo, "repo", "", "repository name within the project")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "label (repeatable; replaces existing)")
	return cmd
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
