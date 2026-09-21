package fake_test

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/judge/fake"
)

func TestFakeReturnsProgrammedAnswers(t *testing.T) {
	f := fake.New(map[string]judge.Answer{
		"ready": {Noul: 0.82},
		"which": {Choice: "c1", Probabilities: map[string]float64{"c0": 0.1, "c1": 0.9}, Confidence: 0.8},
	})
	resp, err := f.Ask(context.Background(), judge.Request{
		Questions: map[string]judge.Question{
			"ready": {Kind: judge.KindNoul, Instructions: "ready?"},
			"which": {Kind: judge.KindChoice, Instructions: "which?"},
		},
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if got := resp.Answers["ready"].Noul; got != 0.82 {
		t.Errorf("ready.Noul = %v, want 0.82", got)
	}
	if got := resp.Answers["which"].Choice; got != "c1" {
		t.Errorf("which.Choice = %q, want c1", got)
	}
}

func TestFakeRecordsRequests(t *testing.T) {
	f := fake.New(nil)
	req := judge.Request{State: "hello", Questions: map[string]judge.Question{
		"q": {Kind: judge.KindNoul, Instructions: "?"},
	}}
	if _, err := f.Ask(context.Background(), req); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("recorded %d requests, want 1", got)
	}
	if got := f.Requests()[0].State; got != "hello" {
		t.Errorf("recorded state = %v, want hello", got)
	}
}

func TestFakeCanReturnError(t *testing.T) {
	f := fake.New(nil)
	f.FailWith(judge.ErrUnavailable)
	if _, err := f.Ask(context.Background(), judge.Request{}); err == nil {
		t.Fatal("Ask() error = nil, want failure")
	}
}
