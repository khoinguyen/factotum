package core

import "testing"

func TestTaskKindValid(t *testing.T) {
	tests := []struct {
		kind TaskKind
		want bool
	}{
		{KindTask, true},
		{KindMilestone, true},
		{"epic", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.kind.Valid(); got != tt.want {
			t.Errorf("TaskKind(%q).Valid() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestTaskStatusValid(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   bool
	}{
		{StatusTodo, true},
		{StatusInProgress, true},
		{StatusBlocked, true},
		{StatusReadyForReview, true},
		{StatusDone, true},
		{StatusCancelled, true},
		{"archived", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.want {
			t.Errorf("TaskStatus(%q).Valid() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestTaskValidate(t *testing.T) {
	base := Task{ID: "t-1", ProjectID: "prj-1", Kind: KindTask, Title: "Do the thing", Status: StatusTodo}

	tests := []struct {
		name    string
		mutate  func(*Task)
		wantErr bool
	}{
		{"valid", func(*Task) {}, false},
		{"missing id", func(t *Task) { t.ID = "" }, true},
		{"missing project", func(t *Task) { t.ProjectID = "" }, true},
		{"blank title", func(t *Task) { t.Title = "   " }, true},
		{"unknown kind", func(t *Task) { t.Kind = "epic" }, true},
		{"unknown status", func(t *Task) { t.Status = "archived" }, true},
		{"self dependency", func(t *Task) { t.Deps = []TaskID{"t-1"} }, true},
		{"empty dependency", func(t *Task) { t.Deps = []TaskID{""} }, true},
		{"duplicate dependency", func(t *Task) { t.Deps = []TaskID{"t-2", "t-2"} }, true},
		{"valid dependencies", func(t *Task) { t.Deps = []TaskID{"t-2", "t-3"} }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := base
			tt.mutate(&task)
			if err := task.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTaskResolves(t *testing.T) {
	p := DefaultResolutionPolicy()
	tests := []struct {
		name string
		task Task
		want bool
	}{
		{"task todo", Task{Kind: KindTask, Status: StatusTodo}, false},
		{"task in progress", Task{Kind: KindTask, Status: StatusInProgress}, false},
		{"task in review", Task{Kind: KindTask, Status: StatusReadyForReview}, true},
		{"task done", Task{Kind: KindTask, Status: StatusDone}, true},
		{"milestone in review", Task{Kind: KindMilestone, Status: StatusReadyForReview}, false},
		{"milestone done", Task{Kind: KindMilestone, Status: StatusDone}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.task.Resolves(p); got != tt.want {
				t.Fatalf("Resolves() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTaskIsMilestone(t *testing.T) {
	if !(Task{Kind: KindMilestone}).IsMilestone() {
		t.Fatal("milestone task should report IsMilestone")
	}
	if (Task{Kind: KindTask}).IsMilestone() {
		t.Fatal("plain task should not report IsMilestone")
	}
}

func TestLinkValidate(t *testing.T) {
	tests := []struct {
		name    string
		link    Link
		wantErr bool
	}{
		{"pr", Link{Kind: LinkPR, URL: "https://example.com/pr/1"}, false},
		{"issue with title", Link{Kind: LinkIssue, URL: "https://example.com/1", Title: "bug"}, false},
		{"doc", Link{Kind: LinkDoc, URL: "https://example.com/doc"}, false},
		{"url", Link{Kind: LinkURL, URL: "https://example.com"}, false},
		{"unknown kind", Link{Kind: "wiki", URL: "https://example.com"}, true},
		{"missing url", Link{Kind: LinkPR}, true},
		{"missing kind", Link{URL: "https://example.com"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.link.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNoteValidate(t *testing.T) {
	tests := []struct {
		name    string
		note    Note
		wantErr bool
	}{
		{"valid", Note{ID: "n-1", Body: "needs a decision"}, false},
		{"valid with link", Note{ID: "n-1", Body: "see PR", Links: []Link{{Kind: LinkPR, URL: "https://example.com/pr/1"}}}, false},
		{"missing id", Note{Body: "x"}, true},
		{"missing body", Note{ID: "n-1"}, true},
		{"invalid link", Note{ID: "n-1", Body: "x", Links: []Link{{Kind: LinkPR}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.note.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
