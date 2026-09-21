package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

const intentLabels = "add_label::"

func intentAnswers(labels []string) map[string]judge.Answer {
	answers := whenAnswers() // the not_before path reuses the date questions
	answers["status"] = judge.Answer{Choice: "unspecified", Confidence: 0.9}
	answers["status_stated"] = judge.Answer{Probability: 0.05}
	answers["kind"] = judge.Answer{Choice: "unspecified", Confidence: 0.9}
	answers["kind_stated"] = judge.Answer{Probability: 0.05}
	answers["priority"] = judge.Answer{Choice: "unspecified", Confidence: 0.9}
	answers["priority_stated"] = judge.Answer{Probability: 0.05}
	answers["not_before_stated"] = judge.Answer{Probability: 0.05}
	for _, label := range labels {
		answers[intentLabels+label] = judge.Answer{Probability: 0.05}
	}
	return answers
}

func TestParseTaskIntent(t *testing.T) {
	labels := []string{"urgent", "perf"}
	cases := []struct {
		name string
		set  func(map[string]judge.Answer)
		want func(*testing.T, TaskSet)
	}{
		{"status", func(a map[string]judge.Answer) {
			a["status_stated"] = judge.Answer{Probability: 0.9}
			a["status"] = judge.Answer{Choice: "done", Confidence: 0.95}
		}, func(t *testing.T, set TaskSet) {
			if set.Status == nil || *set.Status != core.StatusDone {
				t.Fatalf("Status = %v, want done", set.Status)
			}
		}},
		{"kind", func(a map[string]judge.Answer) {
			a["kind_stated"] = judge.Answer{Probability: 0.9}
			a["kind"] = judge.Answer{Choice: "milestone", Confidence: 0.95}
		}, func(t *testing.T, set TaskSet) {
			if set.Kind == nil || *set.Kind != core.KindMilestone {
				t.Fatalf("Kind = %v, want milestone", set.Kind)
			}
		}},
		{"priority", func(a map[string]judge.Answer) {
			a["priority_stated"] = judge.Answer{Probability: 0.9}
			a["priority"] = judge.Answer{Choice: "50", Confidence: 0.95}
		}, func(t *testing.T, set TaskSet) {
			if set.Priority == nil || *set.Priority != 50 {
				t.Fatalf("Priority = %v, want 50", set.Priority)
			}
		}},
		{"labels", func(a map[string]judge.Answer) {
			a[intentLabels+"urgent"] = judge.Answer{Probability: 0.9}
		}, func(t *testing.T, set TaskSet) {
			if len(set.Labels) != 1 || set.Labels[0] != "urgent" {
				t.Fatalf("Labels = %v, want [urgent]", set.Labels)
			}
		}},
		{"not before reuses the date service", func(a map[string]judge.Answer) {
			a["not_before_stated"] = judge.Answer{Probability: 0.9}
			a[whenMode] = judge.Answer{Choice: "relative", Confidence: 0.95}
			a[whenDayAnchor] = judge.Answer{Choice: "tomorrow", Confidence: 0.95}
		}, func(t *testing.T, set TaskSet) {
			want := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
			if set.NotBefore == nil || !set.NotBefore.Equal(want) {
				t.Fatalf("NotBefore = %v, want %s", set.NotBefore, want)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := intentAnswers(labels)
			tc.set(answers)
			service := NewIntentService(fake.New(answers))
			set, err := service.ParseTaskIntent(context.Background(), "phrase", labels, whenNow)
			if err != nil {
				t.Fatalf("ParseTaskIntent() error = %v", err)
			}
			tc.want(t, set)
		})
	}
}

func TestParseTaskIntentOverFireIsRejected(t *testing.T) {
	// A phrase that states nothing must produce an empty set, not a defaulted one.
	service := NewIntentService(fake.New(intentAnswers([]string{"urgent"})))
	_, err := service.ParseTaskIntent(context.Background(), "hmm", []string{"urgent"}, whenNow)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("ParseTaskIntent() error = %v, want core.ErrInvalid", err)
	}
}

func TestParseTaskIntentUnavailable(t *testing.T) {
	service := NewIntentService(judge.Disabled{})
	_, err := service.ParseTaskIntent(context.Background(), "mark it done", nil, whenNow)
	if !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("ParseTaskIntent() error = %v, want judge.ErrUnavailable", err)
	}
}
