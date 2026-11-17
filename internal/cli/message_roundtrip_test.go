package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/harness/opencode"
)

// TestMessageLinkReachesTheInjectedTurn walks the whole path a receiver reads:
// `ft msg send --link` stores the attachment, `ft msg agent claim` hands the
// receiver's wire the link, and the plugin's formatMessage renders it into the
// text injected as the agent's next turn. The unit tests on each side pin the
// shape, but only the joined path catches a field that stops at the seam: the
// bug this covers was a link stored and never shown.
func TestMessageLinkReachesTheInjectedTurn(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not on PATH: %v", err)
	}
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("msg", "send", "actor:linker", "see the issue", "--project", projectID, "--from", "builder-t-1",
		"--link", "issue=https://example.invalid/2")

	registered := runJSON(t, r, "msg", "agent", "register", "--actor", "linker", "--harness", "opencode", "--project", projectID)
	runID, _ := registered["run_id"].(string)
	if runID == "" {
		t.Fatalf("register json missing run_id: %v", registered)
	}
	claimed := runJSON(t, r, "msg", "agent", "claim", "--run", runID, "--actor", "linker", "--project", projectID)
	message, ok := claimed["message"].(map[string]any)
	if !ok {
		t.Fatalf("claim returned no message: %v", claimed)
	}
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal claimed message: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "factotum-msg.js"), opencode.MsgPluginBytes(), 0o644); err != nil {
		t.Fatalf("stage plugin: %v", err)
	}
	script := `import { formatMessage } from "./factotum-msg.js";
const message = JSON.parse(process.argv[1]);
process.stdout.write(formatMessage(message));`
	cmd := exec.Command(nodePath, "--input-type=module", "-e", script, string(payload))
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("format the claimed message: %v", err)
	}
	if got := string(out); !strings.Contains(got, "[issue] https://example.invalid/2") {
		t.Fatalf("injected turn lost the link:\n%s", got)
	}
}
