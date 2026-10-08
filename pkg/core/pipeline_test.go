package core

import (
	"errors"
	"testing"
)

func TestPipelineStateValid(t *testing.T) {
	valid := []PipelineState{
		PipelineQueued, PipelineGrooming, PipelineReview, PipelineBuilding,
		PipelineQA, PipelineRollout, PipelineDone, PipelineCancelled,
		PipelineFailed, PipelinePaused,
	}
	for _, state := range valid {
		if !state.Valid() {
			t.Errorf("PipelineState(%q).Valid() = false, want true", state)
		}
	}
	for _, state := range []PipelineState{"", "unknown", "DONE"} {
		if state.Valid() {
			t.Errorf("PipelineState(%q).Valid() = true, want false", state)
		}
	}
}

func TestPipelineStateTerminal(t *testing.T) {
	// done and cancelled are immutable; failed and paused stay reversible.
	terminal := []PipelineState{PipelineDone, PipelineCancelled}
	for _, state := range terminal {
		if !state.Terminal() {
			t.Errorf("PipelineState(%q).Terminal() = false, want true", state)
		}
	}
	for _, state := range []PipelineState{PipelineQueued, PipelineGrooming, PipelineReview, PipelineBuilding, PipelineQA, PipelineRollout, PipelineFailed, PipelinePaused} {
		if state.Terminal() {
			t.Errorf("PipelineState(%q).Terminal() = true, want false", state)
		}
	}
}

func TestGateKindValid(t *testing.T) {
	valid := []GateKind{
		GateNone, GateGroomQuestions, GateDesignRework, GateMergeApproval,
		GateRelease, GateRollout,
	}
	for _, gate := range valid {
		if !gate.Valid() {
			t.Errorf("GateKind(%q).Valid() = false, want true", gate)
		}
	}
	for _, gate := range []GateKind{"unknown", "MERGE_APPROVAL"} {
		if gate.Valid() {
			t.Errorf("GateKind(%q).Valid() = true, want false", gate)
		}
	}
}

func TestPipelineValidate(t *testing.T) {
	capture := TicketID("i-1")
	valid := Pipeline{
		ID:        "pl-1",
		ProjectID: "prj-1",
		CaptureID: capture,
		State:     PipelineQueued,
		Gate:      GateNone,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(valid) error = %v, want nil", err)
	}

	cases := []struct {
		name     string
		pipeline Pipeline
	}{
		{"empty id", Pipeline{ProjectID: "prj-1", CaptureID: capture, State: PipelineQueued}},
		{"empty project", Pipeline{ID: "pl-1", CaptureID: capture, State: PipelineQueued}},
		{"empty capture", Pipeline{ID: "pl-1", ProjectID: "prj-1", State: PipelineQueued}},
		{"unknown state", Pipeline{ID: "pl-1", ProjectID: "prj-1", CaptureID: capture, State: "lost"}},
		{"unknown gate", Pipeline{ID: "pl-1", ProjectID: "prj-1", CaptureID: capture, State: PipelineQueued, Gate: "bad"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.pipeline.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}
