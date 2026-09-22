package app

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

func memoryNote(id, title, brief string) *core.Artifact {
	return &core.Artifact{ID: core.ArtifactID(id), ProjectID: "prj", Kind: core.ArtifactMemory, Title: title, Brief: brief, Body: "body of " + title}
}

func TestMemoryRelationSupersedes(t *testing.T) {
	candidate := memoryNote("art-1", "Deploy notes", "old steps")
	written := memoryNote("art-2", "Deploy runbook", "new steps")
	f := fake.New(map[string]judge.Answer{
		"rel::art-1": {Rating: 2, Confidence: 0.9},
	})
	rel, err := NewMemoryRelationService(f).Check(context.Background(), written, []*core.Artifact{candidate})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if rel.Action != "supersedes" || rel.Candidate == nil || rel.Candidate.ID != "art-1" {
		t.Fatalf("relation = %+v, want supersedes art-1", rel)
	}
}

func TestMemoryRelationRelated(t *testing.T) {
	candidate := memoryNote("art-1", "Deploy notes", "old")
	written := memoryNote("art-2", "Deploy checklist", "new")
	f := fake.New(map[string]judge.Answer{
		"rel::art-1": {Rating: 1, Confidence: 0.8},
	})
	rel, err := NewMemoryRelationService(f).Check(context.Background(), written, []*core.Artifact{candidate})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if rel.Action != "related" {
		t.Fatalf("relation = %+v, want related", rel)
	}
}

func TestMemoryRelationUnrelated(t *testing.T) {
	candidate := memoryNote("art-1", "Deploy notes", "old")
	written := memoryNote("art-2", "Terraform tips", "new")
	f := fake.New(map[string]judge.Answer{
		"rel::art-1": {Rating: 0.1, Confidence: 0.9},
	})
	rel, err := NewMemoryRelationService(f).Check(context.Background(), written, []*core.Artifact{candidate})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if rel.Action != "unrelated" {
		t.Fatalf("relation = %+v, want unrelated", rel)
	}
}

func TestMemoryRelationLowConfidenceIsUnrelated(t *testing.T) {
	candidate := memoryNote("art-1", "Deploy notes", "old")
	written := memoryNote("art-2", "Deploy runbook", "new")
	f := fake.New(map[string]judge.Answer{
		"rel::art-1": {Rating: 2, Confidence: 0.1},
	})
	rel, err := NewMemoryRelationService(f).Check(context.Background(), written, []*core.Artifact{candidate})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if rel.Action != "unrelated" {
		t.Fatalf("relation = %+v, want unrelated when confidence is low", rel)
	}
}

func TestMemoryRelationNoCandidates(t *testing.T) {
	rel, err := NewMemoryRelationService(judge.Disabled{}).Check(context.Background(), memoryNote("art-2", "x", "y"), nil)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if rel.Action != "unrelated" {
		t.Fatalf("relation = %+v, want unrelated with no candidates", rel)
	}
}

func TestMemoryRelationUnavailable(t *testing.T) {
	candidate := memoryNote("art-1", "Deploy notes", "old")
	_, err := NewMemoryRelationService(judge.Disabled{}).Check(context.Background(), memoryNote("art-2", "x", "y"), []*core.Artifact{candidate})
	if !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("Check() error = %v, want judge.ErrUnavailable", err)
	}
}
