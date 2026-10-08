package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/internal/groom"
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
		stageGroomOutputs(t, base, prompt,
			"# Grooming report - 2026-10-08\n\n## Summary\n\n- items groomed: 1\n\n"+
				"## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n",
			"# Deferred questions\n\n## Questions\n\n- Which cache? (item: t-x, owner: PO)\n\n## Resolved\n")
		if producedTitle != "" {
			producedID = firstField(t, r.run("task", "create", "-p", projectID, "-t", producedTitle))
		}
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
	for _, want := range []string{"SESSION", "DATE", "MODE", "SCOPE", "PRODUCED", sessionID, "headless",
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
	if got.Session != sessionID || got.Project != projectID || got.Mode != "headless" {
		t.Fatalf("session doc = %+v, want session %s project %s mode headless", got, sessionID, projectID)
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
			TicketID string `json:"task_id"`
			Title    string `json:"title"`
			Status   string `json:"status"`
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
	if len(doc.Produced) != 1 || doc.Produced[0].TicketID != producedID || doc.Produced[0].Title != "Extract cache module" {
		t.Fatalf("produced = %+v, want the task %s", doc.Produced, producedID)
	}
}

// TestGroomListJSONNormalizesEmptyProduced pins the machine shape: a session
// that produced nothing must serialize `produced` as an empty array, not null,
// so a JSON consumer can iterate it without a nil check.
func TestGroomListJSONNormalizesEmptyProduced(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	seedGroomSession(t, r, projectID, cfgPath, "")

	out := r.run("--config", cfgPath, "-o", "json", "groom", "list", "-p", projectID)
	if strings.Contains(out, "\"produced\": null") {
		t.Fatalf("groom list -o json emitted a null produced:\n%s", out)
	}
	if !strings.Contains(out, "\"produced\": []") {
		t.Fatalf("groom list -o json produced is not an empty array:\n%s", out)
	}
}

// TestGroomShowDeletedProducedTaskIsIdOnly pins that show still names a produced
// task that has since been deleted, rather than dropping it or failing.
func TestGroomShowDeletedProducedTaskIsIdOnly(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	sessionID, producedID := seedGroomSession(t, r, projectID, cfgPath, "Extract cache module")

	r.run("task", "delete", producedID)

	out := r.run("--config", cfgPath, "groom", "show", sessionID)
	if !strings.Contains(out, producedID) {
		t.Fatalf("groom show dropped a deleted produced task's id:\n%s", out)
	}

	var doc struct {
		Produced []struct {
			TicketID string `json:"task_id"`
			Title    string `json:"title"`
		} `json:"produced"`
	}
	if err := json.Unmarshal([]byte(r.run("--config", cfgPath, "-o", "json", "groom", "show", sessionID)), &doc); err != nil {
		t.Fatalf("groom show -o json: %v", err)
	}
	if len(doc.Produced) != 1 || doc.Produced[0].TicketID != producedID || doc.Produced[0].Title != "" {
		t.Fatalf("produced = %+v, want the deleted task %s by id alone", doc.Produced, producedID)
	}
}

// TestGroomShowFallsBackToSessionFiles pins sessionOutputBody's fallback: when
// the recorded output artifact is gone but the session file survives, show reads
// the file rather than failing.
func TestGroomShowFallsBackToSessionFiles(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	dataDir := filepath.Dir(r.path)
	sessionID := "groom-manual"
	rec := groom.SessionRecord{
		ID:        sessionID,
		Project:   projectID,
		Mode:      "interactive",
		CreatedAt: time.Now().UTC(),
		Scope:     []groom.ScopeItem{{ID: "t-x", Kind: "idea", Title: "x"}},
		Report:    "art-missing",
		Deferred:  "art-missing-too",
	}
	if err := groom.WriteSession(dataDir, rec); err != nil {
		t.Fatalf("WriteSession error = %v", err)
	}
	mustWrite(t, groom.ReportPath(dataDir, sessionID), "# Grooming report - manual\n\n## Summary\n")
	mustWrite(t, groom.DeferredQuestionsPath(dataDir, sessionID), "# Deferred questions\n\n## Questions\n")

	out := r.run("--config", cfgPath, "groom", "show", sessionID)
	for _, want := range []string{"# Grooming report - manual", "# Deferred questions"} {
		if !strings.Contains(out, want) {
			t.Fatalf("groom show did not fall back to the session file %q:\n%s", want, out)
		}
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
