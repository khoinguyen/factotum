package pi

import (
	_ "embed"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
)

// msgExtension is the pi message receiver: the structural mirror of the
// OpenCode plugin, the same register/claim/inject/ack protocol client adapted
// to pi's extension and sendUserMessage APIs.
//
//go:embed extension/factotum-msg.js
var msgExtension []byte

// msgExtensionPath is where the receiver extension is staged in the
// environment, relative to the workspace root: pi loads project extensions from
// .pi/extensions/*.js.
const msgExtensionPath = ".pi/extensions/factotum-msg.js"

// MsgExtensionBytes returns a copy of the embedded receiver extension source.
func MsgExtensionBytes() []byte { return append([]byte(nil), msgExtension...) }

// MsgExtensionFile returns the isolation file that stages the receiver
// extension into a run's workspace.
func MsgExtensionFile() isolation.File {
	return isolation.File{Path: msgExtensionPath, Content: MsgExtensionBytes(), Mode: 0o644}
}

// MessagingEnabled reports whether env carries enough receiver configuration to
// install and use the extension: a project and an actor to register as.
func MessagingEnabled(env map[string]string) bool {
	return harness.MessagingEnabled(env)
}
