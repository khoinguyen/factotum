package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// tableRow returns the first output line containing needle, or "" when none does.
func tableRow(out, needle string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// seedGroomSession runs one grooming session with the fake backend, which writes
// the two outputs and creates one produced task, and returns the session id and
// the produced task id.
func seedGroomSession(t *testing.T, r *runner, projectID, cfgPath, producedTitle string) (sessionID, producedID string) {
	t.Helper()
	promptPath := t.TempDir() + "/prompt.md"
	mustWrite(t, promptPath, "# Grooming session prompt\n\nYou are the team lead.\n")

	base := isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: 0})
	backend := groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
		prompt := cmd.Argv[len(cmd.Argv)-1]
		mustWrite(t, kickoffPath(prompt, "Write the report to: "),
			"# Grooming report - 2026-10-08\n\n## Summary\n\n- items groomed: 1\n\n"+
				"## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n")
		mustWrite(t, kickoffPath(prompt, "Write the deferred questions to: "),
			"# Deferred questions\n\n## Questions\n\n- Which cache? (item: t-x, owner: PO)\n\n## Resolved\n")
		producedID = firstField(t, r.run("task", "create", "-p", projectID, "-t", producedTitle))
	}}
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	sessionID = firstField(t, out)
	return sessionID, producedID
}

func TestGroomListShowsSessions(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	sessionID, _ := seedGroomSession(t, r, projectID, cfgPath, "Extract cache module")

	out := r.run("--config", cfgPath, "groom", "list", "-p", projectID)
	for _, want := range []string{"SESSION", "DATE", "MODE", "SCOPE", "PRODUCED", sessionID, "interactive",
		time.Now().UTC().Format("2006-01-02")} {
		if !strings.Contains(out, want) {
			t.Fatalf("groom list output missing %q:\n%s", want, out)
		}
	}
	row := tableRow(out, sessionID)
	if row == "" {
		t.Fatalf("groom list has no row for %s:\n%s", sessionID, out)
	}
	// Session, date, mode, scope count, produced count: one scoped idea, one
	// produced task.
	fields := strings.Fields(row)
	if len(fields) != 5 || fields[3] != "1" || fields[4] != "1" {
		t.Fatalf("groom list row = %q, want the scope and produced counts both 1", row)
	}
}

func TestGroomListJSONIsStable(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Maybe cache"))
	sessionID, producedID := seedGroomSession(t, r, projectID, cfgPath, "Extract cache module")

	var docs []struct {
		Session  string   `json:"session"`
		Created  string   `json:"created"`
		Mode     string   `json:"mode"`
		Project  string   `json:"project"`
		Scope    []string `json:"scope"`
		Produced []string `json:"produced"`
	}
	if err := json.Unmarshal([]byte(r.run("--config", cfgPath, "-o", "json", "groom", "list", "-p", projectID)), &docs); err != nil {
		t.Fatalf("groom list -o json: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("groom list returned %d sessions, want 1: %+v", len(docs), docs)
	}
	got := docs[0]
	if got.Session != sessionID || got.Project != projectID || got.Mode != "interactive" {
		t.Fatalf("session doc = %+v, want session %s project %s mode interactive", got, sessionID, projectID)
	}
	if got.Created == "" {
		t.Fatal("session doc has no created timestamp")
	}
	if len(got.Scope) != 1 || got.Scope[0] != ideaID {
		t.Fatalf("scope = %v, want [%s]", got.Scope, ideaID)
	}
	if len(got.Produced) != 1 || got.Produced[0] != producedID {
		t.Fatalf("produced = %v, want [%s]", got.Produced, producedID)
	}
}

func TestGroomShowPrintsReportDeferredAndProduced(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	sessionID, producedID := seedGroomSession(t, r, projectID, cfgPath, "Extract cache module")

	out := r.run("--config", cfgPath, "groom", "show", sessionID)
	for _, want := range []string{
		"session: " + sessionID,
		"report: art-",
		"deferred: art-",
		"## Summary",
		"items groomed: 1",
		"## Questions",
		"Which cache?",
		producedID,
		"Extract cache module",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("groom show output missing %q:\n%s", want, out)
		}
	}
}

func TestGroomShowJSONCarriesBodiesAndTasks(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	sessionID, producedID := seedGroomSession(t, r, projectID, cfgPath, "Extract cache module")

	var doc struct {
		Session      string `json:"session"`
		Mode         string `json:"mode"`
		Project      string `json:"project"`
		ReportBody   string `json:"report_body"`
		DeferredBody string `json:"deferred_body"`
		Produced     []struct {
			TaskID string `json:"task_id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"produced"`
	}
	if err := json.Unmarshal([]byte(r.run("--config", cfgPath, "-o", "json", "groom", "show", sessionID)), &doc); err != nil {
		t.Fatalf("groom show -o json: %v", err)
	}
	if doc.Session != sessionID || doc.Project != projectID {
		t.Fatalf("show doc = %+v, want session %s project %s", doc, sessionID, projectID)
	}
	if !strings.Contains(doc.ReportBody, "## Summary") {
		t.Fatalf("report_body missing report content:\n%s", doc.ReportBody)
	}
	if !strings.Contains(doc.DeferredBody, "## Questions") {
		t.Fatalf("deferred_body missing deferred content:\n%s", doc.DeferredBody)
	}
	if len(doc.Produced) != 1 || doc.Produced[0].TaskID != producedID || doc.Produced[0].Title != "Extract cache module" {
		t.Fatalf("produced = %+v, want the task %s", doc.Produced, producedID)
	}
}

func TestGroomShowUnknownSessionErrors(t *testing.T) {
	r := newRunner(t)
	_, cfgPath := tasklessContext(t, r)
	err := r.runErr("--config", cfgPath, "groom", "show", "groom-missing")
	if err == nil {
		t.Fatal("groom show of an unknown session error = nil, want an error")
	}
}
