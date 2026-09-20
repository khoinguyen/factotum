package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseNotBefore(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"2030-01-01", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"2030-01-01T10:00:00Z", time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC), false},
		{"+7d", now.Add(7 * 24 * time.Hour), false},
		{"+1w", now.Add(7 * 24 * time.Hour), false},
		{"+36h", now.Add(36 * time.Hour), false},
		{"+30m", now.Add(30 * time.Minute), false},
		{"soon", time.Time{}, true},
		{"+7x", time.Time{}, true},
		{"+", time.Time{}, true},
	}
	for _, tt := range tests {
		got, err := parseNotBefore(tt.in, now)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseNotBefore(%q) error = nil, want error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseNotBefore(%q) error = %v", tt.in, err)
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("parseNotBefore(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestTaskSetNotBeforeRelativeDefers(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "soak"))

	r.run("task", "set", taskID, "not_before=+7d")
	if out := r.run("task", "next", "--project", projectID); strings.Contains(out, taskID) {
		t.Fatalf("+7d task should not be ready:\n%s", out)
	}
}

func TestTaskSetNotBeforeDefersFromNext(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "soak"))

	r.run("task", "set", taskID, "not_before=2030-01-01")

	if out := r.run("task", "next", "--project", projectID); strings.Contains(out, taskID) {
		t.Fatalf("deferred task should not be ready:\n%s", out)
	}
	get := r.run("task", "get", taskID)
	if !strings.Contains(get, "not_before: 2030-01-01T00:00:00Z") {
		t.Fatalf("task get missing not_before:\n%s", get)
	}

	r.run("task", "set", taskID, "not_before=")
	if out := r.run("task", "next", "--project", projectID); !strings.Contains(out, taskID) {
		t.Fatalf("cleared task should be ready:\n%s", out)
	}
}

func TestTaskGetJSONIncludesNotBefore(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "soak"))
	r.run("task", "set", taskID, "not_before=2030-01-01")

	out := r.run("task", "get", taskID, "-o", "json")
	var doc struct {
		NotBefore string `json:"not_before"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("task get json: %v\n%s", err, out)
	}
	if doc.NotBefore != "2030-01-01T00:00:00Z" {
		t.Fatalf("not_before = %q, want 2030-01-01T00:00:00Z", doc.NotBefore)
	}
}

func TestTaskSetNotBeforeRejectsInvalid(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "soak"))

	err := r.runErr("task", "set", taskID, "not_before=soon")
	if err == nil || !strings.Contains(err.Error(), "not_before") {
		t.Fatalf("expected a not_before parse error, got %v", err)
	}
}
