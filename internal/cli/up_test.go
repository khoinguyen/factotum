package cli

import (
	"strings"
	"testing"
)

func TestUpStartStatusAndList(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a spark", "-b", "context"))

	started := r.run("up", "start", ideaID, "-p", projectID)
	pipelineID := firstField(t, started)
	if pipelineID == "" || pipelineID == "(none)" {
		t.Fatalf("up start should enqueue a pipeline:\n%s", started)
	}
	if !strings.Contains(started, "state: queued") {
		t.Fatalf("up start should report state: queued:\n%s", started)
	}

	status := r.run("up", "status", "-p", projectID)
	for _, want := range []string{
		"pipeline: " + pipelineID,
		"capture: " + ideaID,
		"state: queued",
		"gate: -",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("up status missing %q:\n%s", want, status)
		}
	}

	list := r.run("up", "list", "-p", projectID)
	if !strings.Contains(list, pipelineID) || !strings.Contains(list, "PIPELINE") {
		t.Fatalf("up list should table the pipeline:\n%s", list)
	}
}

func TestUpStartSkipsExecutableTicket(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "real work"))

	out := r.run("up", "start", taskID, "-p", projectID)
	if !strings.Contains(out, "pipeline: (none)") {
		t.Fatalf("up start on an executable ticket should skip:\n%s", out)
	}
}
