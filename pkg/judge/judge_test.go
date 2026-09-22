package judge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/judge"
)

func TestDisabledReturnsErrUnavailable(t *testing.T) {
	var j judge.Judge = judge.Disabled{}
	_, err := j.Ask(context.Background(), judge.Request{
		State:     "anything",
		Questions: map[string]judge.Question{"q": {Kind: judge.KindYesNo, Instructions: "?"}},
	})
	if !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("Ask() error = %v, want ErrUnavailable", err)
	}
}

func TestQuestionKinds(t *testing.T) {
	cases := map[judge.Kind]string{
		judge.KindYesNo:  "yes_no",
		judge.KindChoice: "choice",
		judge.KindRating: "rating",
	}
	for kind, want := range cases {
		if string(kind) != want {
			t.Errorf("Kind %v = %q, want %q", kind, string(kind), want)
		}
	}
}
