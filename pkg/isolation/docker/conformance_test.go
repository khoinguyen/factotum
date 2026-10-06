package docker_test

import (
	"os"
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/conformance"
	"github.com/khoinguyen/factotum/pkg/isolation/docker"
)

// TestConformance runs the shared IsolationBackend contract against the docker
// backend on a live daemon. It is opt-in because it needs a working docker CLI
// and daemon (and pulls the small test image if absent); CI has neither. Run it
// with:
//
//	FACTOTUM_DOCKER_TEST=1 mise run test-docker
//
// The suite uses a small Alpine image rather than the agent image so the check
// stays fast; every subtest gets a fresh backend and container name, and the
// suite deletes each container it prepares.
func TestConformance(t *testing.T) {
	if os.Getenv("FACTOTUM_DOCKER_TEST") == "" {
		t.Skip("set FACTOTUM_DOCKER_TEST=1 with a running docker daemon to run the live conformance suite")
	}
	conformance.Run(t, func(t *testing.T) isolation.IsolationBackend {
		return docker.New(docker.Options{Image: "alpine:3.20"})
	}, conformance.WithLogs())
}
