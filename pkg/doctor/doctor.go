// Package doctor diagnoses Factotum's optional subsystems and recommends concrete
// fixes. A subsystem is optional and fails softly at use time - the embedder and the
// judge fall back to a deterministic path - so the only signal today is a one-line
// warning that does not say what to change. Doctor turns that into a report: each
// check names the observed state and, when something is wrong, the exact fix.
//
// Doctor never acts on its own. Diagnose only reads config and (through the injected
// Prober) a reachability probe; it starts no process and downloads nothing. Applying
// a fix is the caller's job, and only with explicit consent.
package doctor

import (
	"context"
	"fmt"
	"strings"
)

// Status is a check's outcome. The report's exit is derived from these: any Fail
// fails the report, and a caller may treat Warn as a failure with --strict.
type Status string

const (
	// StatusOK means the subsystem works as configured (or is intentionally off).
	StatusOK Status = "ok"
	// StatusWarn means the subsystem is not configured, or works with a fallback.
	StatusWarn Status = "warn"
	// StatusFail means the subsystem is configured but broken and needs action.
	StatusFail Status = "fail"
)

// Action is a concrete, consented fix a check can offer. It is data, not effect:
// Diagnose never runs it.
type Action struct {
	// Kind is ActionCommand or ActionReindex.
	Kind string `json:"kind" yaml:"kind"`
	// Command is the shell command for ActionCommand.
	Command string `json:"command,omitempty" yaml:"command,omitempty"`
	// Project is the project whose memory to re-embed for ActionReindex.
	Project string `json:"project,omitempty" yaml:"project,omitempty"`
	// Description is the human summary of what the fix does.
	Description string `json:"description" yaml:"description"`
}

const (
	// ActionCommand runs a provider-specific shell command (for example `ollama pull`).
	ActionCommand = "command"
	// ActionReindex re-embeds a project's memory after a model change.
	ActionReindex = "reindex"
)

// Check is one diagnostic: a named assertion, its observed state, and how to fix it.
type Check struct {
	Name           string  `json:"name" yaml:"name"`
	Status         Status  `json:"status" yaml:"status"`
	Summary        string  `json:"summary" yaml:"summary"`
	Recommendation string  `json:"recommendation,omitempty" yaml:"recommendation,omitempty"`
	Action         *Action `json:"action,omitempty" yaml:"action,omitempty"`
}

// Report is a whole diagnosis. Overall is the worst check status.
type Report struct {
	Checks  []Check `json:"checks" yaml:"checks"`
	Overall Status  `json:"overall" yaml:"overall"`
}

// Failed reports whether any check is a hard failure.
func (r Report) Failed() bool { return r.count(StatusFail) > 0 }

// Warned reports whether any check is a warning (including failures).
func (r Report) Warned() bool { return r.count(StatusWarn) > 0 || r.Failed() }

func (r Report) count(status Status) int {
	n := 0
	for _, check := range r.Checks {
		if check.Status == status {
			n++
		}
	}
	return n
}

// Endpoint is the embedder endpoint to probe. Protocol selects the wire shape
// (ollama or openai); URL is the configured endpoint without a path suffix.
type Endpoint struct {
	Protocol string
	URL      string
	APIKey   string
	Model    string
}

// Prober performs the external checks config alone cannot answer. It is an
// interface so tests inject a fake and never open a socket or scan the PATH.
type Prober interface {
	// Reachable reports whether the endpoint answers at all. Any HTTP response is
	// reachable; only a transport failure is not.
	Reachable(ctx context.Context, ep Endpoint) error
	// HasModel reports whether the endpoint serves the configured model.
	HasModel(ctx context.Context, ep Endpoint) error
	// Resolvable reports whether the command's executable is on PATH.
	Resolvable(ctx context.Context, command string) error
}

// Embed is the embedder configuration doctor diagnoses.
type Embed struct {
	// Provider is the configured provider name; "" means vector recall is off.
	Provider string
	// Known is whether Provider is a registered provider. The caller computes it, so
	// doctor stays independent of the provider registry.
	Known bool
	// Endpoint is the HTTP endpoint (ollama/openai), unused for the command provider.
	Endpoint string
	// Command is the stdio fallback command.
	Command string
	// Model is the embedding model.
	Model string
	// APIKey is the endpoint credential, when one is configured.
	APIKey string
}

