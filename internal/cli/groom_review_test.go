package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/groom"
)

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
