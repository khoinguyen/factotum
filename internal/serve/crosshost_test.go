package serve

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/harness/opencode"
	"github.com/khoinguyen/factotum/pkg/harness/pi"
)

// These tests prove the shipped receivers reach a hub over the cross-host
// transport, not through a local store: each loads the real embedded plugin in
// the real opencode/pi binary, points it only at a token-gated `ft serve`
// handler via FACTOTUM_MSG_URL/FACTOTUM_SERVE_TOKEN, and asserts a pre-sent
// message is delivered. FACTOTUM_BIN names a path that does not exist, so the
// local `ft` fallback cannot satisfy the test: only /api/msg/* can.
//
// They call a real model, so they are opt-in and skipped when the gate is unset
// (CI stays hermetic). Run them with a usable provider/model, for example:
//
//	FACTOTUM_MSG_E2E_OPENCODE_MODEL=opencode-go/deepseek-v4.1-flash \
//	FACTOTUM_MSG_E2E_PI_MODEL=<provider>/<model> \
//	  go test -count=1 -run TestCrossHostDeliveryRealReceivers -v ./internal/serve/
const (
	opencodeModelGate = "FACTOTUM_MSG_E2E_OPENCODE_MODEL"
	piModelGate       = "FACTOTUM_MSG_E2E_PI_MODEL"
	crossHostPrompt   = "Do not use any tools. Reply with the single word READY."
)

type crossHostReceiver struct {
	name     string
	bin      string
	modelVar string
	stage    func(t *testing.T, dir string)
	args     func(model string) []string
}

func crossHostReceivers() []crossHostReceiver {
	return []crossHostReceiver{
		{
			name:     "opencode",
			bin:      "opencode",
			modelVar: opencodeModelGate,
			stage: func(t *testing.T, dir string) {
				t.Helper()
				pluginDir := filepath.Join(dir, ".opencode", "plugin")
				if err := os.MkdirAll(pluginDir, 0o755); err != nil {
					t.Fatalf("MkdirAll(%s) error = %v", pluginDir, err)
				}
				if err := os.WriteFile(filepath.Join(pluginDir, "factotum-msg.js"), opencode.MsgPluginBytes(), 0o644); err != nil {
					t.Fatalf("write opencode plugin: %v", err)
				}
			},
			args: func(model string) []string {
				return []string{"run", "--model", model, "--auto", crossHostPrompt}
			},
		},
		{
			name:     "pi",
			bin:      "pi",
			modelVar: piModelGate,
			stage: func(t *testing.T, dir string) {
				t.Helper()
				extDir := filepath.Join(dir, ".pi", "extensions")
				if err := os.MkdirAll(extDir, 0o755); err != nil {
					t.Fatalf("MkdirAll(%s) error = %v", extDir, err)
				}
				if err := os.WriteFile(filepath.Join(extDir, "factotum-msg.js"), pi.MsgExtensionBytes(), 0o644); err != nil {
					t.Fatalf("write pi extension: %v", err)
				}
			},
			args: func(model string) []string {
				return []string{"--approve", "--model", model, "-p", crossHostPrompt}
			},
		},
	}
}

// TestCrossHostDeliveryRealReceivers sends a message to a hub actor and asserts
// a receiver in a separate process, configured with only the hub URL and token,
// claims it over HTTP.
func TestCrossHostDeliveryRealReceivers(t *testing.T) {
	for _, receiver := range crossHostReceivers() {
		t.Run(receiver.name, func(t *testing.T) {
			model := os.Getenv(receiver.modelVar)
			if model == "" {
				t.Skipf("set %s to a provider/model to run the real cross-host delivery test", receiver.modelVar)
			}
			binaryPath, err := exec.LookPath(receiver.bin)
			if err != nil {
				t.Skipf("%s is not on PATH: %v", receiver.bin, err)
			}

			f := newFixture(t)
			project := f.addProject(t, "remote", "Remote")
			hub := newTestServer(t, f, Options{Project: project.ID, Token: testToken, Tasks: f.tasks, Messages: f.messages})

			actor := core.ActorID("act-remote")
			message, err := f.messages.Send(context.Background(), app.SendMessageInput{
				ProjectID: project.ID,
				Target:    "actor:" + string(actor),
				Body:      "Cross-host probe: reply with the single word RECEIVED.",
			})
			if err != nil {
				t.Fatalf("Send() error = %v", err)
			}

			dir := t.TempDir()
			receiver.stage(t, dir)

			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binaryPath, receiver.args(model)...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"FACTOTUM_PROJECT="+string(project.ID),
				"FACTOTUM_ACTOR="+string(actor),
				"FACTOTUM_HARNESS="+receiver.name,
				"FACTOTUM_MSG_URL="+hub.URL,
				"FACTOTUM_SERVE_TOKEN="+testToken,
				// Deliberately not a real binary: the local ft path must not be
				// what delivers the message.
				"FACTOTUM_BIN="+filepath.Join(dir, "no-local-ft"),
				"FACTOTUM_MSG_WAIT_MS=2000",
				"FACTOTUM_MSG_TTL_MS=60000",
			)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatalf("start %s: %v", receiver.bin, err)
			}

			// Claiming moves the message to delivered, so reaching that state
			// proves the receiver registered and claimed over HTTP: with
			// FACTOTUM_BIN pointing nowhere, no local ft path can have done it.
			// Inject/ack run the same receiver code whichever transport carries
			// the protocol, and are covered by the Node unit tests.
			claimed := waitForClaimed(t, f, message.ID, 140*time.Second)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			if !claimed {
				final, _ := f.messages.Get(context.Background(), message.ID)
				t.Fatalf("message %s was not claimed over the HTTP transport (final state %v); %s output:\n%s",
					message.ID, finalState(final), receiver.name, output.String())
			}
		})
	}
}

// finalState reports a message's state for a failure message, or "?" when it
// cannot be fetched.
func finalState(message *core.Message) core.MessageState {
	if message == nil {
		return "?"
	}
	return message.State
}

// waitForClaimed polls a message until a receiver has claimed it (delivered) or
// already acked it (read), or the deadline passes.
func waitForClaimed(t *testing.T, f *fixture, id core.MessageID, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		message, err := f.messages.Get(context.Background(), id)
		if err == nil && (message.State == core.MessageDelivered || message.State == core.MessageRead) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