// Judge is the judge configuration doctor diagnoses. Only the credential is
// diagnosed in v1; a judge's model/endpoint are not probed.
type Judge struct {
	Provider string
	Known    bool
	KeySet   bool
}

// Input is everything a diagnosis reads. It is pure data plus the Prober, so
// Diagnose never touches config files, the store, or the network directly.
type Input struct {
	Embed Embed
	Judge Judge
	// Project is the project whose memory index is inspected; "" skips the index
	// check.
	Project string
	// MemoryCount is how many memory artifacts the project has. The index check
	// runs only when there is memory to embed.
	MemoryCount int
	// IndexModels are the distinct embedding models present in the project's
	// vectors. Empty means no vectors are indexed yet.
	IndexModels []string
	Probe       Prober
}

// Diagnose runs the embedder and judge checks and returns the report. It is
// read-only: no fix is applied.
func Diagnose(ctx context.Context, in Input) Report {
	var checks []Check
	provider := embedProviderCheck(in.Embed)
	checks = append(checks, provider)
	if provider.Status != StatusFail && in.Embed.Provider != "" {
		checks = append(checks, embedTransportChecks(ctx, in)...)
	}
	if index, ok := embedIndexCheck(in); ok {
		checks = append(checks, index)
	}
	checks = append(checks, judgeCheck(in.Judge))
	return Report{Checks: checks, Overall: worst(checks)}
}

// embedProviderCheck answers "is a provider configured, and can it be built?".
func embedProviderCheck(e Embed) Check {
	const name = "embed.provider"
	switch {
	case e.Provider == "":
		return Check{
			Name:           name,
			Status:         StatusWarn,
			Summary:        "no embedding provider configured; memory search is lexical",
			Recommendation: `set [embed] provider to "ollama", "openai", or "command" to enable vector recall`,
		}
	case !e.Known:
		return Check{
			Name:           name,
			Status:         StatusFail,
			Summary:        fmt.Sprintf("unknown embedding provider %q", e.Provider),
			Recommendation: "set [embed] provider to a registered provider (ollama, openai, or command)",
		}
	case e.Provider == "command" && e.Command == "":
		return Check{
			Name:           name,
			Status:         StatusFail,
			Summary:        `provider "command" has no command configured`,
			Recommendation: "set [embed] command to an embedding command, or choose another provider",
		}
	case e.Provider != "command" && e.Endpoint == "" && e.Command == "":
		return Check{
			Name:           name,
			Status:         StatusFail,
			Summary:        fmt.Sprintf("provider %q has neither an endpoint nor a command", e.Provider),
			Recommendation: "set [embed] endpoint (for example http://localhost:11434), or set [embed] command",
		}
	default:
		return Check{Name: name, Status: StatusOK, Summary: fmt.Sprintf("provider %q configured", e.Provider)}
	}
}

// embedTransportChecks probes the endpoint, the model, and the stdio command. It
// assumes the provider is configured and known.
func embedTransportChecks(ctx context.Context, in Input) []Check {
	e := in.Embed
	var checks []Check
	reachable := false
	if e.Provider != "command" && e.Endpoint != "" {
		endpoint := Endpoint{Protocol: e.Provider, URL: e.Endpoint, APIKey: e.APIKey, Model: e.Model}
		if in.Probe != nil {
			if err := in.Probe.Reachable(ctx, endpoint); err != nil {
				checks = append(checks, Check{
					Name:           "embed.endpoint",
					Status:         StatusFail,
					Summary:        fmt.Sprintf("endpoint %s is unreachable: %v", e.Endpoint, err),
					Recommendation: fmt.Sprintf("start the embedding server, or fix [embed] endpoint (currently %s)", e.Endpoint),
				})
			} else {
				reachable = true
				checks = append(checks, Check{Name: "embed.endpoint", Status: StatusOK, Summary: fmt.Sprintf("endpoint %s is reachable", e.Endpoint)})
			}
		}
	}
	if reachable && e.Model != "" && in.Probe != nil {
		checks = append(checks, embedModelCheck(ctx, in.Probe, e))
	}
	if e.Command != "" {
		checks = append(checks, embedCommandCheck(ctx, in.Probe, e.Command))
	}
	return checks
}

