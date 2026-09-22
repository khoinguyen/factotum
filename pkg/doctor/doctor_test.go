package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeProbe records whether the network checks ran and returns canned outcomes,
// so the diagnosis logic is tested without a socket.
type fakeProbe struct {
	reachErr   error
	modelErr   error
	commandErr error
	reachCalls int
	modelCalls int
	cmdCalls   int
}

func (p *fakeProbe) Reachable(context.Context, Endpoint) error {
	p.reachCalls++
	return p.reachErr
}

func (p *fakeProbe) HasModel(context.Context, Endpoint) error {
	p.modelCalls++
	return p.modelErr
}

func (p *fakeProbe) Resolvable(context.Context, string) error {
	p.cmdCalls++
	return p.commandErr
}

func findCheck(t *testing.T, r Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in report %+v", name, r.Checks)
	return Check{}
}

func hasCheck(r Report, name string) bool {
	for _, c := range r.Checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestDiagnoseNoProviderIsWarnNotFail(t *testing.T) {
	probe := &fakeProbe{}
	report := Diagnose(context.Background(), Input{
		Embed: Embed{},
		Judge: Judge{Provider: "typesafe", Known: true},
		Probe: probe,
	})
	if report.Failed() {
		t.Fatalf("an unconfigured optional subsystem is not a failure: %+v", report.Checks)
	}
	if !report.Warned() || report.Overall != StatusWarn {
		t.Fatalf("unconfigured provider should warn, overall = %q", report.Overall)
	}
	if c := findCheck(t, report, "embed.provider"); c.Status != StatusWarn {
		t.Fatalf("embed.provider = %+v, want warn", c)
	}
	if c := findCheck(t, report, "judge.key"); c.Status != StatusWarn {
		t.Fatalf("judge.key = %+v, want warn", c)
	}
	if probe.reachCalls != 0 || probe.modelCalls != 0 || probe.cmdCalls != 0 {
		t.Fatalf("an unconfigured provider must not probe: %+v", probe)
	}
}

func TestDiagnoseUnknownProviderFails(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed: Embed{Provider: "bogus", Known: false},
		Judge: Judge{Provider: "typesafe", Known: true, KeySet: true},
		Probe: &fakeProbe{},
	})
	c := findCheck(t, report, "embed.provider")
	if c.Status != StatusFail {
		t.Fatalf("embed.provider = %+v, want fail", c)
	}
	if !report.Failed() {
		t.Fatal("an unknown provider should fail the report")
	}
	if hasCheck(report, "embed.endpoint") || hasCheck(report, "embed.model") {
		t.Fatalf("a provider that cannot be built should not add transport checks: %+v", report.Checks)
	}
}

func TestDiagnoseCommandProviderNeedsCommand(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed: Embed{Provider: "command", Known: true, Command: ""},
		Judge: Judge{Provider: "typesafe", Known: true, KeySet: true},
		Probe: &fakeProbe{},
	})
	if c := findCheck(t, report, "embed.provider"); c.Status != StatusFail {
		t.Fatalf("command provider without a command must fail: %+v", c)
	}
}

func TestDiagnoseHTTPProviderNeedsSomeTransport(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed: Embed{Provider: "ollama", Known: true},
		Judge: Judge{Provider: "typesafe", Known: true, KeySet: true},
		Probe: &fakeProbe{},
	})
	if c := findCheck(t, report, "embed.provider"); c.Status != StatusFail {
		t.Fatalf("a provider with no endpoint and no command must fail: %+v", c)
	}
}

func healthyEmbed() Embed {
	return Embed{Provider: "ollama", Known: true, Endpoint: "http://127.0.0.1:11434", Model: "nomic-embed-text"}
}

func TestDiagnoseHealthyEmbedder(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed: healthyEmbed(),
		Judge: Judge{Provider: "typesafe", Known: true, KeySet: true},
		Probe: &fakeProbe{},
	})
	for _, name := range []string{"embed.provider", "embed.endpoint", "embed.model"} {
		if c := findCheck(t, report, name); c.Status != StatusOK {
			t.Fatalf("%s = %+v, want ok", name, c)
		}
	}
	if report.Overall != StatusOK || report.Failed() || report.Warned() {
		t.Fatalf("healthy report = %+v", report)
	}
}

func TestDiagnoseUnreachableEndpointSkipsModelProbe(t *testing.T) {
	probe := &fakeProbe{reachErr: errors.New("connection refused")}
	report := Diagnose(context.Background(), Input{
		Embed: healthyEmbed(),
		Judge: Judge{Provider: "typesafe", Known: true, KeySet: true},
		Probe: probe,
	})
	if c := findCheck(t, report, "embed.endpoint"); c.Status != StatusFail || !strings.Contains(c.Summary, "connection refused") {
		t.Fatalf("embed.endpoint = %+v, want fail with the cause", c)
	}
	if probe.modelCalls != 0 || hasCheck(report, "embed.model") {
		t.Fatalf("the model cannot be checked on an unreachable endpoint: %+v", report.Checks)
	}
	if !report.Failed() {
		t.Fatal("an unreachable configured endpoint should fail")
	}
}

