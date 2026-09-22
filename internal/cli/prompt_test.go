package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/agent"
	"github.com/khoinguyen/factotum/pkg/agent/fake"
	"github.com/khoinguyen/factotum/pkg/app"
)

func planAgent(t *testing.T, r *runner, tasks ...agent.Task) *fake.Agent {
	t.Helper()
	ag := fake.New(agent.Plan{Tasks: tasks})
	r.agent = ag
	return ag
}

// executePrompt runs a command with the runner's setup and returns any error,
// including non-usage errors that runSplit would treat as fatal.
func executePrompt(t *testing.T, r *runner, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &stdout, &stderr, nil)
	base := r.setup(deps)
	root := NewRoot(deps)
	root.SetArgs(append(append([]string{}, base...), args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestPromptPreviewDoesNotCreateTasks(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	planAgent(t, r, agent.Task{
		Kind:        "task",
		Title:       "Add auth",
		Description: "hash passwords",
		Labels:      []string{"auth"},
	})

	out := r.run("prompt", "--project", projectID, "build", "login")

	if !strings.Contains(out, "project_id: "+projectID) {
		t.Fatalf("preview should carry the target project:\n%s", out)
	}
	if !strings.Contains(out, "title: Add auth") || !strings.Contains(out, "description: hash passwords") {
		t.Fatalf("preview should use the task-apply document shape:\n%s", out)
	}
	if !strings.Contains(out, "status: todo") {
		t.Fatalf("preview should show the proposed status:\n%s", out)
	}
	if list := r.run("task", "list", "--project", projectID); strings.Contains(list, "Add auth") {
		t.Fatalf("preview must not create tasks:\n%s", list)
	}
}

func TestPromptApplyCreatesTasks(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	planAgent(t, r,
		agent.Task{Title: "Add auth", Description: "hash passwords"},
		agent.Task{Kind: "milestone", Title: "Ship login"},
	)

	out := r.run("prompt", "--project", projectID, "--yes", "build", "login")
	if !strings.Contains(out, "created: true") {
		t.Fatalf("apply should report creation:\n%s", out)
	}

	list := r.run("task", "list", "--project", projectID)
	if !strings.Contains(list, "Add auth") || !strings.Contains(list, "Ship login") {
		t.Fatalf("apply should create every proposed task:\n%s", list)
	}
	if !strings.Contains(list, "milestone") {
		t.Fatalf("apply should preserve the proposed kind:\n%s", list)
	}
}

func TestPromptProjectFallsBackToDefault(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ag := planAgent(t, r, agent.Task{Title: "Add auth"})

	if err := os.WriteFile(r.userPath, []byte("default_project = \""+projectID+"\"\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	out := r.run("prompt", "build", "login")

	if !strings.Contains(out, "project_id: "+projectID) {
		t.Fatalf("preview should use the default project:\n%s", out)
	}
	requests := ag.Requests()
	if len(requests) != 1 || requests[0].Project != projectID {
		t.Fatalf("agent requests = %+v, want project %q", requests, projectID)
	}
}

func TestPromptProjectFlagOverridesDefault(t *testing.T) {
	r := newRunner(t)
	firstField(t, r.run("project", "create", "Acme"))
	other := firstField(t, r.run("project", "create", "Beta"))
	ag := planAgent(t, r, agent.Task{Title: "Add auth"})

	if err := os.WriteFile(r.userPath, []byte("default_project = \"acme\"\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	out := r.run("prompt", "--project", other, "build", "login")

	if !strings.Contains(out, "project_id: "+other) {
		t.Fatalf("preview should use the flagged project:\n%s", out)
	}
	if requests := ag.Requests(); len(requests) != 1 || requests[0].Project != other {
		t.Fatalf("agent requests = %+v, want project %q", requests, other)
	}
}

func TestPromptWithoutAgentReportsClearError(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	_, stderr := r.runSplit("prompt", "--project", projectID, "build", "login")
	if !strings.Contains(stderr, "no agent configured") {
		t.Fatalf("stderr should explain the missing agent:\n%s", stderr)
	}
}

func TestPromptAgentCommandFlagOverridesConfigured(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ag := planAgent(t, r, agent.Task{Title: "From config"})

	out := r.run("prompt", "--project", projectID,
		"--agent-command", `printf %s '{"tasks":[{"title":"From flag"}]}'`, "build", "login")

	if !strings.Contains(out, "From flag") || strings.Contains(out, "From config") {
		t.Fatalf("--agent-command should win over the configured agent:\n%s", out)
	}
	if requests := ag.Requests(); len(requests) != 0 {
		t.Fatalf("configured agent should not be consulted, got %+v", requests)
	}
}

func TestPromptApplyRejectsInvalidPlanBeforeWriting(t *testing.T) {
	cases := []struct {
		name string
		task agent.Task
		want string
	}{
		{"unknown kind", agent.Task{Kind: "epic", Title: "Bad"}, "unknown task kind"},
		{"empty title", agent.Task{Title: "   "}, "title is required"},
		{"unknown repo", agent.Task{Title: "Bad", Repo: "nope"}, "not part of project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			projectID := firstField(t, r.run("project", "create", "Acme"))
			planAgent(t, r, agent.Task{Title: "Good"}, tc.task)

			_, _, err := executePrompt(t, r, "prompt", "--yes", "--project", projectID, "build", "login")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Execute() error = %v, want it to mention %q", err, tc.want)
			}
			if list := r.run("task", "list", "--project", projectID); strings.Contains(list, "Good") {
				t.Fatalf("an invalid plan must not be applied partially:\n%s", list)
			}
		})
	}
}

func TestPromptUnknownProviderIsConfigError(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	if err := os.WriteFile(r.userPath, []byte("[agent]\nprovider = \"bogus\"\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	_, _, err := executePrompt(t, r, "prompt", "--project", projectID, "build", "login")
	if err == nil || !strings.Contains(err.Error(), "unknown provider") || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("Execute() error = %v, want the unknown provider named", err)
	}
}

func TestPromptEmptyPlanIsNoted(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	planAgent(t, r)

	out := r.run("prompt", "--project", projectID, "build", "login")
	if !strings.Contains(out, "(no tasks proposed)") {
		t.Fatalf("an empty plan should say so:\n%s", out)
	}
}

func TestPromptAgentFailureIsReported(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ag := planAgent(t, r)
	ag.FailWith(errors.New("agent exploded"))

	if _, _, err := executePrompt(t, r, "prompt", "--project", projectID, "build", "login"); err == nil || !strings.Contains(err.Error(), "agent exploded") {
		t.Fatalf("Execute() error = %v, want the agent failure surfaced", err)
	}
}
