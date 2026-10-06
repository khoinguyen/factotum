package docker

import (
	"reflect"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

// TestRunArgsAreDeterministic pins the reproducibility contract: the same spec
// and workspace always yield the same container definition (no timestamps or
// random content), so a re-run reproduces the environment.
func TestRunArgsAreDeterministic(t *testing.T) {
	spec := isolation.Spec{
		Image:  isolation.Image{Ref: "img:1", User: "1000:1000"},
		Env:    map[string]string{"Z": "last", "A": "first"},
		Labels: map[string]string{"task": "t-1", "project": "p"},
	}
	first := runArgs("fttest", spec, "/ws", "img:1")
	second := runArgs("fttest", spec, "/ws", "img:1")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("run args differ between calls:\n%v\n%v", first, second)
	}
	// A different name is the only thing that makes two definitions distinct.
	other := runArgs("other", spec, "/ws", "img:1")
	if reflect.DeepEqual(first, other) {
		t.Fatal("run args do not depend on the container name")
	}
}

// TestRunArgsMountOnlyWorkspace pins that the definition bind-mounts exactly
// the workspace, at the same path, and nothing else.
func TestRunArgsMountOnlyWorkspace(t *testing.T) {
	args := runArgs("fttest", isolation.Spec{}, "/ws", "img:1")
	var mounts []string
	for i, a := range args {
		if a == "--mount" && i+1 < len(args) {
			mounts = append(mounts, args[i+1])
		}
	}
	if len(mounts) != 1 || mounts[0] != "type=bind,source=/ws,target=/ws" {
		t.Fatalf("mounts = %v, want only the workspace bind", mounts)
	}
}

// TestExecArgsInjectKeysNotValues pins that env values never appear in argv:
// exec passes `--env KEY` and the value travels in the client environment.
func TestExecArgsInjectKeysNotValues(t *testing.T) {
	args := execArgs("fttest", "/ws", map[string]string{"B": "2", "A": "1"}, []string{"sh", "-c", "true"}, false)
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, "=1") || strings.Contains(joined, "=2") {
		t.Fatalf("exec args embed a value: %v", args)
	}
	if !contains(args, "--env", "A") || !contains(args, "--env", "B") {
		t.Fatalf("exec args %v do not inject the env keys", args)
	}
}

func contains(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		match := true
		for j, want := range seq {
			if args[i+j] != want {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestResolvePathRootsAtWorkdir pins that a relative path is rooted at the
// workspace and an absolute path is used as given.
func TestResolvePathRootsAtWorkdir(t *testing.T) {
	if got := resolvePath("/ws", "a.txt"); got != "/ws/a.txt" {
		t.Errorf("resolvePath(relative) = %q, want /ws/a.txt", got)
	}
	if got := resolvePath("/ws", "/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("resolvePath(absolute) = %q, want /etc/hosts", got)
	}
}

// TestMergeEnvLaterWins pins that later layers override earlier ones, so a
// per-command variable beats the spec's.
func TestMergeEnvLaterWins(t *testing.T) {
	got := mergeEnv(map[string]string{"A": "spec", "B": "spec"}, map[string]string{"A": "cmd"})
	if got["A"] != "cmd" || got["B"] != "spec" {
		t.Fatalf("mergeEnv = %v, want A=cmd B=spec", got)
	}
}
