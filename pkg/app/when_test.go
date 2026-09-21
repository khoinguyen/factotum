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

// whenNow is a Sunday, so weekday resolution has an unambiguous answer.
var whenNow = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)

func choiceAnswer(choice string, confidence float64) judge.Answer {
	return judge.Answer{Choice: choice, Confidence: confidence}
}

// whenAnswers returns a full answer set with every question answered "none", so a
// case only sets the fields it cares about.
func whenAnswers() map[string]judge.Answer {
	answers := map[string]judge.Answer{}
	for _, id := range []string{
		whenMode, whenMonth, whenDay, whenYear, whenDayAnchor, whenWeekday,
		whenWeekOffset, whenOffsetUnit, whenOffsetCount, whenPeriodEnd,
	} {
		answers[id] = choiceAnswer("none", 0.95)
	}
	return answers
}

func TestAssembleWhen(t *testing.T) {
	cases := []struct {
		name string
		set  func(map[string]judge.Answer)
		want time.Time
	}{
		{"today", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.95)
			a[whenDayAnchor] = choiceAnswer("today", 0.95)
		}, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)},
		{"tomorrow", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.95)
			a[whenDayAnchor] = choiceAnswer("tomorrow", 0.95)
		}, time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)},
		{"day after tomorrow", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.95)
			a[whenDayAnchor] = choiceAnswer("day_after", 0.9)
		}, time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)},
		{"bare weekday is next occurrence", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.95)
			a[whenDayAnchor] = choiceAnswer("weekday", 0.95)
			a[whenWeekday] = choiceAnswer("Friday", 0.95)
			a[whenWeekOffset] = choiceAnswer("none", 0.9)
		}, time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)},
		{"next friday", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.95)
			a[whenDayAnchor] = choiceAnswer("weekday", 0.95)
			a[whenWeekday] = choiceAnswer("Friday", 0.95)
			a[whenWeekOffset] = choiceAnswer("next", 0.9)
		}, time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)},
		{"this friday", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.95)
			a[whenDayAnchor] = choiceAnswer("weekday", 0.95)
			a[whenWeekday] = choiceAnswer("Friday", 0.95)
			a[whenWeekOffset] = choiceAnswer("current", 0.9)
		}, time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)},
		{"in two weeks", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.95)
			a[whenDayAnchor] = choiceAnswer("offset", 0.9)
			a[whenOffsetUnit] = choiceAnswer("week", 0.95)
			a[whenOffsetCount] = choiceAnswer("2", 0.95)
		}, time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)},
		{"in three days", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.95)
			a[whenDayAnchor] = choiceAnswer("offset", 0.9)
			a[whenOffsetUnit] = choiceAnswer("day", 0.95)
			a[whenOffsetCount] = choiceAnswer("3", 0.95)
		}, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)},
		{"absolute with inferred year", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("absolute", 0.95)
			a[whenMonth] = choiceAnswer("October", 0.95)
			a[whenDay] = choiceAnswer("15", 0.95)
			a[whenYear] = choiceAnswer("none", 0.9)
		}, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)},
		{"absolute date more than a month past rolls forward", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("absolute", 0.95)
			a[whenMonth] = choiceAnswer("August", 0.95)
			a[whenDay] = choiceAnswer("14", 0.95)
			a[whenYear] = choiceAnswer("none", 0.9)
		}, time.Date(2027, 8, 14, 0, 0, 0, 0, time.UTC)},
		{"end of month", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.9)
			a[whenPeriodEnd] = choiceAnswer("end_of_month", 0.9)
		}, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)},
		{"explicit year", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("absolute", 0.95)
			a[whenMonth] = choiceAnswer("March", 0.95)
			a[whenDay] = choiceAnswer("3", 0.95)
			a[whenYear] = choiceAnswer("2027", 0.95)
		}, time.Date(2027, 3, 3, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := whenAnswers()
			tc.set(answers)
			got, err := assembleWhen(answers, whenNow)
			if err != nil {
				t.Fatalf("assembleWhen() error = %v", err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("assembleWhen() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAssembleWhenRejects(t *testing.T) {
	cases := []struct {
		name string
		set  func(map[string]judge.Answer)
	}{
		{"no date stated", func(map[string]judge.Answer) {}},
		{"low confidence", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("relative", 0.95)
			a[whenDayAnchor] = choiceAnswer("today", 0.3)
		}},
		{"year out of range", func(a map[string]judge.Answer) {
			a[whenMode] = choiceAnswer("absolute", 0.95)
			a[whenMonth] = choiceAnswer("March", 0.95)
			a[whenDay] = choiceAnswer("3", 0.95)
			a[whenYear] = choiceAnswer("out_of_range", 0.95)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := whenAnswers()
			tc.set(answers)
			if _, err := assembleWhen(answers, whenNow); err == nil {
				t.Fatal("assembleWhen() error = nil, want a rejection")
			}
		})
	}
}

func TestWhenServiceParseUsesJudge(t *testing.T) {
	answers := whenAnswers()
	answers[whenMode] = choiceAnswer("relative", 0.95)
	answers[whenDayAnchor] = choiceAnswer("tomorrow", 0.95)
	f := fake.New(answers)
	service := NewWhenService(f)

	got, err := service.Parse(context.Background(), "tomorrow", whenNow)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if want := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Parse() = %s, want %s", got, want)
	}
	if len(f.Requests()) != 1 {
		t.Fatalf("judge calls = %d, want 1", len(f.Requests()))
	}
}

func TestWhenServiceParseWrapsRejectionAsInvalid(t *testing.T) {
	service := NewWhenService(fake.New(whenAnswers()))
	_, err := service.Parse(context.Background(), "sometime", whenNow)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Parse() error = %v, want core.ErrInvalid", err)
	}
}

func TestWhenServiceParseUnavailable(t *testing.T) {
	service := NewWhenService(judge.Disabled{})
	_, err := service.Parse(context.Background(), "next friday", whenNow)
	if !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("Parse() error = %v, want judge.ErrUnavailable", err)
	}
}

func TestAssembleWhenNormalizesToMidnightUTC(t *testing.T) {
	answers := whenAnswers()
	answers[whenMode] = choiceAnswer("relative", 0.95)
	answers[whenDayAnchor] = choiceAnswer("today", 0.95)
	now := time.Date(2026, 9, 20, 12, 34, 56, 0, time.UTC)
	got, err := assembleWhen(answers, now)
	if err != nil {
		t.Fatalf("assembleWhen() error = %v", err)
	}
	if want := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("assembleWhen() = %s, want %s", got, want)
	}
}
