// Package clone deep-copies core entities so storage adapters never share
// mutable memory with callers.
package clone

import "github.com/khoinguyen/factotum/pkg/core"

func Policy(policy core.ResolutionPolicy) core.ResolutionPolicy {
	policy.TaskStatuses = append([]core.TaskStatus(nil), policy.TaskStatuses...)
	policy.MilestoneStatuses = append([]core.TaskStatus(nil), policy.MilestoneStatuses...)
	return policy
}

func Project(project core.Project) core.Project {
	project.Repos = append([]core.Repository(nil), project.Repos...)
	project.Policy = Policy(project.Policy)
	return project
}

func Task(task core.Task) core.Task {
	if task.AssigneeID != nil {
		id := *task.AssigneeID
		task.AssigneeID = &id
	}
	task.WaitingOn = append([]core.ActorID(nil), task.WaitingOn...)
	task.Labels = append([]string(nil), task.Labels...)
	task.Deps = append([]core.TaskID(nil), task.Deps...)
	task.Notes = notes(task.Notes)
	if task.Milestone != nil {
		meta := *task.Milestone
		if meta.TargetDate != nil {
			target := *meta.TargetDate
			meta.TargetDate = &target
		}
		task.Milestone = &meta
	}
	return task
}

func Actor(actor core.Actor) core.Actor {
	return actor
}

func Artifact(artifact core.Artifact) core.Artifact {
	if artifact.TaskID != nil {
		id := *artifact.TaskID
		artifact.TaskID = &id
	}
	artifact.Links = append([]core.Link(nil), artifact.Links...)
	return artifact
}

func Event(event core.Event) core.Event {
	if event.TaskID != nil {
		id := *event.TaskID
		event.TaskID = &id
	}
	if event.By != nil {
		id := *event.By
		event.By = &id
	}
	if event.Data != nil {
		data := make(map[string]any, len(event.Data))
		for key, value := range event.Data {
			data[key] = value
		}
		event.Data = data
	}
	if event.Tally != nil {
		tally := *event.Tally
		event.Tally = &tally
	}
	return event
}

func notes(notes []core.Note) []core.Note {
	if notes == nil {
		return nil
	}
	out := make([]core.Note, len(notes))
	for i, note := range notes {
		note.Links = append([]core.Link(nil), note.Links...)
		out[i] = note
	}
	return out
}
