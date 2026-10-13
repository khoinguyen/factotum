package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestIdeaCreateRecordsOriginArtifact pins the grooming-agent path end to end:
// an idea captured from a prompt artifact carries that artifact as its origin,
// shown by `task get` in text and as machine fields.
func TestIdeaCreateRecordsOriginArtifact(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	docID := firstField(t, r.run("doc", "create", "-p", projectID, "-t", "Add dark mode", "-b", "please add a dark mode toggle"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Dark mode", "--origin-artifact", docID))

	got := r.run("task", "get", ideaID)
	if !strings.Contains(got, "origin_artifact: "+docID+" Add dark mode") {
		t.Fatalf("task get should show the prompt artifact with its title:\n%s", got)
	}

	var doc struct {
		OriginArtifact      *string `json:"origin_artifact"`
		OriginArtifactTitle *string `json:"origin_artifact_title"`
	}
	if err := json.Unmarshal([]byte(r.run("task", "get", ideaID, "-o", "json")), &doc); err != nil {
		t.Fatalf("task get -o json: %v", err)
	}
	if doc.OriginArtifact == nil || *doc.OriginArtifact != docID {
		t.Fatalf("origin_artifact = %v, want %s", doc.OriginArtifact, docID)
	}
	if doc.OriginArtifactTitle == nil || *doc.OriginArtifactTitle != "Add dark mode" {
		t.Fatalf("origin_artifact_title = %v, want %q", doc.OriginArtifactTitle, "Add dark mode")
	}
}

// TestBugCreateRecordsOriginArtifact pins that a grooming agent can capture a
// bug against the prompt too, not only an idea.
func TestBugCreateRecordsOriginArtifact(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	docID := firstField(t, r.run("doc", "create", "-p", projectID, "-t", "Login is broken", "-b", "the login button does nothing"))
	bugID := firstField(t, r.run("bug", "create", "-p", projectID, "-t", "Login button is dead", "--origin-artifact", docID))

	got := r.run("task", "get", bugID)
	if !strings.Contains(got, "origin_artifact: "+docID+" Login is broken") {
		t.Fatalf("task get should show the prompt artifact with its title:\n%s", got)
	}
}

// TestCaptureWithoutOriginArtifactStaysClean pins that the provenance is
// optional: a plain capture prints no origin_artifact line.
func TestCaptureWithoutOriginArtifactStaysClean(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "A spark"))

	if got := r.run("task", "get", ideaID); strings.Contains(got, "origin_artifact") {
		t.Fatalf("a capture with no prompt artifact must not print one:\n%s", got)
	}
}
