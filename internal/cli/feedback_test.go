package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/feedback"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
	"github.com/khoinguyen/factotum/pkg/version"
)

// writeFeedbackConfig writes a machine-scoped config mapping each project to a
// jsonfile sink path.
func writeFeedbackConfig(t *testing.T, r *runner, sinks map[string]string) {
	t.Helper()
	var b strings.Builder
	for project, path := range sinks {
		fmt.Fprintf(&b, "[projects.%s.store]\nbackend = \"jsonfile\"\noptions = { path = %q }\n\n", project, path)
	}
	if err := os.WriteFile(r.userPath, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
}

// storedTasks reads every task from a jsonfile store.
func storedTasks(t *testing.T, path string) []*core.Task {
	t.Helper()
	backend, err := jsonfile.Open(context.Background(), store.Config{Options: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("open store %s: %v", path, err)
	}
	defer func() { _ = backend.Close() }()
	tasks, err := backend.Tasks().List(context.Background(), store.TaskFilter{})
	if err != nil {
		t.Fatalf("list tasks in %s: %v", path, err)
	}
	return tasks
}

func singleStoredTask(t *testing.T, path string) *core.Task {
	t.Helper()
	tasks := storedTasks(t, path)
	if len(tasks) != 1 {
		t.Fatalf("store %s has %d tasks, want 1", path, len(tasks))
	}
	return tasks[0]
}

func containsTaskMessage(tasks []*core.Task, message string) bool {
	for _, task := range tasks {
		if strings.Contains(task.Description, message) || strings.Contains(task.Title, message) {
			return true
		}
	}
	return false
}

func TestFeedbackCreateStoresInFactotumNotCaller(t *testing.T) {
	r := newRunner(t)
	sink := filepath.Join(t.TempDir(), "factotum.json")
	writeFeedbackConfig(t, r, map[string]string{"factotum": sink})

	out := r.run("feedback", "create", "-b", "blocked tasks still appear in next")

	if !strings.HasPrefix(firstField(t, out), "t-") {
		t.Fatalf("feedback create did not print a task id:\n%s", out)
	}
	if !strings.Contains(out, "project: factotum") {
		t.Fatalf("feedback create did not report the sink project:\n%s", out)
	}
	if containsTaskMessage(storedTasks(t, r.path), "blocked tasks still appear in next") {
		t.Fatalf("feedback landed in the caller's store %s", r.path)
	}
	if !containsTaskMessage(storedTasks(t, sink), "blocked tasks still appear in next") {
		t.Fatalf("feedback did not land in the factotum store %s", sink)
	}
}

func TestFeedbackCreateLabelsExternalFeedback(t *testing.T) {
	r := newRunner(t)
	sink := filepath.Join(t.TempDir(), "factotum.json")
	writeFeedbackConfig(t, r, map[string]string{"factotum": sink})

	r.run("feedback", "create", "-b", "a bug")

	task := singleStoredTask(t, sink)
	if !store.MatchLabels(*task, []string{feedback.LabelExternalFeedback}) {
		t.Fatalf("labels = %v, want %s", task.Labels, feedback.LabelExternalFeedback)
	}
}

func TestFeedbackCreateBodyFields(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"message", []string{"-b", "the message text"}, "the message text"},
		{"flow", []string{"-b", "a bug", "--flow", "ft task next"}, "flow: ft task next"},
		{"version", []string{"-b", "a bug"}, "ft version: " + version.Version},
		{"project", []string{"-b", "a bug", "-p", "acme"}, "project: acme"},
		{"repo", []string{"-b", "a bug", "-r", "widget"}, "repo: widget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			sink := filepath.Join(t.TempDir(), "factotum.json")
			writeFeedbackConfig(t, r, map[string]string{"factotum": sink})

			r.run(append([]string{"feedback", "create"}, tc.args...)...)

			task := singleStoredTask(t, sink)
			if !strings.Contains(task.Description, tc.want) {
				t.Fatalf("stored body = %q, want it to contain %q", task.Description, tc.want)
			}
		})
	}
}

func TestFeedbackCreateStoreOverride(t *testing.T) {
	r := newRunner(t)
	sink := filepath.Join(t.TempDir(), "factotum.json")
	other := filepath.Join(t.TempDir(), "other.json")
	writeFeedbackConfig(t, r, map[string]string{"factotum": sink, "other": other})

	r.run("feedback", "create", "-b", "routed elsewhere", "--feedback-store", "other")

	if containsTaskMessage(storedTasks(t, sink), "routed elsewhere") {
		t.Fatal("feedback landed in the default factotum store despite --feedback-store")
	}
	if !containsTaskMessage(storedTasks(t, other), "routed elsewhere") {
		t.Fatal("feedback did not land in the overridden store")
	}
}

