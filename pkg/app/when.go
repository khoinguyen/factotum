package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
)

// whenMinConfidence is the floor for the weakest part used to assemble a date. Below
// it the phrase is rejected rather than guessed.
const whenMinConfidence = 0.6

// Question ids for the date questions.
const (
	whenMode        = "mode"
	whenMonth       = "month"
	whenDay         = "day"
	whenYear        = "year"
	whenDayAnchor   = "day_anchor"
	whenWeekday     = "weekday"
	whenWeekOffset  = "week_offset"
	whenOffsetUnit  = "offset_unit"
	whenOffsetCount = "offset_count"
	whenPeriodEnd   = "period_end"
)

var errUnreadable = errors.New("could not read a date from the phrase")

var (
	whenMonths   = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	whenWeekdays = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
)

// WhenService turns a natural-language date phrase into a concrete time. The
// deterministic formats (RFC3339, YYYY-MM-DD, +duration) are the caller's fast path;
// this service reads free text, and all calendar math happens here against now.
type WhenService struct {
	judge judge.Judge
}

func NewWhenService(j judge.Judge) *WhenService { return &WhenService{judge: j} }

// Parse reads the parts of a date phrase and assembles them. It returns
// judge.ErrUnavailable when no judge is configured, and a core.ErrInvalid error when
// the phrase cannot be assembled or any part is below the confidence floor.
func (s *WhenService) Parse(ctx context.Context, phrase string, now time.Time) (time.Time, error) {
	response, err := s.judge.Ask(ctx, judge.Request{State: phrase, Questions: whenQuestions(now)})
	if err != nil {
		return time.Time{}, err
	}
	when, err := assembleWhen(response.Answers, now)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %w", core.ErrInvalid, err)
	}
	return when, nil
}

// whenQuestions asks for the parts of a date, never for the date itself, so the model
// reads the text and code does the calendar math.
func whenQuestions(now time.Time) map[string]judge.Question {
	absent := "The phrase does not state this, or it is not this kind of date."
	years := map[string]any{"none": "No year is stated; code infers it.",
		"out_of_range": "A year is stated but outside the listed range."}
	for year := now.Year() - 1; year <= now.Year()+10; year++ {
		years[strconv.Itoa(year)] = nil
	}
	months := map[string]any{"none": absent}
	for _, month := range whenMonths {
		months[month] = nil
	}
	days := map[string]any{"none": absent}
	for day := 1; day <= 31; day++ {
		days[strconv.Itoa(day)] = nil
	}
	weekdays := map[string]any{"none": absent}
	for _, weekday := range whenWeekdays {
		weekdays[weekday] = nil
	}
	counts := map[string]any{"none": absent}
	for n := 1; n <= 12; n++ {
		counts[strconv.Itoa(n)] = nil
	}
	return map[string]judge.Question{
		whenMode: {Kind: judge.KindChoice, Criteria: map[string]any{
			"absolute": "A calendar date naming a month, e.g. 'October 15'.",
			"relative": "A date relative to today: today, tomorrow, a weekday, or an offset.",
			"none":     absent},
			Instructions: "How is the target date written?"},
		whenMonth: {Kind: judge.KindChoice, Criteria: months,
			Instructions: "If the date is absolute, which month?"},
		whenDay: {Kind: judge.KindChoice, Criteria: days,
			Instructions: "If the date is absolute, which day of the month?"},
		whenYear: {Kind: judge.KindChoice, Criteria: years,
			Instructions: "If the date is absolute, which year?"},
		whenDayAnchor: {Kind: judge.KindChoice, Criteria: map[string]any{
			"today": nil, "tomorrow": nil, "day_after": "The day after tomorrow.",
			"weekday": "A named day of the week.", "offset": "An explicit duration from today.",
			"none": absent},
			Instructions: "If the date is relative, which kind?"},
		whenWeekday: {Kind: judge.KindChoice, Criteria: weekdays,
			Instructions: "If a weekday is named, which one?"},
		whenWeekOffset: {Kind: judge.KindChoice, Criteria: map[string]any{
			"next": "'next Thursday' or 'Thursday next week'.", "current": "'this Thursday'.",
			"none": "A bare weekday with no qualifier."},
			Instructions: "If a weekday is named, which week?"},
		whenOffsetUnit: {Kind: judge.KindChoice, Criteria: map[string]any{
			"day": nil, "week": nil, "none": absent},
			Instructions: "If the date is an explicit duration, what unit?"},
		whenOffsetCount: {Kind: judge.KindChoice, Criteria: counts,
			Instructions: "If the date is an explicit duration, how many units?"},
		whenPeriodEnd: {Kind: judge.KindChoice, Criteria: map[string]any{
			"end_of_month": nil, "end_of_week": nil, "end_of_quarter": nil, "end_of_year": nil,
			"none": "Not the end of a period."},
			Instructions: "If the date is the end of a period, which period?"},
	}
}

