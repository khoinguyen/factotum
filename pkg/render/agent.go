package render

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/rank"
)

type Agent struct{}

func (Agent) Format() string { return "agent" }

func (Agent) Render(_ context.Context, w io.Writer, view View) error {
	derived := view.info()
	var b strings.Builder

	fmt.Fprintf(&b, "# %s — task graph snapshot %s\n", view.projectName(), view.snapshotTime())
	stats := derived.stats
	fmt.Fprintf(&b, "stats: scope=%d resolved=%d done=%d ready_agent=%d ready_human=%d dep_blocked=%d blocked=%d cycles=%d waves=%d\n",
		stats.Scope, stats.Resolved, stats.Done, stats.ReadyAgent, stats.ReadyHuman, stats.DepBlocked, stats.Blocked, stats.Cycles, stats.Waves)

	ranked := view.ranked()
	writeReadyGroup(&b, "next agent:", ranked, derived.readyAgent, derived, view)
	writeReadyGroup(&b, "next human:", ranked, derived.readyHuman, derived, view)

	b.WriteString("dep-blocked:\n")
	for _, id := range derived.ids {
		task := derived.tasks[id]
		if view.resolved(task) || derived.readyAgent[id] || derived.readyHuman[id] || derived.cycles[id] {
			continue
		}
		fmt.Fprintf(&b, "  - %s wave=%d blocked_by=[%s]%s%s\n", id, derived.waves[id], strings.Join(view.blockers(id), ", "), repoTag(task.Repo), kindTag(task))
	}

	if cycles := view.Graph.Cycles(); len(cycles) > 0 {
		b.WriteString("cycles:\n")
		for _, cycle := range cycles {
			parts := make([]string, 0, len(cycle)+1)
			for _, id := range cycle {
				parts = append(parts, string(id))
			}
			parts = append(parts, string(cycle[0]))
			fmt.Fprintf(&b, "  - %s\n", strings.Join(parts, " -> "))
		}
	}

	if external := view.Graph.ExternalDeps(); len(external) > 0 {
		parts := make([]string, 0, len(external))
		for _, id := range external {
			parts = append(parts, string(id))
		}
		fmt.Fprintf(&b, "external_deps: [%s]\n", strings.Join(parts, ", "))
	}

	return writeString(w, b.String())
}

func writeReadyGroup(b *strings.Builder, header string, ranked []rank.Scored, members map[core.TaskID]bool, derived *info, view View) {
	b.WriteString(header + "\n")
	for _, scored := range ranked {
		if !members[scored.TaskID] {
			continue
		}
		task := derived.tasks[scored.TaskID]
		fmt.Fprintf(b, "  - %s score=%.2f unblocks=%d wave=%d%s%s %s\n",
			scored.TaskID, scored.Score, view.Graph.UnblockCount(scored.TaskID), derived.waves[scored.TaskID], repoTag(task.Repo), kindTag(task), task.Title)
	}
}

func kindTag(task core.Task) string {
	if task.IsMilestone() {
		return " [milestone]"
	}
	return ""
}

func repoTag(repo string) string {
	if repo == "" {
		return ""
	}
	return " repo=" + repo
}

func (v View) snapshotTime() string {
	if v.Now.IsZero() {
		return "unknown"
	}
	return v.Now.UTC().Format(time.RFC3339)
}

func (v View) blockers(id core.TaskID) []string {
	var out []string
	for _, dep := range v.Graph.Deps(id) {
		task, ok := v.Graph.Task(dep)
		if !ok {
			out = append(out, string(dep)+"?")
			continue
		}
		if !task.Resolves(v.Project.Policy) {
			out = append(out, string(dep))
		}
	}
	sort.Strings(out)
	return out
}
