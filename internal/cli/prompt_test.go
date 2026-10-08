package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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

func TestPromptApplyIsIdempotent(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	planAgent(t, r,
		agent.Task{Title: "Add auth", Description: "hash passwords"},
		agent.Task{Kind: "milestone", Title: "Ship login"},
	)

	first := r.run("prompt", "--project", projectID, "--yes", "build", "login")
	if !strings.Contains(first, "created: true") {
		t.Fatalf("first run should create the plan:\n%s", first)
	}

	second := r.run("prompt", "--project", projectID, "--yes", "build", "login")
	if strings.Contains(second, "created: true") {
		t.Fatalf("an identical re-run must not create tasks:\n%s", second)
	}
	if !strings.Contains(second, "skipped: true") {
		t.Fatalf("an identical re-run should report the skipped tasks:\n%s", second)
	}

	list := r.run("task", "list", "--project", projectID)
	for _, title := range []string{"Add auth", "Ship login"} {
		if got := strings.Count(list, title); got != 1 {
			t.Fatalf("title %q appears %d times, want 1:\n%s", title, got, list)
		}
	}
}

func TestPromptApplySkipsExistingAndCreatesNew(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "create", "--project", projectID, "--title", "Add auth")
	planAgent(t, r,
		agent.Task{Title: "Add auth"},
		agent.Task{Title: "Ship login"},
	)

	out := r.run("prompt", "--project", projectID, "--yes", "build", "login")
	if !strings.Contains(out, "created: true") || !strings.Contains(out, "skipped: true") {
		t.Fatalf("a partially-applied plan should report both outcomes:\n%s", out)
	}

	list := r.run("task", "list", "--project", projectID)
	if got := strings.Count(list, "Add auth"); got != 1 {
		t.Fatalf("existing title appears %d times, want 1:\n%s", got, list)
	}
	if !strings.Contains(list, "Ship login") {
		t.Fatalf("the missing task should still be created:\n%s", list)
	}
}

// TestPromptApplyStructuredOutputCarriesSkipped pins the machine-readable shape
// of `ft prompt -y`: a `created` group and a `skipped` group of task documents,
// so a scripted re-run can tell what was written and what was left alone.
func TestPromptApplyStructuredOutputCarriesSkipped(t *testing.T) {
	cases := []struct {
		name   string
		format string
		parse  func(t *testing.T, out string) promptGroups
	}{
		{"json", "json", parsePromptJSON},
		{"yaml", "yaml", parsePromptYAML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			projectID := firstField(t, r.run("project", "create", "Acme"))
			r.run("task", "create", "--project", projectID, "--title", "Add auth")
			planAgent(t, r,
				agent.Task{Title: "Add auth"},
				agent.Task{Title: "Ship login"},
			)

			out := r.run("prompt", "--project", projectID, "--yes", "-o", tc.format, "build", "login")
			groups := tc.parse(t, out)
			if len(groups.Created) != 1 || groups.Created[0].Title != "Ship login" {
				t.Fatalf("created group = %+v, want only Ship login:\n%s", groups.Created, out)
			}
			if len(groups.Skipped) != 1 || groups.Skipped[0].Title != "Add auth" {
				t.Fatalf("skipped group = %+v, want only Add auth:\n%s", groups.Skipped, out)
			}
			if groups.Skipped[0].ProjectID != projectID {
				t.Fatalf("skipped entry should carry the project: %+v", groups.Skipped[0])
			}
		})
	}
}

// TestPromptApplyStructuredDocumentsStayPureTaskDocs guards the taskDoc
// contract: each created/skipped document must strict-parse as a task-apply
// document, with no outcome field leaking into it, so `jq '.created' | ft task
// apply -f -` works.
func TestPromptApplyStructuredDocumentsStayPureTaskDocs(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "create", "--project", projectID, "--title", "Add auth")
	planAgent(t, r, agent.Task{Title: "Add auth"}, agent.Task{Title: "Ship login"})

	out := r.run("prompt", "--project", projectID, "--yes", "-o", "json", "build", "login")
	var groups map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &groups); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out)
	}
	for _, key := range []string{"created", "skipped"} {
		if _, err := parseTaskDocs(groups[key], "json"); err != nil {
			t.Fatalf("prompt %s documents are not strict task docs: %v\n%s", key, err, out)
		}
	}
}

