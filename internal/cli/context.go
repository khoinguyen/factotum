package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

type contextDep struct {
	ID     string `json:"id" yaml:"id"`
	Title  string `json:"title" yaml:"title"`
	Status string `json:"status" yaml:"status"`
}

type contextNote struct {
	Author    string    `json:"author,omitempty" yaml:"author,omitempty"`
	Body      string    `json:"body" yaml:"body"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}

type contextArtifact struct {
	ID    string `json:"id" yaml:"id"`
	Title string `json:"title" yaml:"title"`
	Body  string `json:"body,omitempty" yaml:"body,omitempty"`
}

type contextEvent struct {
	Time    string `json:"time" yaml:"time"`
	Kind    string `json:"kind" yaml:"kind"`
	Summary string `json:"summary" yaml:"summary"`
}

// taskContext bundles everything an agent needs to start a task in one call.
type taskContext struct {
	Task   taskDoc           `json:"task" yaml:"task"`
	Deps   []contextDep      `json:"deps,omitempty" yaml:"deps,omitempty"`
	Notes  []contextNote     `json:"notes,omitempty" yaml:"notes,omitempty"`
	Memory []contextArtifact `json:"memory,omitempty" yaml:"memory,omitempty"`
	Events []contextEvent    `json:"events,omitempty" yaml:"events,omitempty"`
}

func newTaskContextCommand(deps *Deps) *cobra.Command {
	var fields string
	var events int

	cmd := &cobra.Command{
		Use:   "context <task>",
		Short: "Bundle a task with its dependencies, notes, memory, and recent events",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			requested := parseFieldList(fields)
			if err := validateFieldList(requested, contextSectionNames); err != nil {
				return usageError(cmd, "%s", err)
			}
			selected := make(map[string]bool, len(requested))
			for _, name := range requested {
				selected[name] = true
			}
			task, err := deps.Tasks.Get(cmd.Context(), core.TaskID(args[0]))
			if err != nil {
				return err
			}
			ctx := taskContext{Task: taskDocFrom(task)}
			if selected["deps"] {
				ctx.Deps = contextDeps(cmd.Context(), deps, task)
			}
			if selected["notes"] {
				for _, note := range task.Notes {
					ctx.Notes = append(ctx.Notes, contextNote{Author: string(note.Author), Body: note.Body, CreatedAt: note.CreatedAt})
				}
			}
			if selected["memory"] {
				kind := core.ArtifactMemory
				artifacts, err := deps.Artifacts.List(cmd.Context(), store.ArtifactFilter{TaskID: &task.ID, Kind: &kind})
				if err != nil {
					return err
				}
				for _, artifact := range artifacts {
					ctx.Memory = append(ctx.Memory, contextArtifact{ID: string(artifact.ID), Title: artifact.Title, Body: artifact.Body})
				}
			}
			if selected["events"] {
				recent, err := deps.Backend.Events().List(cmd.Context(), store.EventFilter{TaskID: &task.ID, Limit: events})
				if err != nil {
					return err
				}
				for _, event := range recent {
					ctx.Events = append(ctx.Events, contextEvent{
						Time:    event.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
						Kind:    string(event.Kind),
						Summary: event.Summary,
					})
				}
			}
			return deps.emit(ctx, func() { printContext(deps, ctx) })
		},
	}
	cmd.Flags().StringVar(&fields, "fields", "task,deps,notes,memory,events", "sections to include (comma-separated)")
	cmd.Flags().IntVarP(&events, "events", "n", 5, "recent events to include")
	return cmd
}

// contextSectionNames are the selectable sections of a task context bundle.
var contextSectionNames = []string{"task", "deps", "notes", "memory", "events"}

func contextDeps(ctx context.Context, deps *Deps, task *core.Task) []contextDep {
	snapshot, err := app.LoadSnapshot(ctx, deps.Backend, task.ProjectID, deps.Clock.Now())
	if err != nil {
		return nil
	}
	var out []contextDep
	for _, depID := range task.Deps {
		if dep, ok := snapshot.Graph.Task(depID); ok {
			out = append(out, contextDep{ID: string(dep.ID), Title: dep.Title, Status: string(dep.Status)})
		}
	}
	return out
}

func printContext(deps *Deps, ctx taskContext) {
	task := ctx.Task
	deps.printFields(
		f("task_id", derefString(task.ID)),
		f("status", derefString(task.Status)),
		f("title", derefString(task.Title)),
		f("project", derefString(task.ProjectID)),
		f("repo", deps.repoValue(derefString(task.Repo))),
	)
	if len(ctx.Deps) > 0 {
		deps.printf("deps:\n")
		for _, dep := range ctx.Deps {
			deps.printf("  %s [%s] %s\n", dep.ID, dep.Status, dep.Title)
		}
	}
	if len(ctx.Notes) > 0 {
		deps.printf("notes:\n")
		for _, note := range ctx.Notes {
			deps.printf("  %s\n", note.Body)
		}
	}
	if len(ctx.Memory) > 0 {
		deps.printf("memory:\n")
		for _, memory := range ctx.Memory {
			deps.printf("  %s: %s\n", memory.ID, memory.Title)
		}
	}
	if len(ctx.Events) > 0 {
		deps.printf("events:\n")
		for _, event := range ctx.Events {
			deps.printf("  %s %s %s\n", event.Time, event.Kind, event.Summary)
		}
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
