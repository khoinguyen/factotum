package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/khoinguyen/factotum/pkg/check"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/registry"
	"github.com/khoinguyen/factotum/pkg/store"
)

// humanDecidedLabel marks a task whose check a human overrode, so it can be
// filtered.
const humanDecidedLabel = "human-decided"

// CheckService runs advisory checks over a task's spec and caches their results
// as task_check artifacts. check itself is read-only apart from that derived
// cache; recording a human decision is a separate mutation (Decide).
type CheckService struct {
	backend store.Backend
	clock   Clock
	ids     IDGen
	checks  *registry.Registry[check.Check]
}

func NewCheckService(backend store.Backend, clock Clock, ids IDGen, checks *registry.Registry[check.Check]) *CheckService {
	return &CheckService{backend: backend, clock: clock, ids: ids, checks: checks}
}

// Names returns the registered check names in order.
func (s *CheckService) Names() []string {
	if s.checks == nil {
		return nil
	}
	return s.checks.Names()
}

// Run evaluates the named checks (all when names is empty). A fresh cached
// result is reused without running the check, unless force is set; force never
// downgrades a ready verdict for unchanged content, so an agent cannot loop
// against a target it already met. A human override short-circuits the judge
// entirely while its content hash matches.
func (s *CheckService) Run(ctx context.Context, taskID core.TaskID, names []string, force bool) ([]check.Result, error) {
	task, err := s.backend.Tasks().Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	selected, err := s.selected(names)
	if err != nil {
		return nil, err
	}
	spec := specFrom(task)
	results := make([]check.Result, 0, len(selected))
	var errs []error
	for _, c := range selected {
		result, err := s.runOne(ctx, task, spec, c, force)
		if err != nil {
			// Checks are independent: one failing check must not hide the
			// results of the others.
			errs = append(errs, fmt.Errorf("%s: %w", c.Name(), err))
			continue
		}
		results = append(results, result)
	}
	return results, errors.Join(errs...)
}

func (s *CheckService) runOne(ctx context.Context, task *core.Task, spec check.Spec, c check.Check, force bool) (check.Result, error) {
	cached, err := s.latest(ctx, task, c)
	if err != nil {
		return check.Result{}, err
	}
	if cached != nil && cached.result.ContentHash == spec.Hash() {
		// A human decision is terminal; a ready verdict is never downgraded.
		if cached.result.Override || cached.result.Verdict == check.Ready || !force {
			return decorate(cached.result, task), nil
		}
	}
	result, err := c.Run(ctx, spec)
	if err != nil {
		return check.Result{}, err
	}
	result.ContentHash = spec.Hash()
	result.CheckedAt = s.clock.Now()
	result.Checked = true
	if err := s.store(ctx, task, result); err != nil {
		return check.Result{}, err
	}
	return decorate(result, task), nil
}

// Cached returns the cached results without ever running a check or touching the
// network. A result whose content hash no longer matches is marked stale.
func (s *CheckService) Cached(ctx context.Context, taskID core.TaskID, names []string) ([]check.Result, error) {
	task, err := s.backend.Tasks().Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	selected, err := s.selected(names)
	if err != nil {
		return nil, err
	}
	spec := specFrom(task)
	results := make([]check.Result, 0, len(selected))
	for _, c := range selected {
		cached, err := s.latest(ctx, task, c)
		if err != nil {
			return nil, err
		}
		if cached == nil {
			results = append(results, check.Result{Check: c.Name(), CheckVersion: c.Version()})
			continue
		}
		result := decorate(cached.result, task)
		result.Stale = cached.result.ContentHash != spec.Hash()
		results = append(results, result)
	}
	return results, nil
}

// Decide records (or clears) a human decision for a check. It requires a human
// actor, because the override records a human call. Deciding sets the
// human-decided label; clearing removes it and restores the judge verdict.
func (s *CheckService) Decide(ctx context.Context, taskID core.TaskID, checkName string, actor *core.Actor, reason string, clear bool) (check.Result, error) {
	if actor == nil || actor.Kind != core.ActorHuman {
		return check.Result{}, fmt.Errorf("%w: recording a check decision requires a human actor", core.ErrInvalid)
	}
	task, err := s.backend.Tasks().Get(ctx, taskID)
	if err != nil {
		return check.Result{}, err
	}
	c, err := s.checks.MustLookup(checkName)
	if err != nil {
		return check.Result{}, err
	}
	if clear {
		return s.clearDecision(ctx, task, c)
	}
	result := check.Result{
		Check:        c.Name(),
		CheckVersion: c.Version(),
		ContentHash:  specFrom(task).Hash(),
		Verdict:      check.Ready,
		Override:     true,
		DecidedBy:    string(actor.ID),
		DecidedAt:    s.clock.Now(),
		Reason:       reason,
		CheckedAt:    s.clock.Now(),
		Checked:      true,
		Note:         check.AdvisoryNote,
	}
	if err := s.store(ctx, task, result); err != nil {
		return check.Result{}, err
	}
	if err := s.setHumanDecided(ctx, task, true); err != nil {
		return check.Result{}, err
	}
	return decorate(result, task), nil
}

