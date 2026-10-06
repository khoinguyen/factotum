package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskGroomedRequiresAcceptanceCriteria(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	if err := r.runErr("task", "create", "-p", projectID, "-t", "x", "--groomed"); err == nil {
		t.Fatal("create --groomed without acceptance criteria should fail")
	}

	out := r.run("task", "create", "-p", projectID, "-t", "y", "--groomed", "--acceptance", "it works", "--acceptance", "it is tested")
	taskID := firstField(t, out)

	get := r.run("task", "get", taskID, "-o", "json")
	var doc struct {
		Groomed            bool     `json:"groomed"`
		AcceptanceCriteria []string `json:"acceptance_criteria"`
	}
	if err := json.Unmarshal([]byte(get), &doc); err != nil {
		t.Fatalf("task get json: %v\n%s", err, get)
	}
	if !doc.Groomed || len(doc.AcceptanceCriteria) != 2 {
		t.Fatalf("groomed = %v criteria = %v, want true/[it works it is tested]", doc.Groomed, doc.AcceptanceCriteria)
	}
}

func TestTaskUpdateGroomedAndUngroomed(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "x"))

	if err := r.runErr("task", "update", taskID, "--groomed"); err == nil {
		t.Fatal("update --groomed without criteria should fail")
	}

	r.run("task", "update", taskID, "--groomed", "--acceptance", "verified")
	if out := r.run("task", "get", taskID); !strings.Contains(out, "groomed: true") {
		t.Fatalf("task get missing groomed: true:\n%s", out)
	}

	r.run("task", "update", taskID, "--ungroomed")
	if out := r.run("task", "get", taskID); strings.Contains(out, "groomed: true") {
		t.Fatalf("task get still groomed after --ungroomed:\n%s", out)
	}
}

func TestTaskListFiltersByGroomed(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	groomed := firstField(t, r.run("task", "create", "-p", projectID, "-t", "g", "--groomed", "--acceptance", "done"))
	ungroomed := firstField(t, r.run("task", "create", "-p", projectID, "-t", "u"))

	onlyGroomed := r.run("task", "list", "-p", projectID, "--groomed")
	if !strings.Contains(onlyGroomed, groomed) || strings.Contains(onlyGroomed, ungroomed) {
		t.Fatalf("list --groomed wrong:\n%s", onlyGroomed)
	}
	onlyUngroomed := r.run("task", "list", "-p", projectID, "--ungroomed")
	if !strings.Contains(onlyUngroomed, ungroomed) || strings.Contains(onlyUngroomed, groomed) {
		t.Fatalf("list --ungroomed wrong:\n%s", onlyUngroomed)
	}

	if err := r.runErr("task", "list", "-p", projectID, "--groomed", "--ungroomed"); err == nil {
		t.Fatal("--groomed with --ungroomed should be rejected")
	}
}

func TestTaskNextFiltersByGroomed(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	groomed := firstField(t, r.run("task", "create", "-p", projectID, "-t", "g", "--groomed", "--acceptance", "done"))
	ungroomed := firstField(t, r.run("task", "create", "-p", projectID, "-t", "u"))

	onlyGroomed := r.run("task", "next", "-p", projectID, "--groomed")
	if !strings.Contains(onlyGroomed, groomed) || strings.Contains(onlyGroomed, ungroomed) {
		t.Fatalf("next --groomed wrong:\n%s", onlyGroomed)
	}
	onlyUngroomed := r.run("task", "next", "-p", projectID, "--ungroomed")
	if !strings.Contains(onlyUngroomed, ungroomed) || strings.Contains(onlyUngroomed, groomed) {
		t.Fatalf("next --ungroomed wrong:\n%s", onlyUngroomed)
	}
}

func TestTaskNextForAgentSkipsUngroomed(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "agent", "claude")

	ungroomed := firstField(t, r.run("task", "create", "-p", projectID, "-t", "u"))
	r.run("task", "assign", ungroomed, "--actor", "claude")
	groomed := firstField(t, r.run("task", "create", "-p", projectID, "-t", "g", "--groomed", "--acceptance", "done"))
	r.run("task", "assign", groomed, "--actor", "claude")

	out := r.run("task", "next", "--for", "claude", "-p", projectID)
	if strings.Contains(out, ungroomed) {
		t.Fatalf("--for agent must not offer ungroomed work:\n%s", out)
	}
	if !strings.Contains(out, groomed) {
		t.Fatalf("--for agent must offer groomed work:\n%s", out)
	}
}

func TestTaskClaimForAgentSkipsUngroomed(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "agent", "claude")
	ungroomed := firstField(t, r.run("task", "create", "-p", projectID, "-t", "u"))

	// Only ungroomed work is available, so an agent cannot claim anything.
	if err := r.runErr("task", "claim", "--for", "claude", "-p", projectID); err == nil {
		t.Fatal("agent claim should find no groomed work")
	}

	// A groomed task becomes claimable.
	groomed := firstField(t, r.run("task", "create", "-p", projectID, "-t", "g", "--groomed", "--acceptance", "done"))
	out := r.run("task", "claim", "--for", "claude", "-p", projectID)
	if !strings.Contains(out, groomed) || strings.Contains(out, ungroomed) {
		t.Fatalf("agent claim picked the wrong task:\n%s", out)
	}
}

func TestTaskApplySetsGroomedCriteria(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "x"))

	doc := "id: " + taskID + "\ngroomed: true\nacceptance_criteria:\n  - observable\n  - testable\n"
	path := filepath.Join(t.TempDir(), "task.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	r.run("task", "apply", "-f", path)

	out := r.run("task", "get", taskID, "-o", "json")
	if !strings.Contains(out, `"groomed": true`) || !strings.Contains(out, `"observable"`) || !strings.Contains(out, `"testable"`) {
		t.Fatalf("apply did not set groomed criteria:\n%s", out)
	}
}
