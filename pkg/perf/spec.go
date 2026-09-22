package perf

import (
	"context"
	"fmt"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// DefaultProjectID is the project every synthetic dataset is seeded under.
const DefaultProjectID core.ProjectID = "perf"

// JSONFileSeedCap is the largest task count the harness seeds into the jsonfile
// backend. jsonfile rewrites its whole document on every mutation and looks up
// ids with a linear scan, so seeding is O(n^2) and the file grows unbounded;
// 1000 keeps the harness usable while 10k takes minutes (see t-ewr3xidxtk).
const JSONFileSeedCap = 1000

// Spec is the shape of a synthetic dataset.
type Spec struct {
	Tasks     int `json:"tasks"`
	Artifacts int `json:"artifacts"`
	Events    int `json:"events"`
}

// SpecFor builds the dataset spec for a task scale. Artifacts and events track
// the documented 1k/10k tiers: they match the task count up to 1k, hold at 1k
// until 10k tasks, and cap at 10k above that.
func SpecFor(tasks int) Spec {
	return Spec{Tasks: tasks, Artifacts: secondaryTier(tasks), Events: secondaryTier(tasks)}
}

// secondaryTier maps a task scale onto the artifact/event tier.
func secondaryTier(tasks int) int {
	switch {
	case tasks <= 1000:
		return tasks
	case tasks < 10000:
		return 1000
	default:
		return 10000
	}
}

// SeedCap returns the maximum task count a backend may be seeded with, or 0
// when the backend has no cap. The harness skips scales above the cap.
func SeedCap(backend string) int {
	if backend == "jsonfile" {
		return JSONFileSeedCap
	}
	return 0
}

// Seed creates the dataset in a fresh project and returns its id. Tasks are
// synthetic and interlinked (every tenth task depends on its predecessor) so
// the graph, readiness, and ranking paths do real work. Artifacts are mostly
// docs with a tenth as task-linked memory; events are task.updated records.
func Seed(ctx context.Context, be store.Backend, spec Spec) (core.ProjectID, error) {
	if spec.Tasks < 0 || spec.Artifacts < 0 || spec.Events < 0 {
		return "", fmt.Errorf("perf: negative spec %+v", spec)
	}
	now := time.Unix(0, 0).UTC()
	project := &core.Project{
		ID:        DefaultProjectID,
		Name:      "Perf Scale",
		Policy:    core.DefaultResolutionPolicy(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := be.Projects().Create(ctx, project); err != nil {
		return "", fmt.Errorf("create project: %w", err)
	}
	for i := 0; i < spec.Tasks; i++ {
		task := syntheticTask(i, spec.Tasks, now)
		if err := be.Tasks().Create(ctx, task); err != nil {
			return "", fmt.Errorf("create task %d: %w", i, err)
		}
	}
	for i := 0; i < spec.Artifacts; i++ {
		if err := be.Artifacts().Create(ctx, syntheticArtifact(i, spec.Tasks, now)); err != nil {
			return "", fmt.Errorf("create artifact %d: %w", i, err)
		}
	}
	for i := 0; i < spec.Events; i++ {
		if err := be.Events().Append(ctx, syntheticEvent(i, spec.Tasks, now)); err != nil {
			return "", fmt.Errorf("append event %d: %w", i, err)
		}
	}
	return DefaultProjectID, nil
}

// TaskID returns the synthetic id for the i-th task.
func TaskID(i int) core.TaskID { return core.TaskID(fmt.Sprintf("t-%06d", i)) }

func syntheticTask(i, total int, now time.Time) *core.Task {
	task := &core.Task{
		ID:          TaskID(i),
		ProjectID:   DefaultProjectID,
		Kind:        core.KindTask,
		Title:       fmt.Sprintf("task %d", i),
		Description: fmt.Sprintf("synthetic task %d body for scale tests", i),
		Status:      core.StatusTodo,
		Priority:    i % 5,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if i > 0 && i%10 == 0 {
		task.Deps = []core.TaskID{TaskID(i - 1)}
	}
	return task
}

func syntheticArtifact(i, total int, now time.Time) *core.Artifact {
	artifact := &core.Artifact{
		ID:        core.ArtifactID(fmt.Sprintf("a-%06d", i)),
		ProjectID: DefaultProjectID,
		Kind:      core.ArtifactDoc,
		Title:     fmt.Sprintf("artifact %d", i),
		Brief:     fmt.Sprintf("synthetic artifact %d brief", i),
		Body:      fmt.Sprintf("synthetic artifact %d body for scale tests", i),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if total > 0 && i%10 == 0 {
		taskID := TaskID(i / 10 % total)
		artifact.Kind = core.ArtifactMemory
		artifact.TaskID = &taskID
	}
	return artifact
}

func syntheticEvent(i, total int, now time.Time) *core.Event {
	event := &core.Event{
		ID:        core.EventID(fmt.Sprintf("e-%06d", i)),
		ProjectID: DefaultProjectID,
		Kind:      core.EventTaskUpdated,
		Summary:   fmt.Sprintf("synthetic event %d", i),
		CreatedAt: now.Add(time.Duration(i) * time.Millisecond),
	}
	if total > 0 {
		taskID := TaskID(i % total)
		event.TaskID = &taskID
	}
	return event
}
