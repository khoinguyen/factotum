// Package fake is a deterministic agent for tests. It never spawns a process.
package fake

import (
	"context"

	"github.com/khoinguyen/factotum/pkg/agent"
)

// Agent returns a programmed plan and records every request it receives.
type Agent struct {
	plan     agent.Plan
	requests []agent.Request
	err      error
}

// New returns a fake that answers every Breakdown with plan.
func New(plan agent.Plan) *Agent {
	return &Agent{plan: plan}
}

// FailWith makes every Breakdown return err, so callers can exercise their
// error path.
func (f *Agent) FailWith(err error) { f.err = err }

// Requests returns the requests recorded so far, in order.
func (f *Agent) Requests() []agent.Request { return f.requests }

func (f *Agent) Breakdown(_ context.Context, req agent.Request) (agent.Plan, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return agent.Plan{}, f.err
	}
	return f.plan, nil
}
