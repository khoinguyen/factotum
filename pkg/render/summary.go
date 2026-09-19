package render

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
)

// Summary is a compact, timestamp-free overview for cheap orientation: an
// agent can decide its next action without a full graph snapshot.
type Summary struct{}

func (Summary) Format() string { return "summary" }

func (Summary) Render(_ context.Context, w io.Writer, view View) error {
	derived := view.info()

	counts := map[core.TaskStatus]int{}
	depBlocked := 0
	for _, id := range derived.ids {
		task := derived.tasks[id]
		counts[task.Status]++
		if task.Resolves(view.Project.Policy) || derived.readyAgent[id] || derived.readyHuman[id] || derived.cycles[id] {
			continue
		}
		depBlocked++
	}

	var b strings.Builder
	fmt.Fprintf(&b, "scope=%d todo=%d in_progress=%d blocked=%d review=%d done=%d cancelled=%d\n",
		derived.stats.Scope,
		counts[core.StatusTodo],
		counts[core.StatusInProgress],
		counts[core.StatusBlocked],
		counts[core.StatusReadyForReview],
		counts[core.StatusDone],
		counts[core.StatusCancelled],
	)
	fmt.Fprintf(&b, "ready_agent=%d ready_human=%d dep_blocked=%d cycles=%d\n",
		derived.stats.ReadyAgent, derived.stats.ReadyHuman, depBlocked, derived.stats.Cycles)

	if ranked := view.ranked(); len(ranked) > 0 {
		parts := make([]string, 0, 3)
		for i, scored := range ranked {
			if i == 3 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s %s", scored.TaskID, derived.tasks[scored.TaskID].Title))
		}
		fmt.Fprintf(&b, "next: %s\n", strings.Join(parts, " | "))
	}
	return writeString(w, b.String())
}
