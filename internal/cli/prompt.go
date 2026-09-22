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
// task and reports the created documents.
func (d *Deps) createFromPlan(ctx context.Context, project *core.Project, plan agent.Plan) error {
	if err := validatePlan(project, plan); err != nil {
		return err
	}
	created := make([]taskDoc, 0, len(plan.Tasks))
	tasks := make([]*core.Task, 0, len(plan.Tasks))
	for _, proposed := range plan.Tasks {
		kind := core.TaskKind(proposed.Kind)
		if kind == "" {
			kind = core.KindTask
		}
		task, err := d.Tasks.Add(ctx, app.TaskInput{
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
		tasks = append(tasks, task)
		created = append(created, taskDocFrom(task))
	}
	return d.emit(created, func() {
		for i, task := range tasks {
			if i > 0 {
				d.printf("---\n")
			}
			d.printFields(d.taskFields(task, f("created", true))...)
		}
	})
}

// validatePlan rejects a plan whose tasks could not be created, before any are.
// It reuses core task validation for kind/title and app.CheckRepo for the repo.
func validatePlan(project *core.Project, plan agent.Plan) error {
	for i, proposed := range plan.Tasks {
		kind := core.TaskKind(proposed.Kind)
		if kind == "" {
			kind = core.KindTask
		}
		candidate := core.Task{
			ID:        core.TaskID("proposed"),
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
