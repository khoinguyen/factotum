package check

import (
	"context"
	"testing"
)

func TestSpecHashStableAndFieldSensitive(t *testing.T) {
	base := Spec{ID: "t-1", Title: "title", Kind: "task", Body: "body"}
	if base.Hash() != (Spec{ID: "t-1", Title: "title", Kind: "task", Body: "body"}).Hash() {
		t.Fatal("identical specs must hash identically")
	}
	// id is immutable context and is deliberately outside the hash set.
	if base.Hash() != (Spec{ID: "t-2", Title: "title", Kind: "task", Body: "body"}).Hash() {
		t.Fatal("id must not change the content hash")
	}
	for name, other := range map[string]Spec{
		"title": {ID: "t-1", Title: "other", Kind: "task", Body: "body"},
		"kind":  {ID: "t-1", Title: "title", Kind: "milestone", Body: "body"},
		"body":  {ID: "t-1", Title: "title", Kind: "task", Body: "other"},
	} {
		if base.Hash() == other.Hash() {
			t.Fatalf("%s change did not change the hash", name)
		}
	}
}

func TestSpecHashIsUnambiguousAcrossFields(t *testing.T) {
	// A naive concatenation would collide these; the length prefix must not.
	left := Spec{Title: "ab", Kind: "", Body: "c"}
	right := Spec{Title: "a", Kind: "", Body: "bc"}
	if left.Hash() == right.Hash() {
		t.Fatal("field boundaries are not encoded in the hash")
	}
}

func TestRegistryKeepsIndependentChecks(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("a", stub{name: "a", version: "1"}); err != nil {
		t.Fatalf("Register(a) error = %v", err)
	}
	if err := reg.Register("b", stub{name: "b", version: "1"}); err != nil {
		t.Fatalf("Register(b) error = %v", err)
	}
	if names := reg.Names(); len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("Names() = %v, want sorted [a b]", names)
	}
	if _, ok := reg.Lookup("a"); !ok {
		t.Fatal("Lookup(a) not found")
	}
	if err := reg.Register("a", stub{name: "a", version: "2"}); err == nil {
		t.Fatal("duplicate registration must fail")
	}
}

func TestResultRoundTripsThroughJSON(t *testing.T) {
	in := Result{
		Check:           "grooming",
		CheckVersion:    "1",
		Model:           "jev-latest",
		ContentHash:     "abc",
		Verdict:         NeedsGrooming,
		Confidence:      0.8,
		JudgeConfidence: 0.6,
		Dimensions:      []Dimension{{Name: "scope_bounded", Value: 0.5}},
		Findings:        []Finding{{Dimension: "scope_bounded", Aspect: "boundary vague", Owner: OwnerAgent, Edit: "tighten"}},
	}
	data, err := in.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	out, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Verdict != in.Verdict || len(out.Dimensions) != 1 || len(out.Findings) != 1 {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
	if out.Findings[0].Owner != OwnerAgent || out.Findings[0].Edit != "tighten" {
		t.Fatalf("finding not preserved: %+v", out.Findings[0])
	}
}

type stub struct {
	name    string
	version string
}

func (s stub) Name() string    { return s.name }
func (s stub) Version() string { return s.version }
func (s stub) Run(ctx context.Context, spec Spec) (Result, error) {
	return Result{Check: s.name, CheckVersion: s.version}, nil
}
