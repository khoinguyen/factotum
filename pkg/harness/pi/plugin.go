// Package pi holds artifacts for driving the pi coding agent as a harness. Its
// message receiver extension is the structural mirror of the opencode plugin:
// the same register/claim/inject/ack protocol client, adapted to pi's extension
// and sendUserMessage APIs.
package pi

import (
	_ "embed"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
)

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
