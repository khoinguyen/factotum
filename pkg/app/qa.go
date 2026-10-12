package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// QAVerdict is a feature QA run's outcome. A pass unlocks accepting the capture
// it verifies; a fail records the findings without unlocking anything.
type QAVerdict string

const (
	// QAPass means the QA run verified the feature end to end.
	QAPass QAVerdict = "pass"
	// QAFail means the QA run found the feature wanting; nothing is accepted.
	QAFail QAVerdict = "fail"
)

// Valid reports whether v is a known QA verdict.
func (v QAVerdict) Valid() bool { return v == QAPass || v == QAFail }

// QAService answers the auto-QA loop over captures (ideas and bugs): which
// features have every task resolved and still need QA, and the verdict of each
// QA run. A feature's tasks are the executable tasks that carry the capture as
// their origin edge; the feature is whole when they all resolve under the
// resolution policy.
type QAService struct {
	backend   store.Backend
	clock     Clock
	ids       IDGen
	artifacts *ArtifactService
	messages  *MessageService
}

// NewQAService returns a QA service over backend.
func NewQAService(backend store.Backend, clock Clock, ids IDGen) *QAService {
	return &QAService{
		backend:   backend,
		clock:     clock,
		ids:       ids,
		artifacts: NewArtifactService(backend, clock, ids),
		messages:  NewMessageService(backend, clock, ids),
	}
}

// RemindResolvedCapture prompts the acting caller to dispatch QA when the
// executable task just resolved and thereby completed its origin capture, and
// that capture has no passing QA. It is a no-op when nobody performed the
// transition (no caller to remind), when the task has no origin capture, when
// the capture still has unresolved work, or when it already has a passing QA. It
// returns the sent reminder, or nil when none was due.
func (s *QAService) RemindResolvedCapture(ctx context.Context, task *core.Ticket) (*core.Message, error) {
	actor, _, ok := Caller(ctx)
	if !ok || actor == "" || !task.Kind.Executable() {
		return nil, nil
	}
	capture, err := originTicket(ctx, s.backend, task)
	if err != nil || capture == nil {
		return nil, err
	}
	features, err := featureTasks(ctx, s.backend, capture.ID)
	if err != nil {
		return nil, err
	}
	if len(features) == 0 || !everyResolved(features, featurePolicy()) {
		return nil, nil
	}
	passed, err := featureQAPassed(ctx, s.backend, capture.ID)
	if err != nil {
		return nil, err
	}
	if passed {
		return nil, nil
	}
	body := fmt.Sprintf(
		"QA needed: capture %s (%s) is complete but has no passing QA. Dispatch QA, then record the verdict with `ft qa record %s --verdict pass|fail`; `ft qa ready -p %s` lists what is waiting.",
		capture.ID, capture.Kind, capture.ID, capture.ProjectID)
	return s.messages.Send(ctx, SendMessageInput{
		ProjectID: capture.ProjectID,
		Target:    string(core.ActorAddress(actor)),
		Body:      body,
	})
}

// featurePolicy is the completeness rule for a feature's tasks: QA verifies a
// feature only once every task it produced is finished or deliberately dropped.
// It is stricter than the graph's default resolution policy — which also lets
// ready_for_review unblock dependents — so a feature is never QA'd (or accepted)
// while a task still sits in review with its PR unmerged.
func featurePolicy() core.ResolutionPolicy {
	return core.ResolutionPolicy{
		TaskStatuses:      []core.TicketStatus{core.StatusDone, core.StatusCancelled},
		MilestoneStatuses: []core.TicketStatus{core.StatusDone},
	}
}

// Ready returns the project's captures whose every task has resolved and which
// have not yet passed QA, oldest id first. A capture with no tasks is not ready:
// there is nothing to verify. A capture that failed QA stays ready, so the loop
// re-runs QA after the follow-up work lands.
func (s *QAService) Ready(ctx context.Context, projectID core.ProjectID) ([]*core.Ticket, error) {
	tasks, err := s.backend.Tickets().List(ctx, store.TicketFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	policy := featurePolicy()
	children := featureChildrenByCapture(tasks)
	var ready []*core.Ticket
	for _, capture := range tasks {
		if !capture.Kind.CapturedByHuman() {
			continue
		}
		kids := children[capture.ID]
		if len(kids) == 0 || !everyResolved(kids, policy) {
			continue
		}
		passed, err := s.Passed(ctx, capture)
		if err != nil {
			return nil, err
		}
		if passed {
			continue
		}
		ready = append(ready, capture)
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].ID < ready[j].ID })
	return ready, nil
}

// Passed reports whether the capture has a recorded passing QA verdict.
func (s *QAService) Passed(ctx context.Context, capture *core.Ticket) (bool, error) {
	return featureQAPassed(ctx, s.backend, capture.ID)
}

