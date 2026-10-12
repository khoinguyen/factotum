package app

import (
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

// TestGateCommand pins the exact operator command that clears each gate, the
// single source the CLI hints and the dashboard's /factory view both surface
// (docs/up/design.md §6). A pipeline with no open gate has no command.
func TestGateCommand(t *testing.T) {
	cases := []struct {
		name   string
		gate   core.GateKind
		want   string
		wantOK bool
	}{
		{"none", core.GateNone, "", false},
		{"groom questions", core.GateGroomQuestions, "ft up answer pl-1 -f answers.md", true},
		{"design rework", core.GateDesignRework, "ft up approve pl-1", true},
		{"merge approval", core.GateMergeApproval, "ft up merge pl-1", true},
		{"release", core.GateRelease, "ft up release pl-1", true},
		{"rollout", core.GateRollout, "ft up promote pl-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command, ok := GateCommand(&core.Pipeline{ID: "pl-1", Gate: tc.gate})
			if ok != tc.wantOK {
				t.Fatalf("GateCommand(%q) ok = %v, want %v", tc.gate, ok, tc.wantOK)
			}
			if command != tc.want {
				t.Fatalf("GateCommand(%q) = %q, want %q", tc.gate, command, tc.want)
			}
		})
	}
}
