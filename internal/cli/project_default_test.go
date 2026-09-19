package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProjectConfig(t *testing.T, projectID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "project.toml")
	if err := os.WriteFile(path, []byte("project = \""+projectID+"\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	return path
}

func TestTaskCreateUsesConfiguredProject(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	cfgPath := writeProjectConfig(t, projectID)

	taskID := firstField(t, r.run("--config", cfgPath, "task", "create", "-t", "one"))
	if listed := r.run("task", "list", "--project", projectID); !strings.Contains(listed, taskID) {
		t.Fatalf("task was not created in the configured project:\n%s", listed)
	}
}

func TestTaskCreateRequiresProjectWithoutDefault(t *testing.T) {
	r := newRunner(t)
	if err := r.runErr("task", "create", "-t", "x"); err == nil {
		t.Fatal("expected an error when no project is given and none is configured")
	}
}

func TestTaskListDefaultsToConfiguredProject(t *testing.T) {
	r := newRunner(t)
	alpha := firstField(t, r.run("project", "create", "Alpha"))
	beta := firstField(t, r.run("project", "create", "Beta"))
	alphaTask := firstField(t, r.run("task", "create", "-p", alpha, "-t", "a"))
	betaTask := firstField(t, r.run("task", "create", "-p", beta, "-t", "b"))
	cfgPath := writeProjectConfig(t, alpha)

	out := r.run("--config", cfgPath, "task", "list")
	if !strings.Contains(out, alphaTask) || strings.Contains(out, betaTask) {
		t.Fatalf("task list should default to the configured project:\n%s", out)
	}
}

func TestMilestoneCreateUsesConfiguredProject(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	cfgPath := writeProjectConfig(t, projectID)

	milestoneID := firstField(t, r.run("--config", cfgPath, "milestone", "create", "-t", "v1"))
	if listed := r.run("milestone", "list", "--project", projectID); !strings.Contains(listed, milestoneID) {
		t.Fatalf("milestone was not created in the configured project:\n%s", listed)
	}
}
