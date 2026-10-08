package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/agent"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

func newPromptCommand(deps *Deps) *cobra.Command {
	var projectID, agentCommand string
	var yes bool

	cmd := &cobra.Command{
		Use:   "prompt <words...>",
		Short: "Break a free-form prompt into tasks",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := deps.resolveProject(projectID)
			if err := requireProject(cmd, project); err != nil {
				return err
			}
			proj, err := deps.Projects.Get(cmd.Context(), project)
			if err != nil {
				return err
			}
			agentRef, err := deps.promptAgent(agentCommand)
			if err != nil {
				return err
			}
			plan, err := agentRef.Breakdown(cmd.Context(), agent.Request{
				Prompt:  strings.Join(args, " "),
				Project: string(project),
			})
			if errors.Is(err, agent.ErrUnavailable) {
				return usageError(cmd, "no agent configured; set [agent] command (or FACTOTUM_AGENT_COMMAND) to an agent CLI")
			}
			if err != nil {
				return err
			}
			if !yes {
				docs := planTaskDocs(project, plan)
				return deps.emit(docs, func() {
					if len(docs) == 0 {
						deps.printf("(no tasks proposed)\n")
						return
					}
					for i, doc := range docs {
						if i > 0 {
							deps.printf("---\n")
						}
						data, err := marshalTaskDoc(doc, "yaml")
						if err != nil {
							continue
						}
						deps.printf("%s", data)
					}
				})
			}
			return deps.createFromPlan(cmd.Context(), proj, plan)
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "target project id (defaults to the configured project)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "create the proposed tasks without prompting")
	cmd.Flags().StringVar(&agentCommand, "agent-command", "", "agent CLI to run instead of the configured one")
	return cmd
}

// promptAgent picks the agent for this invocation: the --agent-command flag wins
// over the configured agent, so a different CLI can be tried without editing config.
// A misconfigured provider is named rather than reported as "no agent configured".
func (d *Deps) promptAgent(command string) (agent.Agent, error) {
	if command != "" {
		return agent.New("command", d.Getenv, map[string]string{"command": command})
	}
	if d.agentErr != nil {
		return nil, d.agentErr
	}
	return d.Agent, nil
}

// createFromPlan validates the whole plan before writing anything, so a single
// bad task from the agent cannot leave a half-applied graph, then creates every
// proposed task that is not already present and reports the created and skipped
// documents. Matching is by normalized title within the project, so re-running
// the same prompt is idempotent: a task whose title already exists (or repeats
// earlier in the same plan) is skipped rather than duplicated.
func (d *Deps) createFromPlan(ctx context.Context, project *core.Project, plan agent.Plan) error {
	if err := validatePlan(project, plan); err != nil {
		return err
	}
	existing, err := d.Tasks.List(ctx, store.TicketFilter{ProjectID: project.ID})
	if err != nil {
		return err
	}
	byTitle := make(map[string]*core.Ticket, len(existing))
	for _, task := range existing {
		byTitle[normalizeTitle(task.Title)] = task
	}
	created := make([]taskDoc, 0, len(plan.Tasks))
	tasks := make([]*core.Ticket, 0, len(plan.Tasks))
	skipped := make([]*core.Ticket, 0, len(plan.Tasks))
	for _, proposed := range plan.Tasks {
		if match, ok := byTitle[normalizeTitle(proposed.Title)]; ok {
			skipped = append(skipped, match)
			continue
		}
		kind := core.TicketKind(proposed.Kind)
		if kind == "" {
			kind = core.KindTask
		}
		task, err := d.Tasks.Add(ctx, app.TicketInput{
			ProjectID:   project.ID,
			Repo:        proposed.Repo,
			Kind:        kind,
			Title:       proposed.Title,
			Description: proposed.Description,
			Priority:    proposed.Priority,
			Labels:      proposed.Labels,
		})
		if err != nil {
			return err
		}
		byTitle[normalizeTitle(task.Title)] = task
		tasks = append(tasks, task)
		created = append(created, taskDocFrom(task))
	}
	if len(skipped) > 0 {
		d.warnf("skipped %d task(s) already present in %s", len(skipped), project.ID)
	}
	return d.emit(created, func() {
		first := true
		for _, task := range tasks {
			if !first {
				d.printf("---\n")
			}
			first = false
			d.printFields(d.taskFields(task, f("created", true))...)
		}
		for _, task := range skipped {
			if !first {
				d.printf("---\n")
			}
			first = false
			d.printFields(d.taskFields(task, f("skipped", true))...)
		}
	})
}

// normalizeTitle is the idempotency key for plan matching: case-insensitive,
// with surrounding whitespace ignored, consistent with the duplicate-title
// warning on `task create`.
func normalizeTitle(title string) string {
	return strings.ToLower(strings.TrimSpace(title))
}

// validatePlan rejects a plan whose tasks could not be created, before any are.
// It reuses core task validation for kind/title and app.CheckRepo for the repo.
func validatePlan(project *core.Project, plan agent.Plan) error {
	for i, proposed := range plan.Tasks {
		kind := core.TicketKind(proposed.Kind)
		if kind == "" {
			kind = core.KindTask
		}
		candidate := core.Ticket{
			ID:        core.TicketID("proposed"),
			ProjectID: project.ID,
			Kind:      kind,
			Title:     proposed.Title,
			Status:    core.StatusTodo,
		}
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("agent plan task %d: %w", i+1, err)
		}
		if err := app.CheckRepo(project, proposed.Repo); err != nil {
			return fmt.Errorf("agent plan task %d: %w", i+1, err)
		}
	}
	return nil
}

// planTaskDocs renders a plan in the task-apply document shape, one document per
// proposed task, so a preview reads like the input `ft task apply` accepts.
func planTaskDocs(project core.ProjectID, plan agent.Plan) []taskDoc {
	projectID := string(project)
	docs := make([]taskDoc, 0, len(plan.Tasks))
	for _, proposed := range plan.Tasks {
		kind := proposed.Kind
		if kind == "" {
			kind = string(core.KindTask)
		}
		status := string(core.StatusTodo)
		title := proposed.Title
		doc := taskDoc{ProjectID: &projectID, Kind: &kind, Title: &title, Status: &status}
		if proposed.Description != "" {
			doc.Description = &proposed.Description
		}
		if proposed.Repo != "" {
			doc.Repo = &proposed.Repo
		}
		if proposed.Priority != 0 {
			priority := proposed.Priority
			doc.Priority = &priority
		}
		if len(proposed.Labels) > 0 {
			labels := append([]string{}, proposed.Labels...)
			doc.Labels = &labels
		}
		docs = append(docs, doc)
	}
	return docs
}
