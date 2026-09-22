package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDisabledBreakdownIsUnavailable(t *testing.T) {
	_, err := Disabled{}.Breakdown(context.Background(), Request{Prompt: "do a thing"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Breakdown() error = %v, want ErrUnavailable", err)
	}
}

func TestNewEmptyNameReturnsDisabled(t *testing.T) {
	built, err := New("", nil, nil)
	if err != nil {
		t.Fatalf("New(\"\") error = %v", err)
	}
	if _, ok := built.(Disabled); !ok {
		t.Fatalf("New(\"\") = %T, want Disabled", built)
	}
}

func TestNewUnknownProviderErrors(t *testing.T) {
	_, err := New("does-not-exist", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("New(unknown) error = %v, want the unknown name", err)
	}
}

func TestRegisterAndNewProvider(t *testing.T) {
	const name = "test-provider-agent-7351"
	want := fakeAgent{}
	Register(name, func(_ func(string) string, _ map[string]string) (Agent, error) {
		return want, nil
	})
	built, err := New(name, nil, nil)
	if err != nil {
		t.Fatalf("New(%q) error = %v", name, err)
	}
	if _, ok := built.(fakeAgent); !ok {
		t.Fatalf("New(%q) = %T, want fakeAgent", name, built)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	const name = "test-provider-dup-90210"
	Register(name, func(_ func(string) string, _ map[string]string) (Agent, error) {
		return fakeAgent{}, nil
	})
	defer func() {
		if recover() == nil {
			t.Fatal("second Register with the same name did not panic")
		}
	}()
	Register(name, func(_ func(string) string, _ map[string]string) (Agent, error) {
		return fakeAgent{}, nil
	})
}

type fakeAgent struct{}

func (fakeAgent) Breakdown(context.Context, Request) (Plan, error) { return Plan{}, nil }