// Record stores a QA run's verdict on the capture. A pass requires every task
// resolved, so a feature cannot pass QA with work still open; a fail is always
// recorded, keeping the findings as the audit trail for the follow-up work.
func (s *QAService) Record(ctx context.Context, captureID core.TicketID, verdict QAVerdict, findings string) (*core.Artifact, error) {
	if !verdict.Valid() {
		return nil, fmt.Errorf("%w: unknown qa verdict %q", core.ErrInvalid, verdict)
	}
	capture, err := s.backend.Tickets().Get(ctx, captureID)
	if err != nil {
		return nil, err
	}
	if !capture.Kind.CapturedByHuman() {
		return nil, fmt.Errorf("%w: qa record %s: not a capture", core.ErrInvalid, captureID)
	}
	if verdict == QAPass {
		if err := requireEveryTaskResolved(ctx, s.backend, captureID); err != nil {
			return nil, err
		}
	}
	artifact, err := s.artifacts.Add(ctx, ArtifactInput{
		ProjectID: capture.ProjectID,
		TicketID:  &captureID,
		Kind:      core.ArtifactQA,
		Title:     fmt.Sprintf("QA %s %s", verdict, captureID),
		Brief:     string(verdict),
		Body:      findings,
	})
	if err != nil {
		return nil, err
	}
	return artifact, nil
}

// featureChildrenByCapture groups the project's executable tasks by the capture
// each depends on (its origin edge).
func featureChildrenByCapture(tasks []*core.Ticket) map[core.TicketID][]*core.Ticket {
	children := make(map[core.TicketID][]*core.Ticket)
	for _, task := range tasks {
		if !task.Kind.Executable() {
			continue
		}
		for _, dep := range task.Deps {
			children[dep] = append(children[dep], task)
		}
	}
	return children
}

// everyResolved reports whether every task resolves under the policy.
func everyResolved(tasks []*core.Ticket, policy core.ResolutionPolicy) bool {
	for _, task := range tasks {
		if !task.Resolves(policy) {
			return false
		}
	}
	return true
}

// featureTasks returns the executable tasks whose origin edge is captureID.
func featureTasks(ctx context.Context, backend store.Backend, captureID core.TicketID) ([]*core.Ticket, error) {
	dependsOn := captureID
	tasks, err := backend.Tickets().List(ctx, store.TicketFilter{DependsOn: &dependsOn})
	if err != nil {
		return nil, err
	}
	var features []*core.Ticket
	for _, task := range tasks {
		if task.Kind.Executable() {
			features = append(features, task)
		}
	}
	return features, nil
}

// requireEveryTaskResolved errors when any task of the capture has not resolved.
func requireEveryTaskResolved(ctx context.Context, backend store.Backend, captureID core.TicketID) error {
	features, err := featureTasks(ctx, backend, captureID)
	if err != nil {
		return err
	}
	if len(features) == 0 {
		return fmt.Errorf("%w: qa record %s: it has no tasks to verify", core.ErrInvalid, captureID)
	}
	policy := featurePolicy()
	for _, task := range features {
		if !task.Resolves(policy) {
			return fmt.Errorf("%w: qa record %s: task %s is not done; every task must be done before QA passes",
				core.ErrConflict, captureID, task.ID)
		}
	}
	return nil
}

// featureQAPassed reports whether the capture's latest QA verdict is a pass that
// still verifies the current work. The verdict with the greatest CreatedAt wins
// (ties break on artifact id), so a later fail revokes an earlier pass; and the
// pass must not predate the newest update to any task it verified, so a task
// reopened (and redone) after the pass makes it stale and re-gates the feature.
func featureQAPassed(ctx context.Context, backend store.Backend, captureID core.TicketID) (bool, error) {
	verdict, ok, err := latestQA(ctx, backend, captureID)
	if err != nil || !ok {
		return false, err
	}
	if verdict.Brief != string(QAPass) {
		return false, nil
	}
	features, err := featureTasks(ctx, backend, captureID)
	if err != nil {
		return false, err
	}
	for _, task := range features {
		if task.UpdatedAt.After(verdict.CreatedAt) {
			return false, nil
		}
	}
	return true, nil
}

// latestQA returns the capture's most recently recorded QA verdict. Ordering is
// by CreatedAt, with the artifact id as a deterministic tie-break.
func latestQA(ctx context.Context, backend store.Backend, captureID core.TicketID) (*core.Artifact, bool, error) {
	kind := core.ArtifactQA
	artifacts, err := backend.Artifacts().List(ctx, store.ArtifactFilter{TicketID: &captureID, Kind: &kind})
	if err != nil {
		return nil, false, err
	}
	var latest *core.Artifact
	for _, artifact := range artifacts {
		switch {
		case latest == nil:
			latest = artifact
		case artifact.CreatedAt.After(latest.CreatedAt):
			latest = artifact
		case artifact.CreatedAt.Equal(latest.CreatedAt) && artifact.ID > latest.ID:
			latest = artifact
		}
	}
	return latest, latest != nil, nil
}

// captureAcceptGate enforces the auto-QA gate: a capture with tasks cannot be
// accepted (set done) until every task is done and QA has passed. A capture with
// no tasks has nothing to verify, so it is accepted freely.
func captureAcceptGate(ctx context.Context, backend store.Backend, capture *core.Ticket) error {
	features, err := featureTasks(ctx, backend, capture.ID)
	if err != nil {
		return err
	}
	if len(features) == 0 {
		return nil
	}
	policy := featurePolicy()
	for _, task := range features {
		if !task.Resolves(policy) {
			return fmt.Errorf("%w: cannot accept %s %s: task %s is not done",
				core.ErrConflict, capture.Kind, capture.ID, task.ID)
		}
	}
	passed, err := featureQAPassed(ctx, backend, capture.ID)
	if err != nil {
		return err
	}
	if !passed {
		return fmt.Errorf("%w: cannot accept %s %s without a passing QA; run `ft qa record %s --verdict pass`",
			core.ErrConflict, capture.Kind, capture.ID, capture.ID)
	}
	return nil
}