func (s *CheckService) clearDecision(ctx context.Context, task *core.Task, c check.Check) (check.Result, error) {
	// Delete every override, not just the newest: a task may carry several
	// decisions, and clearing must not resurface an older one.
	overrides, err := s.overrides(ctx, task, c)
	if err != nil {
		return check.Result{}, err
	}
	if len(overrides) == 0 {
		return check.Result{}, fmt.Errorf("%w: no human decision to clear for check %q", core.ErrNotFound, c.Name())
	}
	for _, override := range overrides {
		if err := s.backend.Artifacts().Delete(ctx, override.artifact.ID); err != nil {
			return check.Result{}, err
		}
	}
	if err := s.setHumanDecided(ctx, task, false); err != nil {
		return check.Result{}, err
	}
	results, err := s.Cached(ctx, task.ID, []string{c.Name()})
	if err != nil {
		return check.Result{}, err
	}
	return results[0], nil
}

// setHumanDecided adds or removes the human-decided label without touching the
// content the check hashes.
func (s *CheckService) setHumanDecided(ctx context.Context, task *core.Task, decided bool) error {
	labels := make([]string, 0, len(task.Labels)+1)
	for _, label := range task.Labels {
		if label == humanDecidedLabel {
			continue
		}
		labels = append(labels, label)
	}
	if decided {
		labels = append(labels, humanDecidedLabel)
	}
	if equalStrings(labels, task.Labels) {
		return nil
	}
	_, err := NewTaskService(s.backend, s.clock, s.ids).Set(ctx, task.ID, TaskSet{Labels: labels})
	return err
}

// stored is one cached result and the artifact that carries it.
type stored struct {
	artifact *core.Artifact
	result   check.Result
}

// latest returns the newest cached result for a check, or nil when none exists.
// The newest is ordered by creation time, then id, so it is deterministic under
// a fixed clock.
func (s *CheckService) latest(ctx context.Context, task *core.Task, c check.Check) (*stored, error) {
	all, err := s.artifacts(ctx, task)
	if err != nil {
		return nil, err
	}
	var best *stored
	for _, artifact := range all {
		result, err := check.Unmarshal(artifact.Body)
		if err != nil || result.Check != c.Name() || result.CheckVersion != c.Version() {
			continue
		}
		if best == nil || newer(artifact, best.artifact) {
			best = &stored{artifact: artifact, result: result}
		}
	}
	return best, nil
}

// overrides returns every human-decision artifact for a check, newest first.
func (s *CheckService) overrides(ctx context.Context, task *core.Task, c check.Check) ([]*stored, error) {
	all, err := s.artifacts(ctx, task)
	if err != nil {
		return nil, err
	}
	var found []*stored
	for _, artifact := range all {
		result, err := check.Unmarshal(artifact.Body)
		if err != nil || !result.Override || result.Check != c.Name() || result.CheckVersion != c.Version() {
			continue
		}
		found = append(found, &stored{artifact: artifact, result: result})
	}
	sort.Slice(found, func(i, j int) bool { return newer(found[i].artifact, found[j].artifact) })
	return found, nil
}

func (s *CheckService) artifacts(ctx context.Context, task *core.Task) ([]*core.Artifact, error) {
	kind := core.ArtifactTaskCheck
	taskID := task.ID
	return s.backend.Artifacts().List(ctx, store.ArtifactFilter{
		ProjectID: task.ProjectID,
		TaskID:    &taskID,
		Kind:      &kind,
	})
}

func (s *CheckService) store(ctx context.Context, task *core.Task, result check.Result) error {
	body, err := result.Marshal()
	if err != nil {
		return err
	}
	taskID := task.ID
	now := s.clock.Now()
	artifact := &core.Artifact{
		ID:        core.ArtifactID(s.ids.NewID("art")),
		ProjectID: task.ProjectID,
		TaskID:    &taskID,
		Kind:      core.ArtifactTaskCheck,
		Title:     "check: " + result.Check,
		Body:      body,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := artifact.Validate(); err != nil {
		return err
	}
	return s.backend.Artifacts().Create(ctx, artifact)
}

// selected resolves the requested names to checks, defaulting to every
// registered check.
func (s *CheckService) selected(names []string) ([]check.Check, error) {
	if len(names) == 0 {
		names = s.checks.Names()
	}
	selected := make([]check.Check, 0, len(names))
	for _, name := range names {
		c, err := s.checks.MustLookup(name)
		if err != nil {
			return nil, err
		}
		selected = append(selected, c)
	}
	return selected, nil
}

func specFrom(task *core.Task) check.Spec {
	return check.Spec{
		ID:    string(task.ID),
		Title: task.Title,
		Kind:  string(task.Kind),
		Body:  task.Description,
	}
}

// decorate adds the current note count, which is not part of the hash: notes are
// history and are never read by the judgment.
func decorate(result check.Result, task *core.Task) check.Result {
	result.Checked = true
	result.NotesNotConsidered = len(task.Notes)
	return result
}

func newer(candidate, current *core.Artifact) bool {
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return candidate.ID > current.ID
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
