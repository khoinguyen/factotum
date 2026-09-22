package command

import (
	"context"
	"errors"
	"strings"
	"testing"

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
