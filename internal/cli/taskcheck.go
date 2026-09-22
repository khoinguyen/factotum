package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/check"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/registry"
)

// checkResultDoc is the stable document exchanged by `ft task check -o json` and
// the `checks` field of `ft task get`.
type checkResultDoc struct {
	Check           string              `json:"check" yaml:"check"`
	CheckVersion    string              `json:"check_version" yaml:"check_version"`
	Verdict         string              `json:"verdict" yaml:"verdict"`
	Checked         bool                `json:"checked" yaml:"checked"`
	Confidence      float64             `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	JudgeConfidence float64             `json:"judge_confidence,omitempty" yaml:"judge_confidence,omitempty"`
	Dimensions      []checkDimensionDoc `json:"dimensions,omitempty" yaml:"dimensions,omitempty"`
	Findings        []checkFindingDoc   `json:"findings,omitempty" yaml:"findings,omitempty"`
	Stale           bool                `json:"stale,omitempty" yaml:"stale,omitempty"`
	Override        bool                `json:"override,omitempty" yaml:"override,omitempty"`
	DecidedBy       string              `json:"decided_by,omitempty" yaml:"decided_by,omitempty"`
	DecidedAt       *time.Time          `json:"decided_at,omitempty" yaml:"decided_at,omitempty"`
	Reason          string              `json:"reason,omitempty" yaml:"reason,omitempty"`
	// NotesNotConsidered and Advisory make the spec-vs-notes convention visible.
	NotesNotConsidered int    `json:"notes_not_considered,omitempty" yaml:"notes_not_considered,omitempty"`
	Advisory           string `json:"advisory" yaml:"advisory"`
}

type checkDimensionDoc struct {
	Name  string  `json:"name" yaml:"name"`
	Value float64 `json:"value" yaml:"value"`
}

type checkFindingDoc struct {
	Dimension string `json:"dimension" yaml:"dimension"`
	Aspect    string `json:"aspect,omitempty" yaml:"aspect,omitempty"`
	Owner     string `json:"owner" yaml:"owner"`
	Edit      string `json:"edit" yaml:"edit"`
}

func checkResultDocFrom(result check.Result) checkResultDoc {
	doc := checkResultDoc{
		Check:              result.Check,
		CheckVersion:       result.CheckVersion,
		Verdict:            string(result.Verdict),
		Checked:            result.Checked,
		Confidence:         result.Confidence,
		JudgeConfidence:    result.JudgeConfidence,
		Stale:              result.Stale,
		Override:           result.Override,
		DecidedBy:          result.DecidedBy,
		Reason:             result.Reason,
		NotesNotConsidered: result.NotesNotConsidered,
		Advisory:           advisoryOr(result.Note),
	}
	if !result.Checked {
		doc.Verdict = "not_checked"
	}
	if !result.DecidedAt.IsZero() {
		decidedAt := result.DecidedAt
		doc.DecidedAt = &decidedAt
	}
	for _, dimension := range result.Dimensions {
		doc.Dimensions = append(doc.Dimensions, checkDimensionDoc{Name: dimension.Name, Value: dimension.Value})
	}
	for _, finding := range result.Findings {
		doc.Findings = append(doc.Findings, checkFindingDoc{
			Dimension: finding.Dimension,
			Aspect:    finding.Aspect,
			Owner:     string(finding.Owner),
			Edit:      finding.Edit,
		})
	}
	return doc
}

// checkHeadline is the one-line human summary of a result. decidedBy is the
// resolved display name for a human override.
func checkHeadline(result check.Result, decidedBy string) string {
	switch {
	case !result.Checked:
		return "not checked"
	case result.Override && result.Stale:
		return "stale human decision - the spec changed; re-check"
	case result.Override:
		when := ""
		if !result.DecidedAt.IsZero() {
			when = " on " + result.DecidedAt.UTC().Format(time.RFC3339)
		}
		if result.Reason != "" {
			return fmt.Sprintf("ready (decided by %s%s): %s", decidedBy, when, result.Reason)
		}
		return fmt.Sprintf("ready (decided by %s%s)", decidedBy, when)
	case result.Stale:
		return "stale - the spec changed; re-check"
	default:
		return string(result.Verdict)
	}
}

// summarizeChecks renders the `checks` projection value on one line.
func summarizeChecks(docs []checkResultDoc) string {
	parts := make([]string, 0, len(docs))
	for _, doc := range docs {
		verdict := doc.Verdict
		if !doc.Checked {
			verdict = "not checked"
		}
		parts = append(parts, fmt.Sprintf("%s: %s", doc.Check, verdict))
	}
	return strings.Join(parts, "; ")
}

// printCheckReport renders one check result in full.
func (d *Deps) printCheckReport(result check.Result, actors map[core.ActorID]core.Actor) {
	d.printf("%s: %s\n", result.Check, checkHeadline(result, actorName(actors, core.ActorID(result.DecidedBy))))
	if !result.Checked {
		return
	}
	if !result.Override {
		d.printf("  confidence: %.2f\n", result.Confidence)
		if result.JudgeConfidence > 0 {
			d.printf("  judge_confidence: %.2f (advisory)\n", result.JudgeConfidence)
		}
		for _, dimension := range result.Dimensions {
			d.printf("  %s: %.2f\n", dimension.Name, dimension.Value)
		}
	}
	if len(result.Findings) > 0 {
		d.printf("  findings:\n")
		for _, finding := range result.Findings {
			d.printf("    %s: %s\n", finding.Dimension, findingDetail(finding))
		}
	}
	d.printCheckNotes(result)
	d.printf("  %s\n", advisoryOr(result.Note))
}

// printChecksBlock renders the cached checks under a `checks:` header.
func (d *Deps) printChecksBlock(results []check.Result, actors map[core.ActorID]core.Actor) {
	if len(results) == 0 {
		return
	}
	d.printf("checks:\n")
	for _, result := range results {
		d.printf("  %s: %s\n", result.Check, checkHeadline(result, actorName(actors, core.ActorID(result.DecidedBy))))
		for _, finding := range result.Findings {
			d.printf("    %s: %s\n", finding.Dimension, findingDetail(finding))
		}
	}
	for _, result := range results {
		if result.Checked && !result.Override {
			d.printCheckNotes(result)
			break
		}
	}
}

// advisoryOr falls back to the package disclaimer when a result carries none.
func advisoryOr(note string) string {
	if note == "" {
		return check.AdvisoryNote
	}
	return note
}

// findingDetail renders a finding: a body edit for an agent-owned gap, an
// escalation for a human-owned one.
func findingDetail(finding check.Finding) string {
	label := finding.Aspect
	if label == "" {
		label = "unspecified gap"
	}
	if finding.Owner == check.OwnerHuman || finding.Edit == "" {
		return fmt.Sprintf("%s (human-owned) - needs human decision; do not edit the body", label)
	}
	return fmt.Sprintf("%s (agent) - %s", label, finding.Edit)
}

func (d *Deps) printCheckNotes(result check.Result) {
	d.printf("  notes: %d not considered - the body is the spec; fold decisions into the body to change this verdict\n", result.NotesNotConsidered)
}

// noteVerdictHint warns that a note does not change a not-ready verdict, so an
// agent does not loop adding comments. The body is the spec.
func (d *Deps) noteVerdictHint(results []check.Result) {
	for _, result := range results {
		if !result.Checked || result.Override || result.Stale || result.Verdict == check.Ready {
			continue
		}
		if !d.structured() {
			_, _ = fmt.Fprintf(d.Err,
				"ft: note: the body is the spec; this note does not change the %s verdict (%s). Fold decisions into the body to change it.\n",
				result.Check, result.Verdict)
		}
		return
	}
}

func newTaskCheckCommand(deps *Deps) *cobra.Command {
	var checkNames []string
	var force bool

	cmd := &cobra.Command{
		Use:   "check <task>",
		Short: "Run advisory checks on a task and cache the results",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			results, err := deps.TaskChecks.Run(cmd.Context(), core.TaskID(args[0]), checkNames, force)
			if err != nil {
				return checkCommandError(cmd, err)
			}
			docs := make([]checkResultDoc, 0, len(results))
			for _, result := range results {
				docs = append(docs, checkResultDocFrom(result))
			}
			return deps.emit(docs, func() {
				actors := deps.actorResolver(cmd.Context())
				for i, result := range results {
					if i > 0 {
						deps.printf("\n")
					}
					deps.printCheckReport(result, actors)
				}
			})
		},
	}
	cmd.Flags().StringArrayVar(&checkNames, "check", nil, "run only this check (repeatable; default all)")
	cmd.Flags().BoolVar(&force, "force", false, "recompute even when cached")
	return cmd
}

// checkCommandError turns a check failure into a clear message: an unknown check
// is a usage error, an unavailable judge names the missing key.
func checkCommandError(cmd *cobra.Command, err error) error {
	switch {
	case errors.Is(err, registry.ErrNotFound):
		return usageError(cmd, "%s", err)
	case errors.Is(err, judge.ErrUnavailable):
		return fmt.Errorf("%w: no judge configured; set TYPESAFE_API_KEY to run judge-backed checks", judge.ErrUnavailable)
	default:
		return err
	}
}

func newTaskDecideCommand(deps *Deps) *cobra.Command {
	var checkName, reason string
	var ready, clear bool

	cmd := &cobra.Command{
		Use:   "decide <task>",
		Short: "Record or clear a human decision for a task check",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ready == clear {
				return usageError(cmd, "provide exactly one of --ready or --clear")
			}
			if ready && reason == "" {
				return usageError(cmd, "--ready requires --reason")
			}
			actor, err := deps.requireHumanActor(cmd.Context())
			if err != nil {
				return err
			}
			if checkName == "" {
				checkName = "grooming"
			}
			task, err := deps.Tasks.Get(cmd.Context(), core.TaskID(args[0]))
			if err != nil {
				return err
			}
			if _, err := deps.TaskChecks.Decide(cmd.Context(), task.ID, checkName, actor, reason, clear); err != nil {
				if errors.Is(err, registry.ErrNotFound) {
					return usageError(cmd, "%s", err)
				}
				return err
			}
			action := f("decided", true)
			if clear {
				action = f("cleared", true)
			}
			deps.printFields(
				f("task_id", task.ID), action, f("check", checkName),
				f("project", task.ProjectID), f("repo", deps.repoValue(task.Repo)),
			)
			deps.suggest(hint{Command: fmt.Sprintf("ft task get %s", task.ID), About: "review the decision"})
			return nil
		},
	}
	cmd.Flags().BoolVar(&ready, "ready", false, "record a human decision that the task is ready")
	cmd.Flags().BoolVar(&clear, "clear", false, "clear the recorded human decision")
	cmd.Flags().StringVar(&checkName, "check", "", "check to decide (default grooming)")
	cmd.Flags().StringVar(&reason, "reason", "", "why the task is ready (required with --ready)")
	return cmd
}

// requireHumanActor resolves the attributed actor and rejects a non-human one: a
// check decision records a human's call.
func (d *Deps) requireHumanActor(ctx context.Context) (*core.Actor, error) {
	if d.ActorRef == "" {
		return nil, fmt.Errorf("%w: ft task decide requires a human actor; pass --actor <name>", core.ErrInvalid)
	}
	actor, err := d.Actors.Resolve(ctx, d.ActorRef)
	if err != nil {
		return nil, err
	}
	if actor.Kind != core.ActorHuman {
		return nil, fmt.Errorf("%w: ft task decide requires a human actor; %s is an %s", core.ErrInvalid, actor.Name, actor.Kind)
	}
	return actor, nil
}
