package isolation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// runCombo drives any harness through any isolation backend using only the two
// ports. It never inspects a concrete backend or harness type, so every
// (backend x harness) combination runs the same code path.
func runCombo(backend isolation.IsolationBackend, h harness.Harness, req harness.Request) (harness.Result, error) {
	ctx := context.Background()

	spec, err := h.Spec(req)
	if err != nil {
		return harness.Result{}, err
	}
	handle, err := backend.Prepare(ctx, spec)
	if err != nil {
		return harness.Result{}, err
	}
	cmd, err := h.Command(req)
	if err != nil {
		return harness.Result{}, err
	}
	exec, err := backend.Exec(ctx, handle, cmd)
	if err != nil {
		return harness.Result{}, err
	}

	complete := false
	for ev := range exec.Events() {
		if h.Done(ev) {
			complete = true
		}
	}
	res, err := exec.Wait(ctx)
	if err != nil {
		return harness.Result{}, err
	}
	result, err := h.Result(res.Stdout)
	if err != nil {
		return harness.Result{}, err
	}
	result.ExitCode = res.ExitCode
	result.Complete = complete
	return result, nil
}

// TestMatrixComposesWithoutSpecialCasing proves the two ports compose across a
// backend x harness matrix through one generic path. Two backends (a
// no-isolation host stand-in and an isolating stand-in) each run two harnesses
// (a stdin-delivered agent and an argument-delivered one).
//
// Target matrix for the real adapters:
//
//	openshell x opencode   (verified in the OpenShell spike; first isolating pair)
//	openshell x pi
//	docker    x openshell (OpenShell itself provisioned as a docker workload)
//	local     x opencode   (first launcher pair, dev-only, no isolation)
//
// The fakes here stand in for those adapters until t-epm3ympvxz (local backend)
// and the OpenShell isolation milestone land; the composition itself is already
// exercised for every combination.
func TestMatrixComposesWithoutSpecialCasing(t *testing.T) {
	const (
		prompt   = "summarize the issue"
		model    = "vendor/model-1"
		sentinel = "TASK_COMPLETE"
	)
	tests := []struct {
		name     string
		backend  string
		harness  string
		delivery harnessfake.Delivery
	}{
		{"openshell/opencode", "openshell", "opencode", harnessfake.DeliveryStdin},
		{"openshell/pi", "openshell", "pi", harnessfake.DeliveryArg},
		{"local/opencode", "local", "opencode", harnessfake.DeliveryStdin},
		{"local/pi", "local", "pi", harnessfake.DeliveryArg},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			image := "ghcr.io/example/" + tc.harness + ":1"
			want := tc.harness + " says ok"

			backend := fake.New(tc.backend)
			backend.Program(
				isolation.ExecResult{ExitCode: 0, Stdout: []byte(want + "\n" + sentinel + "\n")},
				isolation.Event{Kind: isolation.EventOutput, Stream: isolation.StreamStdout, Message: want},
				isolation.Event{Kind: isolation.EventOutput, Stream: isolation.StreamStdout, Message: sentinel},
				isolation.Event{Kind: isolation.EventStatus, Message: "exited"},
			)
			h := harnessfake.New(tc.harness).
				WithImage(image).
				WithDelivery(tc.delivery).
				WithSentinel(sentinel)

			result, err := runCombo(backend, h, harness.Request{
				Prompt:  prompt,
				Model:   model,
				Workdir: "/work",
				Labels:  map[string]string{"task": "t-1"},
			})
			if err != nil {
				t.Fatalf("runCombo(%s) error = %v", tc.name, err)
			}
			if result.Output != want {
				t.Errorf("Output = %q, want %q", result.Output, want)
			}
			if !result.Complete {
				t.Errorf("Complete = false, want the harness sentinel to fire")
			}
			if result.ExitCode != 0 {
				t.Errorf("ExitCode = %d, want 0", result.ExitCode)
			}

			prepared := backend.Prepared()
			if len(prepared) != 1 {
				t.Fatalf("Prepared() = %d specs, want 1", len(prepared))
			}
			if prepared[0].Image.Ref != image {
				t.Errorf("prepared image = %q, want %q", prepared[0].Image.Ref, image)
			}
			if prepared[0].Labels["task"] != "t-1" {
				t.Errorf("prepared labels = %v, want task=t-1", prepared[0].Labels)
			}

			cmds := backend.Commands()
			if len(cmds) != 1 {
				t.Fatalf("Commands() = %d, want 1", len(cmds))
			}
			joined := strings.Join(cmds[0].Argv, " ")
			if !strings.Contains(joined, "--model "+model) {
				t.Errorf("argv %v lacks the model flag", cmds[0].Argv)
			}
			switch tc.delivery {
			case harnessfake.DeliveryStdin:
				if string(cmds[0].Stdin) != prompt {
					t.Errorf("stdin = %q, want the prompt", cmds[0].Stdin)
				}
			case harnessfake.DeliveryArg:
				if !strings.Contains(joined, prompt) {
					t.Errorf("argv %v does not carry the prompt", cmds[0].Argv)
				}
			}
		})
	}
}