// assembleWhen turns the answered parts into a date, using only the parts the shape
// calls for and rejecting anything below the confidence floor.
func assembleWhen(answers map[string]judge.Answer, now time.Time) (time.Time, error) {
	choice := func(id string) string { return answers[id].Choice }
	confidence := func(id string) float64 { return answers[id].Confidence }

	used := []float64{confidence(whenMode)}

	if period := choice(whenPeriodEnd); period != "none" {
		used = append(used, confidence(whenPeriodEnd))
		when, err := periodEnd(period, now)
		if err != nil {
			return time.Time{}, err
		}
		return finish(when, used)
	}

	switch choice(whenMode) {
	case "absolute":
		used = append(used, confidence(whenMonth), confidence(whenDay), confidence(whenYear))
		when, err := absoluteDate(choice(whenMonth), choice(whenDay), choice(whenYear), now)
		if err != nil {
			return time.Time{}, err
		}
		return finish(when, used)
	case "relative":
		used = append(used, confidence(whenDayAnchor))
		when, err := relativeDate(choice(whenDayAnchor), answers, now, &used)
		if err != nil {
			return time.Time{}, err
		}
		return finish(when, used)
	default:
		return time.Time{}, errUnreadable
	}
}

func absoluteDate(month, day, year string, now time.Time) (time.Time, error) {
	monthIndex, ok := indexOf(whenMonths, month)
	if !ok {
		return time.Time{}, errUnreadable
	}
	dayNumber, err := strconv.Atoi(day)
	if err != nil || dayNumber < 1 || dayNumber > 31 {
		return time.Time{}, errUnreadable
	}
	inferred := year == "none"
	var yearNumber int
	switch {
	case year == "out_of_range":
		return time.Time{}, errUnreadable
	case inferred:
		yearNumber = now.Year()
	default:
		if yearNumber, err = strconv.Atoi(year); err != nil {
			return time.Time{}, errUnreadable
		}
	}
	when := time.Date(yearNumber, time.Month(monthIndex+1), dayNumber, 0, 0, 0, 0, time.UTC)
	if when.Month() != time.Month(monthIndex+1) || when.Day() != dayNumber {
		return time.Time{}, errUnreadable
	}
	if inferred && when.Before(now.AddDate(0, 0, -31)) {
		when = time.Date(yearNumber+1, time.Month(monthIndex+1), dayNumber, 0, 0, 0, 0, time.UTC)
	}
	return when, nil
}

func relativeDate(anchor string, answers map[string]judge.Answer, now time.Time, used *[]float64) (time.Time, error) {
	switch anchor {
	case "today":
		return now, nil
	case "tomorrow":
		return now.AddDate(0, 0, 1), nil
	case "day_after":
		return now.AddDate(0, 0, 2), nil
	case "weekday":
		*used = append(*used, answers[whenWeekday].Confidence, answers[whenWeekOffset].Confidence)
		weekday, ok := indexOf(whenWeekdays, answers[whenWeekday].Choice)
		if !ok {
			return time.Time{}, errUnreadable
		}
		return resolveWeekday(now, weekday, answers[whenWeekOffset].Choice), nil
	case "offset":
		*used = append(*used, answers[whenOffsetUnit].Confidence, answers[whenOffsetCount].Confidence)
		count, err := strconv.Atoi(answers[whenOffsetCount].Choice)
		if err != nil {
			return time.Time{}, errUnreadable
		}
		switch answers[whenOffsetUnit].Choice {
		case "day":
			return now.AddDate(0, 0, count), nil
		case "week":
			return now.AddDate(0, 0, 7*count), nil
		default:
			return time.Time{}, errUnreadable
		}
	default:
		return time.Time{}, errUnreadable
	}
}

// resolveWeekday uses one convention: a bare weekday is the next occurrence on or
// after today, "current" is this calendar week, and "next" is the following week.
func resolveWeekday(now time.Time, weekday int, offset string) time.Time {
	today := (int(now.Weekday()) + 6) % 7 // Monday = 0
	monday := now.AddDate(0, 0, -today)
	switch offset {
	case "next":
		return monday.AddDate(0, 0, 7+weekday)
	case "current":
		return monday.AddDate(0, 0, weekday)
	default:
		return now.AddDate(0, 0, (weekday-today+7)%7)
	}
}

func periodEnd(period string, now time.Time) (time.Time, error) {
	today := (int(now.Weekday()) + 6) % 7
	switch period {
	case "end_of_month":
		return time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC), nil
	case "end_of_week":
		return now.AddDate(0, 0, 6-today), nil
	case "end_of_quarter":
		lastMonth := (int(now.Month())-1)/3*3 + 3
		return time.Date(now.Year(), time.Month(lastMonth)+1, 0, 0, 0, 0, 0, time.UTC), nil
	case "end_of_year":
		return time.Date(now.Year(), time.December, 31, 0, 0, 0, 0, time.UTC), nil
	default:
		return time.Time{}, errUnreadable
	}
}

// finish rejects the result when any part used to build it is below the floor, and
// normalizes a date phrase to midnight UTC, matching the deterministic date format.
func finish(when time.Time, used []float64) (time.Time, error) {
	for _, confidence := range used {
		if confidence < whenMinConfidence {
			return time.Time{}, errUnreadable
		}
	}
	return time.Date(when.Year(), when.Month(), when.Day(), 0, 0, 0, 0, time.UTC), nil
}

func indexOf(values []string, value string) (int, bool) {
	for i, candidate := range values {
		if candidate == value {
			return i, true
		}
	}
	return 0, false
}
