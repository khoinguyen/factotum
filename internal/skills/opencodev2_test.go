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
