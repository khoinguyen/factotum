package command

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/agent"
)

func TestProviderWithoutCommandIsDisabled(t *testing.T) {
	built, err := agent.New("command", emptyEnv, map[string]string{})
	if err != nil {
		t.Fatalf("New(\"command\") error = %v", err)
	}
	if _, ok := built.(agent.Disabled); !ok {
		t.Fatalf("New(\"command\") = %T, want Disabled when no command is set", built)
	}
}

func TestProviderWithCommandBuildsAgent(t *testing.T) {
	built, err := agent.New("command", emptyEnv, map[string]string{"command": "my-agent"})
	if err != nil {
		t.Fatalf("New(\"command\") error = %v", err)
	}
	if _, ok := built.(*Command); !ok {
		t.Fatalf("New(\"command\") = %T, want *Command", built)
	}
}

func TestBreakdownRunsCommandAndParsesPlan(t *testing.T) {
	var gotCommand string
	var gotStdin string
	run := func(_ context.Context, command string, stdin []byte) ([]byte, error) {
		gotCommand, gotStdin = command, string(stdin)
		return []byte(`{"tasks":[{"kind":"task","title":"Add auth","description":"hash passwords","labels":["auth"]},{"title":"Ship it"}]}`), nil
	}
	client := NewCommand("my-agent --json", run)

	plan, err := client.Breakdown(context.Background(), agent.Request{Prompt: "build login", Project: "acme"})
	if err != nil {
		t.Fatalf("Breakdown() error = %v", err)
	}
	if gotCommand != "my-agent --json" {
		t.Fatalf("command = %q, want the configured command", gotCommand)
	}
	if !strings.Contains(gotStdin, "build login") {
		t.Fatalf("stdin should carry the prompt:\n%s", gotStdin)
	}
	if !strings.Contains(gotStdin, "acme") {
		t.Fatalf("stdin should name the target project:\n%s", gotStdin)
	}
	if len(plan.Tasks) != 2 || plan.Tasks[0].Title != "Add auth" || plan.Tasks[1].Title != "Ship it" {
		t.Fatalf("plan = %+v, want the two parsed tasks", plan)
	}
	if len(plan.Tasks[0].Labels) != 1 || plan.Tasks[0].Labels[0] != "auth" {
		t.Fatalf("first task labels = %v", plan.Tasks[0].Labels)
	}
}

func TestBreakdownStripsCodeFence(t *testing.T) {
	run := func(context.Context, string, []byte) ([]byte, error) {
		return []byte("Here is the plan:\n```json\n{\"tasks\":[{\"title\":\"One\"}]}\n```\n"), nil
	}
	plan, err := NewCommand("agent", run).Breakdown(context.Background(), agent.Request{Prompt: "x"})
	if err != nil {
		t.Fatalf("Breakdown() error = %v", err)
	}
	if len(plan.Tasks) != 1 || plan.Tasks[0].Title != "One" {
		t.Fatalf("plan = %+v, want the fenced task", plan)
	}
}

func TestBreakdownExtractsJSONFromProse(t *testing.T) {
	run := func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`Here is the plan: {"tasks":[{"title":"One"}]}. Done.`), nil
	}
	plan, err := NewCommand("agent", run).Breakdown(context.Background(), agent.Request{Prompt: "x"})
	if err != nil {
		t.Fatalf("Breakdown() error = %v", err)
	}
	if len(plan.Tasks) != 1 || plan.Tasks[0].Title != "One" {
		t.Fatalf("plan = %+v, want the JSON embedded in prose", plan)
	}
}

func TestSanitizeExtractsThePlanObject(t *testing.T) {
	const plan = `{"tasks":[{"title":"One"}]}`
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain object", plan, plan},
		{"prose around", "Here is the plan: " + plan + ". Done.", plan},
		{"prose braces before", "Use {templates} for layout. Plan: " + plan, plan},
		{"prose braces after", plan + " then {done}", plan},
		{"braces inside a string", `{"tasks":[{"title":"One","description":"a {b} c"}]}`,
			`{"tasks":[{"title":"One","description":"a {b} c"}]}`},
		{"code fence", "```json\n" + plan + "\n```", plan},
		{"braces around a code fence", "Here {x}:\n```json\n" + plan + "\n```\nend {y}", plan},
		{"stray object before the plan", `Schema {"a":1}. Plan: ` + plan, plan},
		{"no object at all", "no json here", "no json here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(sanitize([]byte(tc.in))); got != tc.want {
				t.Fatalf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestProviderAppliesConfiguredTimeout(t *testing.T) {
	built, err := agent.New("command", emptyEnv, map[string]string{"command": "agent", "timeout": "5s"})
	if err != nil {
		t.Fatalf("New(\"command\") error = %v", err)
	}
	client, ok := built.(*Command)
	if !ok {
		t.Fatalf("New(\"command\") = %T, want *Command", built)
	}
	if client.timeout != 5*time.Second {
		t.Fatalf("timeout = %v, want 5s", client.timeout)
	}
}

func TestProviderWithoutTimeoutHasNoDeadline(t *testing.T) {
	built, err := agent.New("command", emptyEnv, map[string]string{"command": "agent"})
	if err != nil {
		t.Fatalf("New(\"command\") error = %v", err)
	}
	if client := built.(*Command); client.timeout != 0 {
		t.Fatalf("timeout = %v, want none", client.timeout)
	}
}

func TestBreakdownPropagatesCommandFailure(t *testing.T) {
	run := func(context.Context, string, []byte) ([]byte, error) {
		return nil, errors.New("boom")
	}
	_, err := NewCommand("agent", run).Breakdown(context.Background(), agent.Request{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Breakdown() error = %v, want the command failure", err)
	}
}

func TestBreakdownRejectsInvalidJSON(t *testing.T) {
	run := func(context.Context, string, []byte) ([]byte, error) {
		return []byte("not json"), nil
	}
	_, err := NewCommand("agent", run).Breakdown(context.Background(), agent.Request{Prompt: "x"})
	if err == nil {
		t.Fatal("Breakdown() error = nil, want a decode error")
	}
}

func emptyEnv(string) string { return "" }