type promptGroups struct {
	Created []promptOutcome `json:"created" yaml:"created"`
	Skipped []promptOutcome `json:"skipped" yaml:"skipped"`
}

type promptOutcome struct {
	Title     string `json:"title" yaml:"title"`
	ProjectID string `json:"project_id" yaml:"project_id"`
}

func parsePromptJSON(t *testing.T, out string) promptGroups {
	t.Helper()
	var groups promptGroups
	if err := json.Unmarshal([]byte(out), &groups); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out)
	}
	return groups
}

func parsePromptYAML(t *testing.T, out string) promptGroups {
	t.Helper()
	var groups promptGroups
	if err := yaml.Unmarshal([]byte(out), &groups); err != nil {
		t.Fatalf("decode yaml: %v\n%s", err, out)
	}
	return groups
}

func TestPromptApplyWarnsAboutSkipped(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	planAgent(t, r, agent.Task{Title: "Add auth"}, agent.Task{Title: "Ship login"})
	r.run("prompt", "--project", projectID, "--yes", "build", "login")

	stdout, stderr := r.runSplit("prompt", "--project", projectID, "--yes", "build", "login")
	if !strings.Contains(stderr, "skipped 2 task(s) already present in "+projectID) {
		t.Fatalf("a re-run should warn about the skipped duplicates on stderr:\n%s", stderr)
	}
	if strings.Contains(stdout, "warning") {
		t.Fatalf("warnings must stay off stdout so structured output parses:\n%s", stdout)
	}
}

func TestPromptApplySkipsExistingTitleCaseAndWhitespaceInsensitive(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "create", "--project", projectID, "--title", "Add Auth")
	planAgent(t, r, agent.Task{Title: "  add auth  "})

	out := r.run("prompt", "--project", projectID, "--yes", "build", "login")
	if !strings.Contains(out, "skipped: true") {
		t.Fatalf("a case/whitespace variant of an existing title should be skipped:\n%s", out)
	}
	list := r.run("task", "list", "--project", projectID)
	if got := strings.Count(strings.ToLower(list), "add auth"); got != 1 {
		t.Fatalf("a variant title created a duplicate (appears %d times):\n%s", got, list)
	}
}

func TestPromptApplyDeduplicatesWithinPlan(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	planAgent(t, r, agent.Task{Title: "Add auth"}, agent.Task{Title: "add auth"})

	r.run("prompt", "--project", projectID, "--yes", "build", "login")

	list := r.run("task", "list", "--project", projectID)
	if got := strings.Count(strings.ToLower(list), "add auth"); got != 1 {
		t.Fatalf("a repeated title in one plan appears %d times, want 1:\n%s", got, list)
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

// TestPromptErrorExitCodes pins the process exit codes a caller may branch on,
// so a change in error wrapping cannot silently move them.
func TestPromptErrorExitCodes(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, r *runner)
		want  int
	}{
		{
			name: "invalid plan does not partially apply",
			setup: func(t *testing.T, r *runner) {
				planAgent(t, r, agent.Task{Title: "Good"}, agent.Task{Kind: "epic", Title: "Bad"})
			},
			want: 5,
		},
		{
			name: "unknown provider is an internal error",
			setup: func(t *testing.T, r *runner) {
				if err := os.WriteFile(r.userPath, []byte("[agent]\nprovider = \"bogus\"\n"), 0o600); err != nil {
					t.Fatalf("write user config: %v", err)
				}
			},
			want: 1,
		},
		{
			name:  "no agent configured is a usage error",
			setup: func(t *testing.T, r *runner) {},
			want:  2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			projectID := firstField(t, r.run("project", "create", "Acme"))
			tc.setup(t, r)

			_, _, err := executePrompt(t, r, "prompt", "--yes", "--project", projectID, "build", "login")
			if err == nil {
				t.Fatal("Execute() error = nil, want an error")
			}
			if got := ExitCode(err); got != tc.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", err, got, tc.want)
			}
		})
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
