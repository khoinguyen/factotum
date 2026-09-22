// Package command is the default agent provider: it shells out to a configured
// agent CLI, feeds it a grounded instruction on stdin, and parses a JSON plan
// from stdout. It is the only package that knows the child-process protocol; the
// rest of the code depends on pkg/agent.
package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/khoinguyen/factotum/pkg/agent"
)

func init() { agent.Register("command", provider) }

// provider builds the CLI provider. With no command configured there is nothing
// to run, so the agent is Disabled and the caller reports a clear error.
func provider(_ func(string) string, options map[string]string) (agent.Agent, error) {
	command := strings.TrimSpace(options["command"])
	if command == "" {
		return agent.Disabled{}, nil
	}
	return NewCommand(command, nil), nil
}

// Runner executes a one-shot command with stdin and returns stdout. It is a var
// so tests inject a fake and never spawn a process.
type Runner func(ctx context.Context, command string, stdin []byte) ([]byte, error)

// Command breaks a prompt into a plan by running a one-shot agent CLI.
type Command struct {
	command string
	run     Runner
}

// NewCommand builds a CLI agent. A nil runner executes the command with sh -c.
func NewCommand(command string, run Runner) *Command {
	if run == nil {
		run = execRunner
	}
	return &Command{command: command, run: run}
}

func (c *Command) Breakdown(ctx context.Context, req agent.Request) (agent.Plan, error) {
	out, err := c.run(ctx, c.command, []byte(instruction(req)))
	if err != nil {
		return agent.Plan{}, err
	}
	var plan agent.Plan
	if err := json.Unmarshal(sanitize(out), &plan); err != nil {
		return agent.Plan{}, fmt.Errorf("agent: decode plan: %w", err)
	}
	return plan, nil
}

// instruction grounds the agent: the target project, the exact JSON shape it
// must return, and the user's request.
func instruction(req agent.Request) string {
	var b strings.Builder
	b.WriteString("You are a planning agent for the Factotum task graph.\n")
	fmt.Fprintf(&b, "Break the request below into a set of tasks for project %q.\n\n", req.Project)
	b.WriteString("Reply with a single JSON object and nothing else, in this shape:\n")
	b.WriteString(`{"tasks":[{"kind":"task","title":"...","description":"...","repo":"...","priority":0,"labels":["..."]}]}` + "\n\n")
	b.WriteString("Rules: kind is \"task\" or \"milestone\"; title is a short imperative phrase; " +
		"description carries context and acceptance criteria; repo is one of the project's " +
		"repositories or omitted; priority is an integer, higher is more important; labels are " +
		"optional. Never include ids.\n\n")
	b.WriteString("Request:\n")
	b.WriteString(req.Prompt)
	b.WriteString("\n")
	return b.String()
}

// sanitize tolerates the common agent habit of wrapping JSON in prose or a
// markdown code fence: it extracts the fenced body when present and otherwise
// returns the trimmed output.
func sanitize(out []byte) []byte {
	text := strings.TrimSpace(string(out))
	start := strings.Index(text, "```")
	if start < 0 {
		return []byte(text)
	}
	text = text[start+3:]
	if newline := strings.IndexByte(text, '\n'); newline >= 0 {
		text = text[newline+1:]
	}
	if end := strings.LastIndex(text, "```"); end >= 0 {
		text = text[:end]
	}
	return []byte(strings.TrimSpace(text))
}

func execRunner(ctx context.Context, command string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("agent: command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