func TestFeedbackCreateUsesAddedTransport(t *testing.T) {
	r := newRunner(t)
	sink := filepath.Join(t.TempDir(), "factotum.json")
	writeFeedbackConfig(t, r, map[string]string{"factotum": sink})

	var got feedback.Report
	r.feedbackFactory = func(feedback.Env) (feedback.Transport, error) {
		return feedbackTransportFunc(func(_ context.Context, report feedback.Report) (*core.Task, error) {
			got = report
			return &core.Task{ID: "t-fake", ProjectID: "factotum", Kind: core.KindTask, Title: report.Title(), Status: core.StatusTodo}, nil
		}), nil
	}

	out := r.run("feedback", "create", "-b", "through the plugin", "--transport", "fake")

	if got.Message != "through the plugin" {
		t.Fatalf("added transport received %+v, want the report", got)
	}
	if firstField(t, out) != "t-fake" {
		t.Fatalf("feedback create did not print the transport's task id:\n%s", out)
	}
}

func TestFeedbackCreateFailsWithoutSink(t *testing.T) {
	r := newRunner(t)
	err := r.runErr("feedback", "create", "-b", "nowhere to go")
	if err == nil {
		t.Fatal("feedback create error = nil, want a clear failure")
	}
	if !strings.Contains(err.Error(), "feedback store") {
		t.Fatalf("error = %v, want it to name the feedback store", err)
	}
	if len(storedTasks(t, r.path)) != 0 {
		t.Fatal("a failed report touched the caller's store")
	}
}

func TestFeedbackCreateFailsWithUnreachableSink(t *testing.T) {
	r := newRunner(t)
	if err := os.WriteFile(r.userPath, []byte("[projects.factotum.store]\nbackend = \"nope\"\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	err := r.runErr("feedback", "create", "-b", "nowhere to go")
	if err == nil {
		t.Fatal("feedback create error = nil, want a clear failure")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v, want it to name the bad backend", err)
	}
	if len(storedTasks(t, r.path)) != 0 {
		t.Fatal("a failed report touched the caller's store")
	}
}

func TestFeedbackCreateRedactsSensitiveBody(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "HOME" {
			return "/home/khoi"
		}
		return ""
	}
	sink := filepath.Join(t.TempDir(), "factotum.json")
	writeFeedbackConfig(t, r, map[string]string{"factotum": sink})

	r.run("feedback", "create", "-b", "leak /home/khoi/.factotum/config.toml and ghp_abcdefghijklmnopqrstuvwxyz012345")

	task := singleStoredTask(t, sink)
	if strings.Contains(task.Description, "/home/khoi") {
		t.Fatalf("stored body leaked the home path: %q", task.Description)
	}
	if strings.Contains(task.Description, "ghp_abcdefghijklmnopqrstuvwxyz012345") {
		t.Fatalf("stored body leaked the token: %q", task.Description)
	}
	if !strings.Contains(task.Description, "$HOME") || !strings.Contains(task.Description, "[redacted]") {
		t.Fatalf("stored body = %q, want the redaction markers", task.Description)
	}
}

func TestFeedbackCreateSurfacesSendFailure(t *testing.T) {
	r := newRunner(t)
	sink := filepath.Join(t.TempDir(), "factotum.json")
	writeFeedbackConfig(t, r, map[string]string{"factotum": sink})
	r.feedbackFactory = func(feedback.Env) (feedback.Transport, error) {
		return feedbackTransportFunc(func(context.Context, feedback.Report) (*core.Task, error) {
			return nil, errors.New("sink exploded")
		}), nil
	}

	err := r.runErr("feedback", "create", "-b", "do not drop me", "--transport", "fake")
	if err == nil {
		t.Fatal("feedback create error = nil, want the send failure")
	}
	if !strings.Contains(err.Error(), "sink exploded") {
		t.Fatalf("error = %v, want it to surface the transport failure", err)
	}
	if len(storedTasks(t, sink)) != 0 {
		t.Fatal("a failed send wrote to the sink")
	}
}

func TestFeedbackCreateRejectsEmptyBody(t *testing.T) {
	r := newRunner(t)
	sink := filepath.Join(t.TempDir(), "factotum.json")
	writeFeedbackConfig(t, r, map[string]string{"factotum": sink})
	if err := r.runErr("feedback", "create", "-b", "   "); !errors.Is(err, ErrUsage) {
		t.Fatalf("empty body error = %v, want ErrUsage", err)
	}
	if len(storedTasks(t, sink)) != 0 {
		t.Fatal("an empty report wrote to the sink")
	}
}

// feedbackTransportFunc adapts a function to the feedback.Transport interface.
type feedbackTransportFunc func(context.Context, feedback.Report) (*core.Task, error)

func (f feedbackTransportFunc) Send(ctx context.Context, report feedback.Report) (*core.Task, error) {
	return f(ctx, report)
}
