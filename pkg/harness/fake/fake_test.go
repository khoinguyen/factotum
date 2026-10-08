package fake_test

import (
	"context"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

func TestSpecCarriesImageAndWorkdir(t *testing.T) {
	h := fake.New("agent").WithImage("ghcr.io/example/agent:1")
	spec, err := h.Spec(harness.Request{Workdir: "/work"})
	if err != nil {
		t.Fatalf("Spec() error = %v", err)
	}
	if spec.Image.Ref != "ghcr.io/example/agent:1" {
		t.Errorf("image = %q, want ghcr.io/example/agent:1", spec.Image.Ref)
	}
	if spec.Workdir != "/work" {
		t.Errorf("workdir = %q, want /work", spec.Workdir)
	}
}

func TestSpecCarriesEntrypointAndUser(t *testing.T) {
	h := fake.New("agent").
		WithImage("ghcr.io/example/agent:1").
		WithEntrypoint("opencode", "run").
		WithUser("1000:1000")
	spec, err := h.Spec(harness.Request{Workdir: "/work"})
	if err != nil {
		t.Fatalf("Spec() error = %v", err)
	}

	backend := isofake.New("fake")
	if _, err := backend.Prepare(context.Background(), spec); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepared() = %d specs, want 1", len(prepared))
	}
	image := prepared[0].Image
	if strings.Join(image.Entrypoint, " ") != "opencode run" {
		t.Errorf("entrypoint = %v, want [opencode run]", image.Entrypoint)
	}
	if image.User != "1000:1000" {
		t.Errorf("user = %q, want 1000:1000", image.User)
	}
}

func TestCommandDeliversPrompt(t *testing.T) {
	tests := []struct {
		name     string
		delivery fake.Delivery
		assert   func(t *testing.T, cmd isolation.Command)
	}{
		{
			name:     "argument",
			delivery: fake.DeliveryArg,
			assert: func(t *testing.T, cmd isolation.Command) {
				if !contains(cmd.Argv, "do the thing") {
					t.Fatalf("argv %v does not carry the prompt", cmd.Argv)
				}
			},
		},
		{
			name:     "stdin",
			delivery: fake.DeliveryStdin,
			assert: func(t *testing.T, cmd isolation.Command) {
				if string(cmd.Stdin) != "do the thing" {
					t.Fatalf("stdin = %q, want prompt", cmd.Stdin)
				}
			},
		},
		{
			name:     "environment",
			delivery: fake.DeliveryEnv,
			assert: func(t *testing.T, cmd isolation.Command) {
				if cmd.Env["PROMPT"] != "do the thing" {
					t.Fatalf("env PROMPT = %q", cmd.Env["PROMPT"])
				}
			},
		},
		{
			name:     "file",
			delivery: fake.DeliveryFile,
			assert: func(t *testing.T, cmd isolation.Command) {
				if !contains(cmd.Argv, "/work/prompt.txt") {
					t.Fatalf("argv %v does not reference the prompt file", cmd.Argv)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := fake.New("agent").WithDelivery(tc.delivery)
			cmd, err := h.Command(harness.Request{Prompt: "do the thing", Workdir: "/work"})
			if err != nil {
				t.Fatalf("Command() error = %v", err)
			}
			tc.assert(t, cmd)
		})
	}
}

// TestCommandRequestsTTYWhenInteractive pins the port contract the run service
// relies on: an interactive request marks the command as needing a terminal, so
// the backend attaches one; a headless request does not.
func TestCommandRequestsTTYWhenInteractive(t *testing.T) {
	h := fake.New("agent")
	interactive, err := h.Command(harness.Request{Prompt: "hi", Interactive: true})
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	if !interactive.TTY {
		t.Error("TTY = false for an interactive request, want true")
	}
	headless, err := h.Command(harness.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	if headless.TTY {
		t.Error("TTY = true for a headless request, want false")
	}
}

func TestCommandCarriesModelFlag(t *testing.T) {
	h := fake.New("agent")
	cmd, err := h.Command(harness.Request{Prompt: "hi", Model: "vendor/model"})
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	joined := strings.Join(cmd.Argv, " ")
	if !strings.Contains(joined, "--model vendor/model") {
		t.Fatalf("argv %v lacks the model flag", cmd.Argv)
	}
}

func TestDoneDetectsSentinel(t *testing.T) {
	h := fake.New("agent").WithSentinel("TASK_COMPLETE")
	if h.Done(isolation.Event{Kind: isolation.EventOutput, Message: "still working"}) {
		t.Fatal("Done() = true before the sentinel")
	}
	if !h.Done(isolation.Event{Kind: isolation.EventOutput, Message: "ok TASK_COMPLETE"}) {
		t.Fatal("Done() = false at the sentinel")
	}
}

func TestResultParsesOutput(t *testing.T) {
	h := fake.New("agent")
	got, err := h.Result([]byte("  the answer  \n"))
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if got.Output != "the answer" {
		t.Fatalf("Output = %q, want trimmed answer", got.Output)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
