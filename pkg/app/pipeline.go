package app

import (
	"fmt"

	"github.com/khoinguyen/factotum/pkg/core"
)

// GateCommand returns the exact operator command that clears p's open gate, and
// false when no gate is open. It is the single source for the gate-clearing
// commands, so the CLI hint (`ft up status`) and the dashboard's /factory view
// show the same command and cannot drift (docs/up/design.md §6).
func GateCommand(p *core.Pipeline) (string, bool) {
	switch p.Gate {
	case core.GateGroomQuestions:
		return fmt.Sprintf("ft up answer %s -f answers.md", p.ID), true
	case core.GateDesignRework:
		return fmt.Sprintf("ft up approve %s", p.ID), true
	case core.GateMergeApproval:
		return fmt.Sprintf("ft up merge %s", p.ID), true
	case core.GateRelease:
		return fmt.Sprintf("ft up release %s", p.ID), true
	case core.GateRollout:
		return fmt.Sprintf("ft up promote %s", p.ID), true
	}
	return "", false
}
