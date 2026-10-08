package pi_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/harness/pi"
)

// TestMsgExtensionLoadsOnInstalledPi loads the receiver extension in the
// installed pi. It is the load guard: pi's loader requires the module to
// default-export a factory function, so an object (the opencode plugin shape)
// fails with "does not export a valid factory function". Registration is
// observed best-effort because a machine with no usable model exits before a
// session starts; when pi does not reach registration the test skips rather
// than flaking (the deterministic protocol checks live in the extension's Node
// unit tests). Gated on the binary so CI without pi skips.
func TestMsgExtensionLoadsOnInstalledPi(t *testing.T) {
	piPath, err := exec.LookPath("pi")
	if err != nil {
		t.Skipf("pi is not on PATH: %v", err)
	}

	dir := t.TempDir()
	extDir := filepath.Join(dir, ".pi", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "factotum-msg.js"), pi.MsgExtensionBytes(), 0o644); err != nil {
		t.Fatalf("write extension: %v", err)
	}

	callsPath := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "ft-stub.sh")
	stubScript := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$CALLS\"\n" +
		"case \"$3\" in\n" +
		"  register) printf '{\"run_id\":\"run-1\"}';;\n" +
		"  claim) printf '{\"found\":false}';;\n" +
		"  *) printf '{\"ok\":true}';;\n" +
		"esac\n"
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	env := append(os.Environ(),
		"FACTOTUM_PROJECT=prj-test",
		"FACTOTUM_ACTOR=act-test",
		"FACTOTUM_BIN="+stub,
		"FACTOTUM_MSG_WAIT_MS=10",
		"FACTOTUM_MSG_TTL_MS=1000",
		"CALLS="+callsPath,
	)
	var lastOut []byte
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd := exec.CommandContext(ctx, piPath, "--offline", "--approve", "--no-session", "-p", "hi")
		cmd.Dir = dir
		cmd.Env = env
		lastOut, _ = cmd.CombinedOutput()
		cancel()

		// The regression this test exists for: an invalid extension shape is
		// reported as a load error naming the extension.
		if extensionLoadFailed(lastOut) {
			t.Fatalf("receiver extension failed to load on pi:\n%s", lastOut)
		}
		if calls, _ := os.ReadFile(callsPath); strings.Contains(string(calls), "msg agent register") {
			return // loaded and registered on session_start
		}
	}
	t.Skipf("pi did not reach a session (model unavailable); the load guard still passed; skipped rather than flaked\npi output:\n%s", lastOut)
}

// extensionLoadFailed reports whether pi logged a load failure for the receiver
// extension. pi writes, e.g.
//
//	Error: Failed to load extension "/path/.pi/extensions/factotum-msg.js": Extension does not export a valid factory function: ...
//
// The pattern is scoped to the extension's own file so an unrelated extension
// failure does not trip it.
func extensionLoadFailed(out []byte) bool {
	text := string(out)
	return strings.Contains(text, "factotum-msg.js") &&
		(strings.Contains(text, "Failed to load extension") ||
			strings.Contains(text, "does not export a valid factory function"))
}

// TestExtensionLoadFailedDetector pins the detector against the exact pi load
// error shape so the guard cannot silently become toothless, and against an
// unrelated extension failure.
func TestExtensionLoadFailedDetector(t *testing.T) {
	broken := []byte(`Error: Failed to load extension "/tmp/x/.pi/extensions/factotum-msg.js": Extension does not export a valid factory function: /tmp/x/.pi/extensions/factotum-msg.js`)
	if !extensionLoadFailed(broken) {
		t.Fatal("extensionLoadFailed() = false for a genuine receiver load error")
	}
	other := []byte(`Error: Failed to load extension "/home/u/.pi/extensions/other.js": Extension does not export a valid factory function: /home/u/.pi/extensions/other.js`)
	if extensionLoadFailed(other) {
		t.Fatal("extensionLoadFailed() = true for an unrelated extension failure")
	}
}

// TestStagedExtensionMatchesEmbeddedSource pins that the staged file is the
// embedded extension, so the run and the shipped source cannot drift.
func TestStagedExtensionMatchesEmbeddedSource(t *testing.T) {
	if string(pi.MsgExtensionFile().Content) != string(pi.MsgExtensionBytes()) {
		t.Fatal("MsgExtensionFile content differs from MsgExtensionBytes")
	}
	if got := pi.MsgExtensionFile().Path; got != ".pi/extensions/factotum-msg.js" {
		t.Fatalf("MsgExtensionFile path = %q", got)
	}
}

// TestMsgExtensionUnitTests runs the extension's Node unit tests, so
// `mise run test` covers the receiver protocol logic (register/claim/inject/
// ack) without pi or a store. Gated on node.
func TestMsgExtensionUnitTests(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not on PATH: %v", err)
	}
	cmd := exec.Command(nodePath, "--test", "extension/factotum-msg.test.mjs")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node extension tests failed: %v\n%s", err, out)
	}
}
