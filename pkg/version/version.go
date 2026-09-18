// Package version exposes build version information.
package version

// Version is the human-readable version string. It can be overridden at build
// time with -ldflags "-X github.com/khoinguyen/factotum/pkg/version.Version=...".
var Version = "dev"
