package graph

import (
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

func TestReadinessReasons(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	hold := core.TaskID("hold")

	tests := []struct {
		name    string
		subject core.Task
		extra   []core.Task
		ready   bool
		want    *NotReadyReason
	}{
		{
			name:    "startable",
			subject: task("subject", core.KindTask, core.StatusTodo),
			ready:   true,
		},
		{
			name:    "resolved has no reason",
			subject: task("subject", core.KindTask, core.StatusDone),
		},
		{
			name:    "unresolved dependency",
			subject: task("subject", core.KindTask, core.StatusTodo, "open"),
			extra:   []core.Task{task("open", core.KindTask, core.StatusTodo)},
			want:    &NotReadyReason{Code: ReasonDepUnresolved, Detail: "open"},
		},
		{
			name:    "missing dependency is unresolved",
			subject: task("subject", core.KindTask, core.StatusTodo, "ghost"),
			want:    &NotReadyReason{Code: ReasonDepUnresolved, Detail: "ghost"},
		},
		{
			name:    "blocked",
			subject: task("subject", core.KindTask, core.StatusBlocked),
			want:    &NotReadyReason{Code: ReasonBlocked},
		},
		{
			name:    "in progress",
			subject: task("subject", core.KindTask, core.StatusInProgress),
			want:    &NotReadyReason{Code: ReasonInProgress},
		},
		{
			name:    "snoozed indefinitely",
			subject: snoozedTask("subject", core.Snooze{Indefinite: true}),
			want:    &NotReadyReason{Code: ReasonSnoozed, Detail: "indefinitely"},
		},
		{
			name:    "snoozed until a date",
			subject: snoozedTask("subject", core.Snooze{Until: &future}),
			want:    &NotReadyReason{Code: ReasonSnoozed, Detail: "until 2026-09-20T13:00:00Z"},
		},
		{
			name:    "snoozed until a task",
			subject: snoozedTask("subject", core.Snooze{UntilTask: &hold}),
			extra:   []core.Task{task("hold", core.KindTask, core.StatusTodo)},
			want:    &NotReadyReason{Code: ReasonSnoozed, Detail: "until hold"},
		},
		{
			name:    "not before",
			subject: deferredTask("subject", future),
			want:    &NotReadyReason{Code: ReasonNotBefore, Detail: "2026-09-20T13:00:00Z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tasks := append([]core.Task{}, tt.extra...)
			tasks = append(tasks, tt.subject)
			g, err := NewAt(tasks, core.DefaultResolutionPolicy(), now)
			if err != nil {
				t.Fatalf("NewAt() error = %v", err)
			}
			ready, reason := g.Readiness(tt.subject.ID)
			if ready != tt.ready {
				t.Fatalf("Readiness(%s) ready = %v, want %v", tt.subject.ID, ready, tt.ready)
			}
			if !equalReason(reason, tt.want) {
				t.Fatalf("Readiness(%s) reason = %+v, want %+v", tt.subject.ID, reason, tt.want)
			}
		})
	}
}

func TestReadinessPrecedence(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)

	blockedWithDep := task("blocked-with-dep", core.KindTask, core.StatusBlocked, "open")
	blockedWithSnooze := snoozedTask("blocked-with-snooze", core.Snooze{Indefinite: true})
	blockedWithSnooze.Status = core.StatusBlocked
	blockedWithSnooze.NotBefore = &future
	doingWithSnooze := snoozedTask("doing-with-snooze", core.Snooze{Indefinite: true})
	doingWithSnooze.Status = core.StatusInProgress
	doingWithSnooze.NotBefore = &future
	snoozedBeforeDate := snoozedTask("snoozed-before-date", core.Snooze{Indefinite: true})
	snoozedBeforeDate.NotBefore = &future

	open := task("open", core.KindTask, core.StatusTodo)
	g, err := NewAt(
		[]core.Task{open, blockedWithDep, blockedWithSnooze, doingWithSnooze, snoozedBeforeDate},
		core.DefaultResolutionPolicy(), now,
	)
	if err != nil {
		t.Fatalf("NewAt() error = %v", err)
	}

	tests := []struct {
		id   core.TaskID
		want ReasonCode
	}{
		{"blocked-with-dep", ReasonDepUnresolved},
		{"blocked-with-snooze", ReasonBlocked},
		{"doing-with-snooze", ReasonInProgress},
		{"snoozed-before-date", ReasonSnoozed},
	}
	for _, tt := range tests {
		_, reason := g.Readiness(tt.id)
		if reason == nil || reason.Code != tt.want {
			t.Fatalf("Readiness(%s) reason = %+v, want code %s", tt.id, reason, tt.want)
		}
	}
}

func TestReadinessAgreesWithReadySet(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	hold := core.TaskID("hold")

	tasks := []core.Task{
		task("todo", core.KindTask, core.StatusTodo),
		task("done", core.KindTask, core.StatusDone),
		task("blocked", core.KindTask, core.StatusBlocked),
		task("doing", core.KindTask, core.StatusInProgress),
		task("blocked-by-todo", core.KindTask, core.StatusTodo, "todo"),
		task("ghost-dep", core.KindTask, core.StatusTodo, "ghost"),
		deferredTask("later", future),
		snoozedTask("parked", core.Snooze{Indefinite: true}),
		task("hold", core.KindTask, core.StatusTodo),
		snoozedTask("waiting", core.Snooze{UntilTask: &hold}),
	}
	g, err := NewAt(tasks, core.DefaultResolutionPolicy(), now)
	if err != nil {
		t.Fatalf("NewAt() error = %v", err)
	}

	ready := map[core.TaskID]bool{}
	for _, id := range g.ReadySet() {
		ready[id] = true
	}

	for _, id := range g.IDs() {
		gotReady, reason := g.Readiness(id)
		if gotReady != ready[id] {
			t.Fatalf("Readiness(%s) ready = %v, ReadySet membership = %v", id, gotReady, ready[id])
		}
		if gotReady && reason != nil {
			t.Fatalf("Readiness(%s) is ready but carries reason %+v", id, reason)
		}
	}
	if _, reason := g.Readiness("done"); reason != nil {
		t.Fatalf("resolved task must carry no reason, got %+v", reason)
	}
}

func snoozedTask(id string, snooze core.Snooze) core.Task {
	t := task(id, core.KindTask, core.StatusTodo)
	t.Snooze = &snooze
	return t
}

func deferredTask(id string, at time.Time) core.Task {
	t := task(id, core.KindTask, core.StatusTodo)
	t.NotBefore = &at
	return t
}

func equalReason(got, want *NotReadyReason) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return got.Code == want.Code && got.Detail == want.Detail
}
