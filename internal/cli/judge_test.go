package cli

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/judge"
)

func TestNewDepsWithoutKeyUsesDisabledJudge(t *testing.T) {
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard,
		func(string) string { return "" })
	if _, err := deps.Judge.Ask(context.Background(), judge.Request{}); !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("Ask() error = %v, want ErrUnavailable with no key", err)
	}
}

func TestNewDepsWithKeyDoesNotUseDisabledJudge(t *testing.T) {
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard,
		func(name string) string {
			if name == "TYPESAFE_API_KEY" {
				return "test-key"
			}
			return ""
		})
	if _, disabled := deps.Judge.(judge.Disabled); disabled {
		t.Fatal("judge is Disabled even though TYPESAFE_API_KEY is set")
	}
}