// embedModelCheck reports whether the endpoint serves the configured model.
func embedModelCheck(ctx context.Context, probe Prober, e Embed) Check {
	endpoint := Endpoint{Protocol: e.Provider, URL: e.Endpoint, APIKey: e.APIKey, Model: e.Model}
	if err := probe.HasModel(ctx, endpoint); err != nil {
		check := Check{
			Name:           "embed.model",
			Status:         StatusFail,
			Summary:        fmt.Sprintf("model %q is not served by %s: %v", e.Model, e.Endpoint, err),
			Recommendation: fmt.Sprintf("install or pull model %q on the configured endpoint", e.Model),
		}
		if e.Provider == "ollama" {
			check.Action = &Action{
				Kind:        ActionCommand,
				Command:     "ollama pull " + e.Model,
				Description: "pull " + e.Model + " from the Ollama registry",
			}
		}
		return check
	}
	return Check{Name: "embed.model", Status: StatusOK, Summary: fmt.Sprintf("model %q is served by the endpoint", e.Model)}
}

// embedCommandCheck reports whether the stdio command's executable is on PATH.
func embedCommandCheck(ctx context.Context, probe Prober, command string) Check {
	if probe != nil {
		if err := probe.Resolvable(ctx, command); err != nil {
			return Check{
				Name:           "embed.command",
				Status:         StatusFail,
				Summary:        fmt.Sprintf("command %q is not resolvable: %v", command, err),
				Recommendation: fmt.Sprintf("install the command, or fix [embed] command (currently %q)", command),
			}
		}
	}
	return Check{Name: "embed.command", Status: StatusOK, Summary: fmt.Sprintf("command %q is resolvable", command)}
}

// embedIndexCheck compares the vector side index's models against the configured
// model. It is skipped when there is no memory, no model, or no project: there is
// nothing meaningful to compare.
func embedIndexCheck(in Input) (Check, bool) {
	e := in.Embed
	if e.Provider == "" || !e.Known || e.Model == "" || in.Project == "" || in.MemoryCount == 0 {
		return Check{}, false
	}
	switch {
	case len(in.IndexModels) == 0:
		return Check{
			Name:           "embed.index",
			Status:         StatusWarn,
			Summary:        fmt.Sprintf("%d memories are not indexed; vector recall returns nothing for %s", in.MemoryCount, in.Project),
			Recommendation: "backfill the vectors with `ft memory reindex`",
			Action:         reindexAction(in.Project),
		}, true
	case len(in.IndexModels) == 1 && in.IndexModels[0] == e.Model:
		return Check{Name: "embed.index", Status: StatusOK, Summary: fmt.Sprintf("index matches the configured model %q", e.Model)}, true
	default:
		return Check{
			Name:           "embed.index",
			Status:         StatusFail,
			Summary:        fmt.Sprintf("index holds vectors from %s, but %q is configured", strings.Join(quoteAll(in.IndexModels), ", "), e.Model),
			Recommendation: "re-embed the project's memory with `ft memory reindex`",
			Action:         reindexAction(in.Project),
		}, true
	}
}

func reindexAction(project string) *Action {
	return &Action{
		Kind:        ActionReindex,
		Project:     project,
		Description: "re-embed the project's memory with the configured model",
	}
}

// judgeCheck answers "is the judge usable, and is a key set?".
func judgeCheck(j Judge) Check {
	const name = "judge.key"
	switch {
	case j.Provider == "":
		return Check{Name: name, Status: StatusWarn, Summary: "no judge provider configured; judge-backed features are disabled"}
	case !j.Known:
		return Check{
			Name:           name,
			Status:         StatusFail,
			Summary:        fmt.Sprintf("unknown judge provider %q", j.Provider),
			Recommendation: `set [judge] provider to "typesafe"`,
		}
	case !j.KeySet:
		return Check{
			Name:           name,
			Status:         StatusWarn,
			Summary:        fmt.Sprintf("judge provider %q has no API key; judge-backed features are disabled", j.Provider),
			Recommendation: "set the provider's key (for example [typesafe] secret_api_key, or TYPESAFE_API_KEY)",
		}
	default:
		return Check{Name: name, Status: StatusOK, Summary: fmt.Sprintf("judge provider %q configured with an API key", j.Provider)}
	}
}

func worst(checks []Check) Status {
	overall := StatusOK
	for _, check := range checks {
		if check.Status == StatusFail {
			return StatusFail
		}
		if check.Status == StatusWarn {
			overall = StatusWarn
		}
	}
	return overall
}

func quoteAll(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = fmt.Sprintf("%q", value)
	}
	return out
}
