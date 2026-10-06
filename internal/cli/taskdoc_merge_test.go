package cli

import (
	"testing"
	"time"
)

func ptrTo[T any](value T) *T { return &value }

func TestMergeScalar(t *testing.T) {
	base := 1
	theirs := 2
	cases := []struct {
		name     string
		ours     *int
		base     int
		theirs   int
		want     *int
		changed  bool
		conflict bool
	}{
		{"not supplied", nil, base, theirs, nil, false, false},
		{"unchanged from base", ptrTo(base), base, base, nil, false, false},
		{"unchanged despite concurrent write", ptrTo(base), base, theirs, nil, false, false},
		{"converged on the concurrent value", ptrTo(theirs), base, theirs, nil, false, false},
		{"document-only change", ptrTo(theirs), base, base, ptrTo(theirs), true, false},
		{"divergent change conflicts", ptrTo(3), base, theirs, nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, conflict := mergeScalar(tc.ours, tc.base, tc.theirs)
			if changed != tc.changed || conflict != tc.conflict {
				t.Fatalf("mergeScalar() changed, conflict = %v, %v; want %v, %v",
					changed, conflict, tc.changed, tc.conflict)
			}
			if !equalOptionalInt(got, tc.want) {
				t.Fatalf("mergeScalar() value = %v, want %v", derefInt(got), derefInt(tc.want))
			}
		})
	}
}

func TestMergeStringSet(t *testing.T) {
	base := []string{"a", "b"}
	theirs := []string{"a", "c"}
	cases := []struct {
		name     string
		ours     *[]string
		base     []string
		theirs   []string
		want     *[]string
		changed  bool
		conflict bool
	}{
		{"not supplied", nil, base, theirs, nil, false, false},
		{"unchanged regardless of order", ptrTo([]string{"b", "a"}), base, base, nil, false, false},
		{"unchanged despite concurrent write", ptrTo([]string{"b", "a"}), base, theirs, nil, false, false},
		{"converged on the concurrent value", ptrTo([]string{"c", "a"}), base, theirs, nil, false, false},
		{"document-only change", ptrTo([]string{"a", "b", "z"}), base, base, ptrTo([]string{"a", "b", "z"}), true, false},
		{"clearing the set is a change", ptrTo([]string{}), base, base, ptrTo([]string{}), true, false},
		{"divergent change conflicts", ptrTo([]string{"a", "x"}), base, theirs, nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, conflict := mergeStringSet(tc.ours, tc.base, tc.theirs)
			if changed != tc.changed || conflict != tc.conflict {
				t.Fatalf("mergeStringSet() changed, conflict = %v, %v; want %v, %v",
					changed, conflict, tc.changed, tc.conflict)
			}
			assertStringList(t, "mergeStringSet", got, tc.want)
		})
	}
}

func TestMergeStringSlice(t *testing.T) {
	base := []string{"a", "b"}
	theirs := []string{"a", "c"}
	cases := []struct {
		name     string
		ours     *[]string
		base     []string
		theirs   []string
		want     *[]string
		changed  bool
		conflict bool
	}{
		{"not supplied", nil, base, theirs, nil, false, false},
		{"unchanged in the same order", ptrTo([]string{"a", "b"}), base, base, nil, false, false},
		{"unchanged despite concurrent write", ptrTo([]string{"a", "b"}), base, theirs, nil, false, false},
		{"converged on the concurrent value", ptrTo([]string{"a", "c"}), base, theirs, nil, false, false},
		{"reordering is a change", ptrTo([]string{"b", "a"}), base, base, ptrTo([]string{"b", "a"}), true, false},
		{"document-only append", ptrTo([]string{"a", "b", "z"}), base, base, ptrTo([]string{"a", "b", "z"}), true, false},
		{"clearing the list is a change", ptrTo([]string{}), base, base, ptrTo([]string{}), true, false},
		{"divergent change conflicts", ptrTo([]string{"a", "x"}), base, theirs, nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, conflict := mergeStringSlice(tc.ours, tc.base, tc.theirs)
			if changed != tc.changed || conflict != tc.conflict {
				t.Fatalf("mergeStringSlice() changed, conflict = %v, %v; want %v, %v",
					changed, conflict, tc.changed, tc.conflict)
			}
			assertStringList(t, "mergeStringSlice", got, tc.want)
		})
	}
}

func TestMergeTime(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	theirs := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		ours     *time.Time
		base     *time.Time
		theirs   *time.Time
		want     *time.Time
		changed  bool
		conflict bool
	}{
		{"not supplied", nil, ptrTo(base), ptrTo(theirs), nil, false, false},
		{"unchanged despite equal value at another address", ptrTo(base), ptrTo(base), ptrTo(base), nil, false, false},
		{"unchanged despite concurrent write", ptrTo(base), ptrTo(base), ptrTo(theirs), nil, false, false},
		{"converged on the concurrent value", ptrTo(theirs), ptrTo(base), ptrTo(theirs), nil, false, false},
		{"document-only change", ptrTo(theirs), ptrTo(base), ptrTo(base), ptrTo(theirs), true, false},
		{"setting where base and concurrent were unset", ptrTo(theirs), nil, nil, ptrTo(theirs), true, false},
		{"divergent change conflicts", ptrTo(theirs), ptrTo(base), ptrTo(time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)), nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, conflict := mergeTime(tc.ours, tc.base, tc.theirs)
			if changed != tc.changed || conflict != tc.conflict {
				t.Fatalf("mergeTime() changed, conflict = %v, %v; want %v, %v",
					changed, conflict, tc.changed, tc.conflict)
			}
			if !equalOptionalTime(got, tc.want) {
				t.Fatalf("mergeTime() value = %v, want %v", got, tc.want)
			}
		})
	}
}

func assertStringList(t *testing.T, helper string, got, want *[]string) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Fatalf("%s() value = %v, want %v", helper, derefStringSlice(got), derefStringSlice(want))
	}
	if want == nil {
		return
	}
	if len(*got) != len(*want) {
		t.Fatalf("%s() value = %v, want %v", helper, *got, *want)
	}
	for i := range *want {
		if (*got)[i] != (*want)[i] {
			t.Fatalf("%s() value = %v, want %v", helper, *got, *want)
		}
	}
}

func equalOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
