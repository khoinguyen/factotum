package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedSkillsNameTheOpenCodeV2Harness pins that every embedded usage
// skill that runs a harness points at OpenCode V2 (`--harness opencode2`), not
// only the V1 `opencode`. A V2 reader must not be misled into selecting V1.
func TestEmbeddedSkillsNameTheOpenCodeV2Harness(t *testing.T) {
	for _, name := range []string{"ft", "groom", "software-factory"} {
		skill, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", name, err)
		}
		if !strings.Contains(skill.Body, "opencode2") {
			t.Errorf("skill %q does not name the OpenCode V2 harness `opencode2`; a V2 reader would only see the V1 `opencode`", name)
		}
	}
}

// TestDocsStateV2RunsOnAPrivateServer pins the receiver claim the README and the
// ft skill make. A V2 `ft run` must be described as launching on its own private
// server (`--standalone`), not as attaching to the shared background service:
// that service's environment carries no FACTOTUM_*, so a receiver it loads is
// inert and a `--actor` run silently never registers. Both docs are asserted
// because either one alone would let a reader believe the staged receiver works.
func TestDocsStateV2RunsOnAPrivateServer(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readme := string(data)
	for _, want := range []string{"--standalone", "shared background service"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README does not mention %q; describe how a V2 run reaches its receiver", want)
		}
	}
	skill, err := Get("ft")
	if err != nil {
		t.Fatalf("Get(ft) error = %v", err)
	}
	for _, want := range []string{"--standalone", "private server"} {
		if !strings.Contains(skill.Body, want) {
			t.Errorf("ft skill does not mention %q; `ft run`'s V2 receiver claim must match shipped behavior", want)
		}
	}
}

// TestREADMEExplainsOpenCodeV2Migration pins the OpenCode V2 facts a reader
// needs so the README does not describe only V1: the V2 harness and binary, the
// plugin API port and package (`@opencode/plugin`, Plugin.define/setup), the
// config key rename (`plugin` to `plugins`), the V2 project plugin dir
// (`.opencode/plugins/`), the TUI config move (`tui.json` to `cli.json`), and the
// `opencode2 api get /api/plugin` verification.
func TestREADMEExplainsOpenCodeV2Migration(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"opencode2",
		"harness = \"opencode2\"",
		"@opencode/plugin",
		"Plugin.define",
		"`plugin` to `plugins`",
		".opencode/plugins/",
		"cli.json",
		"tui.json",
		"opencode2 api get /api/plugin",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("README does not mention %q; keep the OpenCode V2 migration guidance current", want)
		}
	}
}
