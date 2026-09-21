package app

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

func dupTask(id, title string) *core.Task {
	return &core.Task{ID: core.TaskID(id), Title: title, Description: "body of " + title}
}

func TestDuplicateCheckFlagsRelatedTask(t *testing.T) {
	task := dupTask("t-new", "Warn when a task title already exists")
	candidate := dupTask("t-old", "Duplicate-title warning on task create")
	f := fake.New(map[string]judge.Answer{
		"link::t-old": {Rating: 0.9, Confidence: 0.7},
		"same::t-old": {Probability: 0.6},
	})
	verdict, err := NewDuplicateService(f).Check(context.Background(), task, []*core.Task{candidate})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if verdict.Action != "flag" || verdict.Candidate == nil || verdict.Candidate.ID != "t-old" {
		t.Fatalf("verdict = %+v, want flag on t-old", verdict)
	}
}

func TestDuplicateCheckLinksSameTask(t *testing.T) {
	task := dupTask("t-new", "Snooze until a task resolves")
	candidate := dupTask("t-old", "Defer a task until another task resolves")
	f := fake.New(map[string]judge.Answer{
		"link::t-old": {Rating: 1.9, Confidence: 0.8},
		"same::t-old": {Probability: 0.9},
	})
	verdict, err := NewDuplicateService(f).Check(context.Background(), task, []*core.Task{candidate})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if verdict.Action != "link" {
		t.Fatalf("verdict.Action = %q, want link", verdict.Action)
	}
}

func TestDuplicateCheckIgnoresDistinctTask(t *testing.T) {
	task := dupTask("t-new", "Add a flag")
	candidate := dupTask("t-old", "Benchmark the hot paths")
	f := fake.New(map[string]judge.Answer{
		"link::t-old": {Rating: 0.1, Confidence: 0.9},
		"same::t-old": {Probability: 0.1},
	})
	verdict, err := NewDuplicateService(f).Check(context.Background(), task, []*core.Task{candidate})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if verdict.Action != "none" {
		t.Fatalf("verdict.Action = %q, want none", verdict.Action)
	}
}

func TestDuplicateCheckStaysSilentWhenUnsure(t *testing.T) {
	task := dupTask("t-new", "Add a flag")
	candidate := dupTask("t-old", "Add a different flag")
	f := fake.New(map[string]judge.Answer{
		"link::t-old": {Rating: 1.0, Confidence: 0.2},
		"same::t-old": {Probability: 0.5},
	})
	verdict, err := NewDuplicateService(f).Check(context.Background(), task, []*core.Task{candidate})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if verdict.Action != "none" {
		t.Fatalf("verdict.Action = %q, want none when unsure", verdict.Action)
	}
}
