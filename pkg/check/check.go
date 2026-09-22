// Package check is the port for advisory task checks. A check judges a task's
// spec and reports a verdict, dimensions, and owned findings; it never mutates
// the task. Checks are registered like rankers and renderers, so a new check is
// a registration, not a new command.
//
// The judgment reads only the task's spec - title, kind, and body - and that read
// set is exactly the set the content hash covers. Notes, labels, and deps are
// history and context, deliberately outside the hash, so a comment never
// invalidates a verdict and a check can never invalidate the result it produced.
package check

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/khoinguyen/factotum/pkg/registry"
)

// Verdict is a check's advisory outcome.
type Verdict string

const (
	// Ready means the check found no gap the implementer must close.
	Ready Verdict = "ready"
	// NeedsGrooming means every finding is agent-owned: the implementer can
	// close it with an edit to the spec.
	NeedsGrooming Verdict = "needs_grooming"
	// NeedsHuman means at least one finding is human-owned: only the author can
	// decide it, so an agent must escalate rather than edit the body.
	NeedsHuman Verdict = "needs_human"
)

// Owner says who can close a finding.
type Owner string

const (
	// OwnerAgent means the implementer can close the finding with a body edit.
	OwnerAgent Owner = "agent"
	// OwnerHuman means only the author can decide it.
	OwnerHuman Owner = "human"
)

// Dimension is one named 0-1 judgment of the spec.
type Dimension struct {
	Name  string  `json:"name" yaml:"name"`
	Value float64 `json:"value" yaml:"value"`
}

// Finding is one concrete gap and its owner. Aspect names the sub-aspect when the
// model named one; it is empty for an unspecified gap that the floor caught.
type Finding struct {
	Dimension string `json:"dimension" yaml:"dimension"`
	Aspect    string `json:"aspect,omitempty" yaml:"aspect,omitempty"`
	Owner     Owner  `json:"owner" yaml:"owner"`
	Edit      string `json:"edit" yaml:"edit"`
}

// Spec is exactly what a check reads: the task's spec fields. A check cannot see
// notes, labels, or deps, so the read set equals the hash set.
type Spec struct {
	ID    string
	Title string
	Kind  string
	Body  string
}

// Hash is the content hash over the spec fields (title, kind, body). The id is
// immutable context and is excluded, so the hash changes precisely when the spec
// changes.
func (s Spec) Hash() string {
	h := sha256.New()
	for _, part := range []string{s.Title, s.Kind, s.Body} {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte(":"))
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Result is one check's outcome. It is stored verbatim as the JSON body of a
// task_check artifact, so its field names are the cache's wire format.
type Result struct {
	Check           string      `json:"check" yaml:"check"`
	CheckVersion    string      `json:"check_version" yaml:"check_version"`
	Model           string      `json:"model,omitempty" yaml:"model,omitempty"`
	ContentHash     string      `json:"content_hash" yaml:"content_hash"`
	Verdict         Verdict     `json:"verdict" yaml:"verdict"`
	Confidence      float64     `json:"confidence" yaml:"confidence"`
	JudgeConfidence float64     `json:"judge_confidence,omitempty" yaml:"judge_confidence,omitempty"`
	Dimensions      []Dimension `json:"dims,omitempty" yaml:"dims,omitempty"`
	Findings        []Finding   `json:"findings,omitempty" yaml:"findings,omitempty"`
	CheckedAt       time.Time   `json:"checked_at,omitempty" yaml:"checked_at,omitempty"`
	// NotesNotConsidered is the count of notes the judgment did not read. Notes
	// are history, not spec, so they are excluded from the hash.
	NotesNotConsidered int `json:"notes_not_considered,omitempty" yaml:"notes_not_considered,omitempty"`
	// Note is the check's advisory note, rendered with the result.
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
	// Override records a human decision: while ContentHash matches, the check
	// reports ready and makes no judge call.
	Override  bool      `json:"override,omitempty" yaml:"override,omitempty"`
	DecidedBy string    `json:"decided_by,omitempty" yaml:"decided_by,omitempty"`
	DecidedAt time.Time `json:"decided_at,omitempty" yaml:"decided_at,omitempty"`
	Reason    string    `json:"reason,omitempty" yaml:"reason,omitempty"`

	// Checked is false when no cached result exists. It is derived, not stored.
	Checked bool `json:"-" yaml:"-"`
	// Stale is true when a cached result's content hash no longer matches the
	// task. It is derived, not stored.
	Stale bool `json:"-" yaml:"-"`
}

// Marshal encodes a result as the cache body.
func (r Result) Marshal() (string, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("marshal check result: %w", err)
	}
	return string(data), nil
}

// Unmarshal decodes a cache body. A malformed body is an error, so a corrupt
// artifact is reported rather than silently treated as unchecked.
func Unmarshal(body string) (Result, error) {
	var result Result
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return Result{}, fmt.Errorf("unmarshal check result: %w", err)
	}
	result.Checked = true
	return result, nil
}

// Check judges one spec. Implementations must not mutate the task and must be
// safe for concurrent use.
type Check interface {
	// Name is the stable check id used by --check and the cache.
	Name() string
	// Version identifies the rubric; a bump invalidates cached results.
	Version() string
	// Run judges the spec. It returns judge.ErrUnavailable when a judge-backed
	// check has no key, so the caller can report that clearly.
	Run(ctx context.Context, spec Spec) (Result, error)
}

// AdvisoryNote is the disclaimer every check rendering carries: a check advises,
// it never gates a transition.
const AdvisoryNote = "advisory; not a gate - the author and the graph decide"

// Registry is the plugin registry for checks.
type Registry = registry.Registry[Check]

// NewRegistry returns an empty check registry.
func NewRegistry() *Registry { return registry.New[Check]() }
