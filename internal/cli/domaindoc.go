package cli

import (
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

// actorDoc is the stable, snake_case document every actor verb emits. Like the
// task document it carries no Go field name, so one parser reads `actor create`
// and `actor list`.
type actorDoc struct {
	ID        string    `json:"id" yaml:"id"`
	Kind      string    `json:"kind" yaml:"kind"`
	Name      string    `json:"name" yaml:"name"`
	Active    bool      `json:"active" yaml:"active"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}

func actorDocFrom(actor *core.Actor) actorDoc {
	return actorDoc{
		ID:        string(actor.ID),
		Kind:      string(actor.Kind),
		Name:      actor.Name,
		Active:    actor.Active,
		CreatedAt: actor.CreatedAt,
	}
}

// actorDocsFrom renders a list of actors as the stable list document.
func actorDocsFrom(actors []*core.Actor) []actorDoc {
	docs := make([]actorDoc, 0, len(actors))
	for _, actor := range actors {
		docs = append(docs, actorDocFrom(actor))
	}
	return docs
}

// repoDoc is one repository inside a project document. It is also the document
// `project repo list` emits on its own.
type repoDoc struct {
	Name        string `json:"name" yaml:"name"`
	URL         string `json:"url" yaml:"url"`
	Path        string `json:"path" yaml:"path"`
	Description string `json:"description" yaml:"description"`
}

func repoDocFrom(repo core.Repository) repoDoc {
	return repoDoc{Name: repo.Name, URL: repo.URL, Path: repo.Path, Description: repo.Description}
}

func repoDocsFrom(repos []core.Repository) []repoDoc {
	docs := make([]repoDoc, 0, len(repos))
	for _, repo := range repos {
		docs = append(docs, repoDocFrom(repo))
	}
	return docs
}

// policyDoc is a project's resolution policy, so the structured document stays
// lossless while spelling every status under a snake_case key.
type policyDoc struct {
	TaskStatuses      []string `json:"task_statuses" yaml:"task_statuses"`
	MilestoneStatuses []string `json:"milestone_statuses" yaml:"milestone_statuses"`
}

func policyDocFrom(policy core.ResolutionPolicy) policyDoc {
	doc := policyDoc{
		TaskStatuses:      make([]string, 0, len(policy.TaskStatuses)),
		MilestoneStatuses: make([]string, 0, len(policy.MilestoneStatuses)),
	}
	for _, status := range policy.TaskStatuses {
		doc.TaskStatuses = append(doc.TaskStatuses, string(status))
	}
	for _, status := range policy.MilestoneStatuses {
		doc.MilestoneStatuses = append(doc.MilestoneStatuses, string(status))
	}
	return doc
}

// projectDoc is the stable, snake_case document every project verb emits.
type projectDoc struct {
	ID          string    `json:"id" yaml:"id"`
	Name        string    `json:"name" yaml:"name"`
	Description string    `json:"description" yaml:"description"`
	Repos       []repoDoc `json:"repos" yaml:"repos"`
	Policy      policyDoc `json:"policy" yaml:"policy"`
	CreatedAt   time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" yaml:"updated_at"`
}

func projectDocFrom(project *core.Project) projectDoc {
	return projectDoc{
		ID:          string(project.ID),
		Name:        project.Name,
		Description: project.Description,
		Repos:       repoDocsFrom(project.Repos),
		Policy:      policyDocFrom(project.Policy),
		CreatedAt:   project.CreatedAt,
		UpdatedAt:   project.UpdatedAt,
	}
}

// projectDocsFrom renders a list of projects as the stable list document.
func projectDocsFrom(projects []*core.Project) []projectDoc {
	docs := make([]projectDoc, 0, len(projects))
	for _, project := range projects {
		docs = append(docs, projectDocFrom(project))
	}
	return docs
}

// tallyDoc is the graph tally an event may carry.
type tallyDoc struct {
	Scope      int `json:"scope" yaml:"scope"`
	Done       int `json:"done" yaml:"done"`
	ReadyAgent int `json:"ready_agent" yaml:"ready_agent"`
	ReadyHuman int `json:"ready_human" yaml:"ready_human"`
	Blocked    int `json:"blocked" yaml:"blocked"`
	Cycles     int `json:"cycles" yaml:"cycles"`
	Waves      int `json:"waves" yaml:"waves"`
}

func tallyDocFrom(tally *core.Tally) *tallyDoc {
	if tally == nil {
		return nil
	}
	return &tallyDoc{
		Scope:      tally.Scope,
		Done:       tally.Done,
		ReadyAgent: tally.ReadyAgent,
		ReadyHuman: tally.ReadyHuman,
		Blocked:    tally.Blocked,
		Cycles:     tally.Cycles,
		Waves:      tally.Waves,
	}
}

// eventDoc is the stable, snake_case audit-log document. The stored event keeps
// its legacy TaskID key; the CLI spells the link task_id like every other
// machine document.
type eventDoc struct {
	ID        string         `json:"id" yaml:"id"`
	ProjectID string         `json:"project_id" yaml:"project_id"`
	TaskID    string         `json:"task_id,omitempty" yaml:"task_id,omitempty"`
	Kind      string         `json:"kind" yaml:"kind"`
	By        string         `json:"by,omitempty" yaml:"by,omitempty"`
	Session   string         `json:"session,omitempty" yaml:"session,omitempty"`
	Summary   string         `json:"summary" yaml:"summary"`
	Data      map[string]any `json:"data,omitempty" yaml:"data,omitempty"`
	Tally     *tallyDoc      `json:"tally,omitempty" yaml:"tally,omitempty"`
	CreatedAt time.Time      `json:"created_at" yaml:"created_at"`
}

func eventDocFrom(event *core.Event) eventDoc {
	doc := eventDoc{
		ID:        string(event.ID),
		ProjectID: string(event.ProjectID),
		Kind:      string(event.Kind),
		Summary:   event.Summary,
		Data:      event.Data,
		Tally:     tallyDocFrom(event.Tally),
		CreatedAt: event.CreatedAt,
	}
	if event.TicketID != nil {
		doc.TaskID = string(*event.TicketID)
	}
	if event.By != nil {
		doc.By = string(*event.By)
	}
	doc.Session = event.Session
	return doc
}

// eventDocsFrom renders a list of events as the stable list document.
func eventDocsFrom(events []*core.Event) []eventDoc {
	docs := make([]eventDoc, 0, len(events))
	for _, event := range events {
		docs = append(docs, eventDocFrom(event))
	}
	return docs
}
