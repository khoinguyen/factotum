package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageAgentRegisterClaimAck(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	actorID := firstField(t, r.run("actor", "create", "claude", "--kind", "agent"))
	r.run("task", "create", "--project", projectID, "--title", "work")

	sent := r.run("msg", "send", "actor:"+actorID, "hello", "--project", projectID)
	messageID := firstField(t, sent)

	registered := runJSON(t, r, "msg", "agent", "register", "--actor", actorID, "--harness", "opencode", "--project", projectID)
	runID, _ := registered["run_id"].(string)
	if runID == "" {
		t.Fatalf("register json missing run_id: %v", registered)
	}

	claimed := runJSON(t, r, "msg", "agent", "claim", "--run", runID, "--actor", actorID, "--project", projectID)
	if claimed["found"] != true {
		t.Fatalf("claim found = %v, want true: %v", claimed["found"], claimed)
	}
	message, ok := claimed["message"].(map[string]any)
	if !ok || message["id"] != messageID || message["body"] != "hello" {
		t.Fatalf("claim message = %v, want id %s body hello", claimed["message"], messageID)
	}

	acked := runJSON(t, r, "msg", "agent", "ack", messageID, "--run", runID, "--state", "read", "--project", projectID)
	if acked["ok"] != true {
		t.Fatalf("ack ok = %v, want true: %v", acked["ok"], acked)
	}

	read := r.run("msg", "inbox", "--project", projectID, "--state", "read")
	if !strings.Contains(read, messageID) {
		t.Fatalf("inbox should list the read message:\n%s", read)
	}

	empty := runJSON(t, r, "msg", "agent", "claim", "--run", runID, "--actor", actorID, "--project", projectID)
	if empty["found"] != false {
		t.Fatalf("second claim found = %v, want false", empty["found"])
	}

	if out := runJSON(t, r, "msg", "agent", "heartbeat", "--run", runID, "--project", projectID); out["ok"] != true {
		t.Fatalf("heartbeat ok = %v, want true", out["ok"])
	}
	if out := runJSON(t, r, "msg", "agent", "deregister", "--run", runID, "--project", projectID); out["ok"] != true {
		t.Fatalf("deregister ok = %v, want true", out["ok"])
	}
}

func TestMessageAgentClaimDefaultsToConfiguredProject(t *testing.T) {
	// Without -p the agent verbs fall back to the configured project.
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	actorID := firstField(t, r.run("actor", "create", "claude", "--kind", "agent"))
	r.run("msg", "send", "actor:"+actorID, "hello", "--project", projectID)

	mustWrite(t, r.projectPath, "project = \""+projectID+"\"\n")
	registered := runJSON(t, r, "msg", "agent", "register", "--actor", actorID)
	runID, _ := registered["run_id"].(string)
	claimed := runJSON(t, r, "msg", "agent", "claim", "--run", runID, "--actor", actorID)
	if claimed["found"] != true {
		t.Fatalf("claim found = %v, want true", claimed["found"])
	}
}

// runJSON runs a command with -o json and decodes the single JSON object.
func runJSON(t *testing.T, r *runner, args ...string) map[string]any {
	t.Helper()
	out := r.run(append(args, "-o", "json")...)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode json output %q: %v", out, err)
	}
	return decoded
}
