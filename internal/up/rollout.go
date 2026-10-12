package up

import (
	"context"

	"github.com/khoinguyen/factotum/pkg/core"
)

// RolloutRunner is the rollout stage. The rollout mechanics (environments,
// deploy, promote, rollback) are owned by t-3p53uzgcve; `ft up` provides the
// stage and the hook. With no rollout configured the stage is a no-op and the
// pipeline goes straight to done (design §9).
type RolloutRunner struct{}

// Run implements StageRunner.
func (RolloutRunner) Run(context.Context, *core.Pipeline) (StageResult, error) {
	return StageResult{Done: true, Detail: "no rollout configured"}, nil
}
