package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
)

// hint is a suggested follow-up command shown after human-oriented output.
type hint struct {
	Command string
	About   string
}

// suggest writes follow-up commands to stderr. Suggestions are suppressed for
// JSON output and when hints are turned off (--no-hints, FACTOTUM_NO_HINTS, or
// no_hints in the config file).
func (d *Deps) suggest(hints ...hint) {
	if d.NoHints || d.structured() || len(hints) == 0 {
		return
	}
	width := 0
	for _, h := range hints {
		if len(h.Command) > width {
			width = len(h.Command)
		}
	}
	var b strings.Builder
	b.WriteString("\nNext:\n")
	for _, h := range hints {
		if h.About == "" {
			fmt.Fprintf(&b, "  %s\n", h.Command)
			continue
		}
		fmt.Fprintf(&b, "  %-*s  %s\n", width, h.Command, h.About)
	}
	_, _ = fmt.Fprint(d.Err, b.String())
}

// taskGetHints guides the viewer from inspecting a task to acting on it.
func (d *Deps) taskGetHints(ctx context.Context, task *core.Task) []hint {
	id := string(task.ID)
	project := string(task.ProjectID)

	if waiting := d.unresolvedDeps(ctx, task); len(waiting) > 0 {
		hints := make([]hint, 0, len(waiting))
		for _, dep := range waiting {
			hints = append(hints, hint{
				Command: fmt.Sprintf("ft task get %s", dep),
				About:   "inspect this dependency first",
			})
		}
		return hints
	}

	switch task.Status {
	case core.StatusDone, core.StatusCancelled:
		return []hint{{Command: fmt.Sprintf("ft task next --project %s", project), About: "pick up the next task"}}
	case core.StatusReadyForReview:
		return []hint{
			{Command: fmt.Sprintf("ft task note create %s --body \"PR: <url>\" --link pr=<url>", id), About: "attach the PR link"},
			{Command: fmt.Sprintf("ft task done %s", id), About: "accept and finish"},
		}
	case core.StatusInProgress:
		return []hint{
			{Command: fmt.Sprintf("ft task review %s", id), About: "hand off for review"},
			{Command: fmt.Sprintf("ft task note create %s --body \"...\"", id), About: "record progress"},
		}
	case core.StatusBlocked:
		return []hint{
			{Command: fmt.Sprintf("ft task start %s", id), About: "resume once unblocked"},
			{Command: fmt.Sprintf("ft task next --project %s", project), About: "work on something else"},
		}
	default:
		return []hint{
			{Command: fmt.Sprintf("ft task start %s", id), About: "begin work"},
			{Command: fmt.Sprintf("ft task assign %s --actor <actor>", id), About: "claim it (see ft actor list)"},
		}
	}
}

func (d *Deps) unresolvedDeps(ctx context.Context, task *core.Task) []core.TaskID {
	policy := core.DefaultResolutionPolicy()
	var waiting []core.TaskID
	for _, depID := range task.Deps {
		dep, err := d.Tasks.Get(ctx, depID)
		if err != nil || !dep.Resolves(policy) {
			waiting = append(waiting, depID)
		}
	}
	return waiting
}

func eventListHints(projectID, taskID string) []hint {
	var hints []hint
	if taskID != "" {
		hints = append(hints, hint{Command: fmt.Sprintf("ft task get %s", taskID), About: "inspect the task behind these events"})
	}
	if projectID != "" {
		hints = append(hints, hint{Command: fmt.Sprintf("ft task next --project %s", projectID), About: "see what to start"})
	}
	return hints
}

func docListHints(projectID string) []hint {
	var hints []hint
	if projectID != "" {
		hints = append(hints, hint{Command: fmt.Sprintf("ft doc create --project %s --title \"...\"", projectID), About: "add a spec, doc, or memory"})
	}
	return append(hints, hint{Command: `ft doc search "<query>"`, About: "search titles and bodies"})
}

func docSearchHints(projectID string) []hint {
	if projectID == "" {
		return nil
	}
	return []hint{{Command: fmt.Sprintf("ft doc list --project %s", projectID), About: "browse all artifacts"}}
}

