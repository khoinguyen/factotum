package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/groom"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
)

// seedGroomSessionWithCapture runs one grooming session whose window diff
// produces both a task and a filed idea capture, using the fake backend. It
// returns the session id, the produced task id, and the capture id.
func seedGroomSessionWithCapture(t *testing.T, r *runner, projectID, cfgPath string) (sessionID, taskID, captureID string) {
	t.Helper()
	promptPath := t.TempDir() + "/prompt.md"
	mustWrite(t, promptPath, "# Grooming session prompt\n\nYou are the team lead.\n")

	backend := writingGroomBackend(t)
	stage := backend.onExec
	backend.onExec = func(cmd isolation.Command) {
		stage(cmd)
		captureID = firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Maybe batch the cache"))
		taskID = firstField(t, r.run("task", "create", "-p", projectID, "-t", "Extract cache module"))
	}
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	sessionID = firstField(t, out)
	return sessionID, taskID, captureID
}

// TestGroomReviewRecordsVerdictAndFindings pins the happy path: the review is
// captured as a session artifact and a note on the origin idea, the verdict is
// stored on the session, and an approving verdict leaves the build unblocked.
func TestGroomReviewRecordsVerdictAndFindings(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Maybe cache"))
	sessionID, producedID := seedGroomSession(t, r, projectID, cfgPath, "Extract cache module")

	out := r.run("--config", cfgPath, "groom", "review", sessionID, "--verdict", "approve", "-b", "Design is sound.")
	for _, want := range []string{"session: " + sessionID, "verdict: approve", "review: art-"} {
		if !strings.Contains(out, want) {
			t.Fatalf("groom review output missing %q:\n%s", want, out)
		}
	}

	dataDir := filepath.Dir(r.path)
	if _, err := os.Stat(groom.ReviewPath(dataDir, sessionID)); err != nil {
		t.Fatalf("review not captured at %s: %v", groom.ReviewPath(dataDir, sessionID), err)
	}
	session, err := groom.ReadSession(dataDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.ReviewVerdict != "approve" || session.Review == "" {
		t.Fatalf("manifest review = %q/%q, want an approving verdict with an artifact id", session.Review, session.ReviewVerdict)
	}
	if len(session.Blocked) != 0 {
		t.Fatalf("manifest blocked = %v, want none on approve", session.Blocked)
	}

	docs := r.run("--config", cfgPath, "doc", "list", "-p", projectID)
	if !strings.Contains(docs, "Architecture review "+sessionID) {
		t.Fatalf("review artifact not recorded:\n%s", docs)
	}
	idea := r.run("--config", cfgPath, "task", "get", ideaID)
	for _, want := range []string{"=== Notes ===", "approve", "Design is sound."} {
		if !strings.Contains(idea, want) {
			t.Fatalf("origin idea missing the review finding %q:\n%s", want, idea)
		}
	}
	produced := r.run("--config", cfgPath, "task", "get", producedID)
	if !strings.Contains(produced, "(todo)") {
		t.Fatalf("approve must not block the produced task:\n%s", produced)
	}

	show := r.run("--config", cfgPath, "groom", "show", sessionID)
	for _, want := range []string{"review_verdict: approve", "=== Architecture review ===", "Design is sound."} {
		if !strings.Contains(show, want) {
			t.Fatalf("groom show missing %q:\n%s", want, show)
		}
	}
}

// TestGroomReviewNeedsReworkBlocksThenApproves pins the build gate: a
// needs-rework verdict blocks the tasks the session produced, and a later
// approving review unblocks exactly those.
func TestGroomReviewNeedsReworkBlocksThenApproves(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	sessionID, producedID := seedGroomSession(t, r, projectID, cfgPath, "Extract cache module")

	r.run("--config", cfgPath, "groom", "review", sessionID, "--verdict", "needs-rework", "-b", "Wrong boundary.")

	produced := r.run("--config", cfgPath, "task", "get", producedID)
	if !strings.Contains(produced, "(blocked)") {
		t.Fatalf("needs-rework must block the produced task:\n%s", produced)
	}
	dataDir := filepath.Dir(r.path)
	session, err := groom.ReadSession(dataDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Blocked) != 1 || session.Blocked[0] != producedID {
		t.Fatalf("manifest blocked = %v, want [%s]", session.Blocked, producedID)
	}

	r.run("--config", cfgPath, "groom", "review", sessionID, "--verdict", "approve", "-b", "Boundary fixed.")
	produced = r.run("--config", cfgPath, "task", "get", producedID)
	if !strings.Contains(produced, "(todo)") {
		t.Fatalf("a later approve must unblock the produced task:\n%s", produced)
	}
	session, err = groom.ReadSession(dataDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Blocked) != 0 {
		t.Fatalf("manifest blocked = %v, want none after approve", session.Blocked)
	}
}

// TestGroomReviewNeedsReworkSkipsCaptures pins the bug: the produced window
// diff includes any idea/bug the session filed, and captures reject a blocked
// status. A needs-rework verdict must block only the produced tasks and record
// those ids; a later approve unblocks exactly them and never touches the
// capture.
func TestGroomReviewNeedsReworkSkipsCaptures(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	sessionID, taskID, captureID := seedGroomSessionWithCapture(t, r, projectID, cfgPath)

	dataDir := filepath.Dir(r.path)
	session, err := groom.ReadSession(dataDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	produced := make(map[string]bool, len(session.Produced))
	for _, id := range session.Produced {
		produced[id] = true
	}
	if !produced[captureID] || !produced[taskID] {
		t.Fatalf("produced = %v, want both the capture %s and the task %s", session.Produced, captureID, taskID)
	}

	out := r.run("--config", cfgPath, "groom", "review", sessionID, "--verdict", "needs-rework", "-b", "Wrong boundary.")
	if !strings.Contains(out, "blocked: "+taskID) {
		t.Fatalf("needs-rework did not report the blocked produced task:\n%s", out)
	}
	if strings.Contains(out, captureID) {
		t.Fatalf("needs-rework claimed the capture %s in blocked:\n%s", captureID, out)
	}
	if got := r.run("--config", cfgPath, "task", "get", taskID); !strings.Contains(got, "(blocked)") {
		t.Fatalf("produced task not blocked:\n%s", got)
	}
	if got := r.run("--config", cfgPath, "task", "get", captureID); !strings.Contains(got, "(todo)") {
		t.Fatalf("capture must stay untouched, not blocked:\n%s", got)
	}
	session, err = groom.ReadSession(dataDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Blocked) != 1 || session.Blocked[0] != taskID {
		t.Fatalf("manifest blocked = %v, want [%s] only", session.Blocked, taskID)
	}

	r.run("--config", cfgPath, "groom", "review", sessionID, "--verdict", "approve", "-b", "Boundary fixed.")
	if got := r.run("--config", cfgPath, "task", "get", taskID); !strings.Contains(got, "(todo)") {
		t.Fatalf("approve did not unblock the produced task:\n%s", got)
	}
	if got := r.run("--config", cfgPath, "task", "get", captureID); !strings.Contains(got, "(todo)") {
		t.Fatalf("approve disturbed the capture:\n%s", got)
	}
	session, err = groom.ReadSession(dataDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Blocked) != 0 {
		t.Fatalf("manifest blocked = %v, want none after approve", session.Blocked)
	}
}

// TestGroomReviewRejectsBadInput pins the input contract: an unknown verdict, a
// missing body, and an unknown session each fail without touching the graph.
func TestGroomReviewRejectsBadInput(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	sessionID, _ := seedGroomSession(t, r, projectID, cfgPath, "Extract cache module")

	if err := r.runErr("--config", cfgPath, "groom", "review", sessionID, "--verdict", "lgtm", "-b", "x"); err == nil {
		t.Fatal("unknown verdict error = nil, want a usage error")
	}
	if err := r.runErr("--config", cfgPath, "groom", "review", sessionID, "--verdict", "approve"); err == nil {
		t.Fatal("missing body error = nil, want an error")
	}
	if err := r.runErr("--config", cfgPath, "groom", "review", "groom-missing", "--verdict", "approve", "-b", "x"); err == nil {
		t.Fatal("unknown session error = nil, want an error")
	}
}

// TestGroomReviewNeedsReworkReassertsBlock guards the gate against going
// silently open: a produced task moved out of blocked (started) is re-blocked by
// a repeated needs-rework, and the reported set matches the real graph state.
func TestGroomReviewNeedsReworkReassertsBlock(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	sessionID, producedID := seedGroomSession(t, r, projectID, cfgPath, "Extract cache module")

	r.run("--config", cfgPath, "groom", "review", sessionID, "--verdict", "needs-rework", "-b", "Wrong boundary.")
	r.run("--config", cfgPath, "task", "start", producedID)
	if got := r.run("--config", cfgPath, "task", "get", producedID); !strings.Contains(got, "(in_progress)") {
		t.Fatalf("task start did not move the produced task out of blocked:\n%s", got)
	}

	out := r.run("--config", cfgPath, "groom", "review", sessionID, "--verdict", "needs-rework", "-b", "Still wrong.")
	if !strings.Contains(out, "blocked: "+producedID) {
		t.Fatalf("re-review did not report the re-asserted block:\n%s", out)
	}
	if got := r.run("--config", cfgPath, "task", "get", producedID); !strings.Contains(got, "(blocked)") {
		t.Fatalf("a repeated needs-rework left the gate open:\n%s", got)
	}
	dataDir := filepath.Dir(r.path)
	session, err := groom.ReadSession(dataDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Blocked) != 1 || session.Blocked[0] != producedID {
		t.Fatalf("manifest blocked = %v, want [%s]", session.Blocked, producedID)
	}
}
