package cli

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/khoinguyen/factotum/internal/config"
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

func TestNewJudgeUnknownProviderIsDisabled(t *testing.T) {
	j := newJudge(func(string) string { return "" }, config.Config{
		Judge: config.Judge{Provider: "not-a-provider", Options: map[string]string{"secret_api_key": "k"}},
	})
	if _, disabled := j.(judge.Disabled); !disabled {
		t.Fatal("unknown provider should fall back to Disabled")
	}
}

func TestNewJudgeTypeSafeWithKeyIsNotDisabled(t *testing.T) {
	j := newJudge(func(string) string { return "" }, config.Config{
		Judge: config.Judge{Provider: "typesafe", Options: map[string]string{"secret_api_key": "sk-1"}},
	})
	if _, disabled := j.(judge.Disabled); disabled {
		t.Fatal("typesafe provider with a key should build a judge")
	}
}

func TestNewJudgeTypeSafeWithoutKeyIsDisabled(t *testing.T) {
	j := newJudge(func(string) string { return "" }, config.Config{
		Judge: config.Judge{Provider: "typesafe"},
	})
	if _, disabled := j.(judge.Disabled); !disabled {
		t.Fatal("typesafe provider without a key should be Disabled")
	}
}
