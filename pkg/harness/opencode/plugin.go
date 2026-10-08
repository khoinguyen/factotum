package opencode

import (
	_ "embed"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
)

//go:embed plugin/factotum-msg.js
var msgPlugin []byte

// msgPluginPath is where the receiver plugin is staged in the environment,
// relative to the workspace root: OpenCode loads project plugins from
// .opencode/plugin/*.js.
const msgPluginPath = ".opencode/plugin/factotum-msg.js"

// MsgPluginBytes returns a copy of the embedded receiver plugin source.
func MsgPluginBytes() []byte { return append([]byte(nil), msgPlugin...) }

// MsgPluginFile returns the isolation file that stages the receiver plugin into
// a run's workspace.
func MsgPluginFile() isolation.File {
	return isolation.File{Path: msgPluginPath, Content: MsgPluginBytes(), Mode: 0o644}
}

// MessagingEnabled reports whether env carries enough receiver configuration to
// install and use the plugin: a project and an actor to register as.
func MessagingEnabled(env map[string]string) bool {
	return harness.MessagingEnabled(env)
}
