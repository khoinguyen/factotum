// Package agent is the port for turning a free-form prompt into a proposed set
// of tasks. Features that need inference depend on this interface, never on a
// provider adapter, so the capability is fakeable in tests and disabled when no
// agent CLI is configured.
//
// The default provider runs a configured agent CLI; model selection is part of
// that command. No credentials live in ft.
package agent

import (
	"context"
	"errors"
	"fmt"
)

// ErrUnavailable means no agent is configured. Callers report a clear error
// instead of running an inference they cannot fulfill.
var ErrUnavailable = errors.New("agent unavailable")

// Task is one proposed task in a plan. It carries only the fields an agent can
// author; identity, status, and timestamps belong to the store.
type Task struct {
	Kind        string   `json:"kind,omitempty" yaml:"kind,omitempty"`
	Title       string   `json:"title" yaml:"title"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Repo        string   `json:"repo,omitempty" yaml:"repo,omitempty"`
	Priority    int      `json:"priority,omitempty" yaml:"priority,omitempty"`
	Labels      []string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// Plan is the set of tasks an agent proposes for one prompt.
type Plan struct {
	Tasks []Task `json:"tasks" yaml:"tasks"`
}

// Request is one breakdown: the prompt and the project it targets.
type Request struct {
	Prompt  string
	Project string
}

// Agent breaks a prompt into a plan. Implementations must be safe for
// concurrent use.
type Agent interface {
	Breakdown(ctx context.Context, req Request) (Plan, error)
}

// Disabled is an agent with no backend. It returns ErrUnavailable so callers
// report that no agent is configured.
type Disabled struct{}

func (Disabled) Breakdown(context.Context, Request) (Plan, error) {
	return Plan{}, ErrUnavailable
}

// Provider builds an Agent from provider-specific options. The agent capability
// does not know any provider by name; each provider parses its own options and
// reads its own environment variables (getenv), so adding a provider does not
// touch this port.
type Provider func(getenv func(string) string, options map[string]string) (Agent, error)

var providers = map[string]Provider{}

// Register adds a provider by name. It panics on an empty or duplicate name, like
// the other plugin registries in the project, so misconfiguration fails at startup.
func Register(name string, provider Provider) {
	if name == "" || provider == nil {
		panic("agent: invalid provider registration")
	}
	if _, exists := providers[name]; exists {
		panic("agent: duplicate provider " + name)
	}
	providers[name] = provider
}

// New builds the named provider from its options. An empty name means no agent is
// configured and returns Disabled; an unknown name is an error.
func New(name string, getenv func(string) string, options map[string]string) (Agent, error) {
	if name == "" {
		return Disabled{}, nil
	}
	provider, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("agent: unknown provider %q", name)
	}
	return provider(getenv, options)
}
