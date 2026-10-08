package docker_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
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

// TestStopTerminatesWorkloadLive is the live half of the Stop contract: after Stop
// the container a prior Exec left running is stopped, so no remote process
// survives, and the handle stays usable because the next Exec restarts it. It
// needs a real daemon and is opt-in like TestConformance:
//
//	FACTOTUM_DOCKER_TEST=1 go test -run TestStopTerminatesWorkloadLive ./pkg/isolation/docker/
func TestStopTerminatesWorkloadLive(t *testing.T) {
	if os.Getenv("FACTOTUM_DOCKER_TEST") == "" {
		t.Skip("set FACTOTUM_DOCKER_TEST=1 with a running docker daemon to run the live Stop test")
	}
	ctx := context.Background()
	be := docker.New(docker.Options{Image: "alpine:3.20"})
	h, err := be.Prepare(ctx, isolation.Spec{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	ex, err := be.Exec(ctx, h, isolation.Command{Argv: []string{"sh", "-c", "sleep 300"}})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if got := containerRunning(t, h.ID()); got != "true" {
		t.Fatalf("container running = %q before Stop, want true", got)
	}

	if err := be.Stop(ctx, h); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := ex.Wait(ctx); err != nil {
		t.Fatalf("Wait() after Stop error = %v", err)
	}
	if got := containerRunning(t, h.ID()); got != "false" {
		t.Fatalf("container running = %q after Stop, want false (workload survived)", got)
	}

	res, err := be.Exec(ctx, h, isolation.Command{Argv: []string{"sh", "-c", "printf ok"}})
	if err != nil {
		t.Fatalf("Exec(after Stop) error = %v", err)
	}
	out, err := res.Wait(ctx)
	if err != nil || string(out.Stdout) != "ok" {
		t.Fatalf("Exec(after Stop) = %q, %v; want ok (handle must stay usable)", out.Stdout, err)
	}
}

// TestCredentialReachesContainerLive verifies the credential transport end to
// end on a real daemon: the value is staged in a file and reaches the container
// through `docker exec --env-file`, never the CLI's argv or environment.
func TestCredentialReachesContainerLive(t *testing.T) {
	if os.Getenv("FACTOTUM_DOCKER_TEST") == "" {
		t.Skip("set FACTOTUM_DOCKER_TEST=1 with a running docker daemon to run the live credential test")
	}
	ctx := context.Background()
	be := docker.New(docker.Options{
		Image:       "alpine:3.20",
		Credentials: resolverFunc(func(context.Context, isolation.Credential) (string, error) { return "s3cr3t", nil }),
	})
	h, err := be.Prepare(ctx, isolation.Spec{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	if err := be.AttachCredential(ctx, h, isolation.Credential{Provider: "p", Ref: "r", EnvVar: "FT_CRED"}); err != nil {
		t.Fatalf("AttachCredential() error = %v", err)
	}
	ex, err := be.Exec(ctx, h, isolation.Command{Argv: []string{"sh", "-c", `printf %s "$FT_CRED"`}})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	res, err := ex.Wait(ctx)
	if err != nil || string(res.Stdout) != "s3cr3t" {
		t.Fatalf("Exec() = %q, %v; want the credential in the container", res.Stdout, err)
	}
}

// TestResourcesReachContainerLive verifies on a real daemon that a spec's CPU
// and memory caps become the container's limits, not merely a `docker run`
// flag the daemon ignores. It is opt-in like TestConformance:
//
//	FACTOTUM_DOCKER_TEST=1 go test -run TestResourcesReachContainerLive ./pkg/isolation/docker/
func TestResourcesReachContainerLive(t *testing.T) {
	if os.Getenv("FACTOTUM_DOCKER_TEST") == "" {
		t.Skip("set FACTOTUM_DOCKER_TEST=1 with a running docker daemon to run the live resource test")
	}
	ctx := context.Background()
	be := docker.New(docker.Options{Image: "alpine:3.20"})
	h, err := be.Prepare(ctx, isolation.Spec{Resources: isolation.Resources{CPUs: "0.5", Memory: "128m"}})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	out, err := exec.Command("docker", "inspect", "--format",
		"{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}}", h.ID()).Output()
	if err != nil {
		t.Fatalf("docker inspect %q error = %v", h.ID(), err)
	}
	if got := strings.TrimSpace(string(out)); got != "500000000 134217728" {
		t.Fatalf("container limits = %q, want the 0.5 CPU / 128MiB caps", got)
	}
}

func containerRunning(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", name).Output()
	if err != nil {
		t.Fatalf("docker inspect %q error = %v", name, err)
	}
	return strings.TrimSpace(string(out))
}
