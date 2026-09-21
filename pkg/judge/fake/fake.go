// Package fake is a deterministic judge for tests. It never touches the network.
package fake

import (
	"context"

	"github.com/khoinguyen/factotum/pkg/judge"
)

// Judge returns programmed answers and records every request it receives.
type Judge struct {
	answers  map[string]judge.Answer
	requests []judge.Request
	err      error
}

// New returns a fake that answers with the given map, keyed by question id.
func New(answers map[string]judge.Answer) *Judge {
	return &Judge{answers: answers}
}

// FailWith makes every Ask return err, so callers can exercise their fallback path.
func (f *Judge) FailWith(err error) { f.err = err }

// Requests returns the requests recorded so far, in order.
func (f *Judge) Requests() []judge.Request { return f.requests }

func (f *Judge) Ask(_ context.Context, req judge.Request) (judge.Response, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return judge.Response{}, f.err
	}
	out := make(map[string]judge.Answer, len(req.Questions))
	for id := range req.Questions {
		if answer, ok := f.answers[id]; ok {
			out[id] = answer
		}
	}
	return judge.Response{Answers: out}, nil
}
