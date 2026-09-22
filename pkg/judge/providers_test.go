package judge_test

import (
	"testing"

	"github.com/khoinguyen/factotum/pkg/judge"
)

func TestNewEmptyNameIsDisabled(t *testing.T) {
	j, err := judge.New("", func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := j.(judge.Disabled); !ok {
		t.Fatal("empty provider name should be Disabled")
	}
}

func TestNewUnknownProviderErrors(t *testing.T) {
	if _, err := judge.New("not-registered", nil, nil); err == nil {
		t.Fatal("unknown provider should be an error")
	}
}

func TestRegisterAndNewPassesOptionsAndEnv(t *testing.T) {
	name := "test-provider-" + t.Name()
	judge.Register(name, func(getenv func(string) string, options map[string]string) (judge.Judge, error) {
		if options["ready"] != "yes" {
			t.Errorf("options = %v, want ready=yes", options)
		}
		if getenv("SOME_KEY") != "value" {
			t.Errorf("getenv not passed through")
		}
		return judge.Disabled{}, nil
	})
	if _, err := judge.New(name, func(key string) string {
		if key == "SOME_KEY" {
			return "value"
		}
		return ""
	}, map[string]string{"ready": "yes"}); err != nil {
		t.Fatalf("New() error = %v", err)
	}
}