func milestoneListHints(projectID string, milestones []*core.Task) []hint {
	var hints []hint
	if projectID != "" {
		hints = append(hints, hint{Command: fmt.Sprintf("ft milestone create --project %s --title \"...\"", projectID), About: "add a milestone"})
	}
	if len(milestones) > 0 {
		hints = append(hints, hint{Command: fmt.Sprintf("ft milestone done %s", milestones[0].ID), About: "mark the first milestone done when met"})
	}
	return hints
}

func actorListHints(actors []*core.Actor) []hint {
	hints := []hint{{Command: `ft actor create "<name>" --kind agent`, About: "register an agent"}}
	if len(actors) > 0 {
		hints = append(hints, hint{Command: fmt.Sprintf("ft task assign <task> --actor %s", actors[0].Name), About: "assign work to the first actor"})
	}
	return hints
}

func projectListHints(projects []*core.Project) []hint {
	if len(projects) == 0 {
		return []hint{{Command: `ft project create "<name>"`, About: "create your first project"}}
	}
	return []hint{
		{Command: fmt.Sprintf("ft project get %s", projects[0].ID), About: "inspect the first project"},
		{Command: `ft project create "<name>"`, About: "create another project"},
	}
}

func projectShowHints(project *core.Project) []hint {
	var hints []hint
	if len(project.Repos) > 0 {
		hints = append(hints, hint{Command: fmt.Sprintf("ft task create --project %s --repo %s --title \"...\"", project.ID, project.Repos[0].Name), About: "add a task in the first repo"})
	} else {
		hints = append(hints, hint{Command: fmt.Sprintf("ft project repo create %s <name>", project.ID), About: "register a repository"})
	}
	return append(hints,
		hint{Command: fmt.Sprintf("ft task next --project %s", project.ID), About: "see what to start"},
		hint{Command: fmt.Sprintf("ft graph render --project %s", project.ID), About: "view the whole graph"},
	)
}

func projectRepoHints(project *core.Project) []hint {
	var hints []hint
	if len(project.Repos) > 0 {
		hints = append(hints, hint{Command: fmt.Sprintf("ft task create --project %s --repo %s --title \"...\"", project.ID, project.Repos[0].Name), About: "add a task in the first repo"})
	} else {
		hints = append(hints, hint{Command: fmt.Sprintf("ft project repo create %s <name>", project.ID), About: "register a repository"})
	}
	return append(hints, hint{Command: fmt.Sprintf("ft project get %s", project.ID), About: "inspect the project"})
}

func taskListHints(tasks []*core.Task, projectID string) []hint {
	project := projectID
	if project == "" && len(tasks) > 0 {
		project = string(tasks[0].ProjectID)
	}
	var hints []hint
	if project != "" {
		hints = append(hints, hint{Command: fmt.Sprintf("ft task next --project %s", project), About: "rank what to start"})
	}
	switch {
	case len(tasks) > 0:
		hints = append(hints, hint{Command: fmt.Sprintf("ft task get %s", tasks[0].ID), About: "inspect the first task"})
	case project != "":
		hints = append(hints, hint{Command: fmt.Sprintf("ft task create --project %s --title \"...\"", project), About: "add a task"})
	}
	return hints
}

func taskNextHints(projectID string, top *core.Task) []hint {
	if top == nil {
		if projectID == "" {
			return []hint{
				{Command: "ft project list", About: "review your projects"},
				{Command: `ft task create --project <project> --title "..."`, About: "add a task"},
			}
		}
		return []hint{
			{Command: fmt.Sprintf("ft task create --project %s --title \"...\"", projectID), About: "add the first task"},
			{Command: fmt.Sprintf("ft task list --project %s", projectID), About: "review the graph"},
		}
	}
	hints := []hint{
		{Command: fmt.Sprintf("ft task get %s", top.ID), About: "understand the task to start"},
		{Command: fmt.Sprintf("ft task start %s", top.ID), About: "begin work"},
	}
	if top.AssigneeID == nil {
		hints = append(hints, hint{Command: fmt.Sprintf("ft task assign %s --actor <actor>", top.ID), About: "claim it (see ft actor list)"})
	}
	return hints
}
