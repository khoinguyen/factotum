package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestSuggestWritesAlignedBlock(t *testing.T) {
	var buf bytes.Buffer
	deps := &Deps{Err: &buf}
	deps.suggest(
		hint{Command: "ft a", About: "first"},
		hint{Command: "ft bb", About: "second"},
	)
	want := "\nNext:\n  ft a   first\n  ft bb  second\n"
	if got := buf.String(); got != want {
		t.Fatalf("suggest() =\n%q\nwant\n%q", got, want)
	}
}

func TestSuggestOmitsPaddingWithoutAbout(t *testing.T) {
	var buf bytes.Buffer
	deps := &Deps{Err: &buf}
	deps.suggest(hint{Command: "ft a", About: "x"}, hint{Command: "ft bb"})
	if got := buf.String(); !strings.Contains(got, "  ft bb\n") {
		t.Fatalf("hint without an about must not be padded:\n%q", got)
	}
}

func TestSuggestSuppressed(t *testing.T) {
	cases := map[string]*Deps{
		"json": {OutputFormat: "json"},
		"flag": {NoHints: true},
	}
	for name, deps := range cases {
		var buf bytes.Buffer
		deps.Err = &buf
		deps.suggest(hint{Command: "ft task next"})
		if buf.Len() != 0 {
			t.Fatalf("%s: expected no suggestions, got %q", name, buf.String())
		}
	}
}

func TestHintsAfterTaskNext(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "one"))

	stdout, stderr := r.runSplit("task", "next", "--project", projectID)
	if strings.Contains(stdout, "Next:") {
		t.Fatalf("suggestions must not go to stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "Next:") {
		t.Fatalf("task next missing suggestions:\n%s", stderr)
	}
	if !strings.Contains(stderr, "ft task get "+taskID) {
		t.Fatalf("task next should suggest task get %s:\n%s", taskID, stderr)
	}
}

func TestHintsSuppressedForJSON(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "create", "--project", projectID, "--title", "one")
	if _, stderr := r.runSplit("task", "next", "--project", projectID, "-o", "json"); strings.Contains(stderr, "Next:") {
		t.Fatalf("json output should not carry suggestions:\n%s", stderr)
	}
}

func TestHintsSuppressedByFlag(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "create", "--project", projectID, "--title", "one")
	if _, stderr := r.runSplit("task", "next", "--project", projectID, "--no-hints"); strings.TrimSpace(stderr) != "" {
		t.Fatalf("--no-hints should silence suggestions, got:\n%s", stderr)
	}
}

func TestHintsAfterTaskAdd(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	_, stderr := r.runSplit("task", "create", "--project", projectID, "--title", "one")
	if !strings.Contains(stderr, "ft task next --project "+projectID) {
		t.Fatalf("task add should suggest task next:\n%s", stderr)
	}
}

func TestTaskGetGuidesWorkflow(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "one"))

	if _, stderr := r.runSplit("task", "get", taskID); !strings.Contains(stderr, "ft task start "+taskID) {
		t.Fatalf("todo task should suggest start:\n%s", stderr)
	}
	r.run("task", "start", taskID)
	if _, stderr := r.runSplit("task", "get", taskID); !strings.Contains(stderr, "ft task review "+taskID) {
		t.Fatalf("in-progress task should suggest review:\n%s", stderr)
	}
	r.run("task", "review", taskID)
	if _, stderr := r.runSplit("task", "get", taskID); !strings.Contains(stderr, "ft task done "+taskID) {
		t.Fatalf("in-review task should suggest done:\n%s", stderr)
	}
	r.run("task", "done", taskID)
	if _, stderr := r.runSplit("task", "get", taskID); !strings.Contains(stderr, "ft task next --project "+projectID) {
		t.Fatalf("resolved task should suggest next:\n%s", stderr)
	}
}

func TestActorAddUsesSlugID(t *testing.T) {
	r := newRunner(t)
	if got := firstField(t, r.run("actor", "create", "--kind", "agent", "Claude Code")); got != "claude-code" {
		t.Fatalf("actor id = %q, want claude-code", got)
	}
}

func TestTaskReopenReturnsToTodo(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "one"))

	r.run("task", "start", taskID)
	out := r.run("task", "reopen", taskID)
	if fields := strings.Fields(out); len(fields) != 2 || fields[0] != taskID || fields[1] != "todo" {
		t.Fatalf("task reopen = %q, want %s todo", out, taskID)
	}
	if show := r.run("task", "get", taskID); !strings.HasPrefix(show, "(todo)") {
		t.Fatalf("reopened task should be todo:\n%s", show)
	}
}

func TestTaskNextAllRanksAcrossProjects(t *testing.T) {
	r := newRunner(t)
	alpha := firstField(t, r.run("project", "create", "Alpha"))
	beta := firstField(t, r.run("project", "create", "Beta"))
	first := firstField(t, r.run("task", "create", "-p", alpha, "-t", "one"))
	second := firstField(t, r.run("task", "create", "-p", beta, "-t", "two"))

	out := r.run("task", "next", "--all")
	if !strings.Contains(out, first) || !strings.Contains(out, second) {
		t.Fatalf("task next --all should span projects:\n%s", out)
	}
	if out := r.run("task", "next", "-a"); !strings.Contains(out, first) || !strings.Contains(out, second) {
		t.Fatalf("task next -a should span projects:\n%s", out)
	}
}

func TestTaskNextRejectsProjectWithAll(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Alpha"))
	stdout, stderr := r.runSplit("task", "next", "--all", "-p", projectID)
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("usage errors must not write stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Fatalf("expected help when --all and --project combine:\n%s", stderr)
	}
}

func TestTaskNextRequiresProjectOrAll(t *testing.T) {
	r := newRunner(t)
	_, stderr := r.runSplit("task", "next")
	if !strings.Contains(stderr, "Usage:") {
		t.Fatalf("expected help when neither --project nor --all is given:\n%s", stderr)
	}
}

func TestTaskGetSuggestsBlockingDependency(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	depID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "dep"))
	blockedID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "blocked", "--dep", depID))

	_, stderr := r.runSplit("task", "get", blockedID)
	if !strings.Contains(stderr, "ft task get "+depID) {
		t.Fatalf("blocked task should suggest inspecting its dependency:\n%s", stderr)
	}
	if strings.Contains(stderr, "ft task start "+blockedID) {
		t.Fatalf("blocked task should not suggest starting:\n%s", stderr)
	}
}