func TestDiagnoseMissingOllamaModelOffersPull(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed: healthyEmbed(),
		Judge: Judge{Provider: "typesafe", Known: true, KeySet: true},
		Probe: &fakeProbe{modelErr: errors.New("not found")},
	})
	c := findCheck(t, report, "embed.model")
	if c.Status != StatusFail {
		t.Fatalf("embed.model = %+v, want fail", c)
	}
	if c.Action == nil || c.Action.Kind != ActionCommand || c.Action.Command != "ollama pull nomic-embed-text" {
		t.Fatalf("embed.model action = %+v, want `ollama pull nomic-embed-text`", c.Action)
	}
}

func TestDiagnoseMissingOpenAIModelHasNoPullAction(t *testing.T) {
	embed := healthyEmbed()
	embed.Provider = "openai"
	report := Diagnose(context.Background(), Input{
		Embed: embed,
		Judge: Judge{Provider: "typesafe", Known: true, KeySet: true},
		Probe: &fakeProbe{modelErr: errors.New("not found")},
	})
	if c := findCheck(t, report, "embed.model"); c.Status != StatusFail || c.Action != nil {
		t.Fatalf("openai model check = %+v, want fail without an auto-fix", c)
	}
}

func TestDiagnoseUnresolvableCommandFails(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed: Embed{Provider: "command", Known: true, Command: "my-embed-cli"},
		Judge: Judge{Provider: "typesafe", Known: true, KeySet: true},
		Probe: &fakeProbe{commandErr: errors.New("executable file not found")},
	})
	c := findCheck(t, report, "embed.command")
	if c.Status != StatusFail || !strings.Contains(c.Summary, "my-embed-cli") {
		t.Fatalf("embed.command = %+v, want fail", c)
	}
}

func TestDiagnoseResolvableCommandIsOK(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed: Embed{Provider: "command", Known: true, Command: "my-embed-cli"},
		Judge: Judge{Provider: "typesafe", Known: true, KeySet: true},
		Probe: &fakeProbe{},
	})
	if c := findCheck(t, report, "embed.command"); c.Status != StatusOK {
		t.Fatalf("embed.command = %+v, want ok", c)
	}
}

func TestDiagnoseIndexModelMismatchFailsWithReindex(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed:       healthyEmbed(),
		Judge:       Judge{Provider: "typesafe", Known: true, KeySet: true},
		Project:     "acme",
		MemoryCount: 3,
		IndexModels: []string{"old-model"},
		Probe:       &fakeProbe{},
	})
	c := findCheck(t, report, "embed.index")
	if c.Status != StatusFail {
		t.Fatalf("embed.index = %+v, want fail", c)
	}
	if c.Action == nil || c.Action.Kind != ActionReindex || c.Action.Project != "acme" {
		t.Fatalf("embed.index action = %+v, want reindex of acme", c.Action)
	}
}

func TestDiagnoseIndexMatchIsOK(t *testing.T) {
	e := healthyEmbed()
	report := Diagnose(context.Background(), Input{
		Embed:       e,
		Judge:       Judge{Provider: "typesafe", Known: true, KeySet: true},
		Project:     "acme",
		MemoryCount: 3,
		IndexModels: []string{e.Model},
		Probe:       &fakeProbe{},
	})
	if c := findCheck(t, report, "embed.index"); c.Status != StatusOK {
		t.Fatalf("embed.index = %+v, want ok", c)
	}
}

func TestDiagnoseEmptyIndexWarns(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed:       healthyEmbed(),
		Judge:       Judge{Provider: "typesafe", Known: true, KeySet: true},
		Project:     "acme",
		MemoryCount: 2,
		IndexModels: nil,
		Probe:       &fakeProbe{},
	})
	c := findCheck(t, report, "embed.index")
	if c.Status != StatusWarn || c.Action == nil || c.Action.Kind != ActionReindex {
		t.Fatalf("embed.index = %+v, want warn with a reindex action", c)
	}
}

func TestDiagnoseNoMemoriesSkipsIndex(t *testing.T) {
	report := Diagnose(context.Background(), Input{
		Embed:   healthyEmbed(),
		Judge:   Judge{Provider: "typesafe", Known: true, KeySet: true},
		Project: "acme",
		Probe:   &fakeProbe{},
	})
	if hasCheck(report, "embed.index") {
		t.Fatalf("no memory to index should skip the index check: %+v", report.Checks)
	}
}

func TestDiagnoseJudgeKeyStates(t *testing.T) {
	tests := []struct {
		name  string
		judge Judge
		want  Status
	}{
		{"unknown provider", Judge{Provider: "nope", Known: false}, StatusFail},
		{"no key", Judge{Provider: "typesafe", Known: true, KeySet: false}, StatusWarn},
		{"key set", Judge{Provider: "typesafe", Known: true, KeySet: true}, StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := Diagnose(context.Background(), Input{
				Embed: Embed{},
				Judge: tt.judge,
				Probe: &fakeProbe{},
			})
			if c := findCheck(t, report, "judge.key"); c.Status != tt.want {
				t.Fatalf("judge.key = %+v, want %s", c, tt.want)
			}
		})
	}
}
