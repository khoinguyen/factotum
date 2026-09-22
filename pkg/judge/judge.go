// Package judge is the port for model-backed judgments. Features that need a
// TypeSafe/jev decision depend on this interface, never on the HTTP adapter, so the
// capability is fakeable in tests and disabled without a key.
package judge

import (
	"context"
	"errors"
)

// ErrUnavailable means no judge is configured, for example when there is no API key.
// Callers fall back to their deterministic path.
var ErrUnavailable = errors.New("judge unavailable")

// Kind is the type of a question. These are the judge's own primitives; a provider
// maps them to its wire types (for example TypeSafe's noul/choice/score).
type Kind string

const (
	// KindYesNo asks for the probability that a condition holds.
	KindYesNo Kind = "yes_no"
	// KindChoice selects one option from a set, with a probability for each.
	KindChoice Kind = "choice"
	// KindRating places the answer on an ordered scale.
	KindRating Kind = "rating"
)

// Question is one typed question about the state. Instructions and Criteria accept a
// string, object, or array.
type Question struct {
	Kind         Kind
	Instructions any
	Criteria     any
}

// Request is one evaluation: a state and a named set of questions.
type Request struct {
	State     any
	Questions map[string]Question
}

// Answer is one typed answer. Only the fields matching the question kind are set.
type Answer struct {
	Choice string
	// Probabilities is the distribution over a Choice's options.
	Probabilities map[string]float64
	// Probability is the yes/no result of a KindYesNo question.
	Probability float64
	// Rating is the position on the scale of a KindRating question.
	Rating float64
	// Confidence summarizes how concentrated the answer distribution is.
	Confidence float64
}

// Response carries the answers and the model and token metadata.
type Response struct {
	Model        string
	Answers      map[string]Answer
	InputTokens  int
	OutputTokens int
}

// Judge evaluates a request. Implementations must be safe for concurrent use.
type Judge interface {
	Ask(ctx context.Context, req Request) (Response, error)
}

// Disabled is a judge with no backend. It returns ErrUnavailable so callers fall back
// to their deterministic path.
type Disabled struct{}

func (Disabled) Ask(context.Context, Request) (Response, error) {
	return Response{}, ErrUnavailable
}
