package cli

import (
	"strings"
	"testing"
)

// TestHermeticGetenvDropsFactotumNamespace pins the helper the run/groom
// integration tests read the environment through: it passes PATH and provider
// keys through but drops the FACTOTUM_* namespace, so an ambient
// FACTOTUM_PROJECT/TASK_ID/STORE from the caller's shell cannot leak into the
// throwaway project and store the test set up.
func TestHermeticGetenvDropsFactotumNamespace(t *testing.T) {
	t.Setenv("FACTOTUM_PROJECT", "factotum")
	t.Setenv("FACTOTUM_TASK_ID", "t-leak")
	t.Setenv("FACTOTUM_STORE", "sqlite")
	t.Setenv("HERMETIC_PROBE", "kept")

	for _, key := range []string{"FACTOTUM_PROJECT", "FACTOTUM_TASK_ID", "FACTOTUM_STORE"} {
		if got := hermeticGetenv(key); got != "" {
			t.Errorf("hermeticGetenv(%s) = %q, want empty", key, got)
		}
	}
	if got := hermeticGetenv("HERMETIC_PROBE"); got != "kept" {
		t.Errorf("hermeticGetenv(HERMETIC_PROBE) = %q, want the ambient value", got)
	}
}

// TestHermeticGetenvIgnoresAmbientProject is the smoke's failure mode in
// process: with an ambient FACTOTUM_PROJECT set, a CLI flow wired to
// hermeticGetenv still resolves the test's configured project rather than the
// caller's.
func TestHermeticGetenvIgnoresAmbientProject(t *testing.T) {
	t.Setenv("FACTOTUM_PROJECT", "factotum")

	r := newRunner(t)
	r.getenv = hermeticGetenv
	projectID := firstField(t, r.run("project", "create", "Acme"))
	cfgPath := writeProjectConfig(t, projectID)

	taskID := firstField(t, r.run("--config", cfgPath, "task", "create", "-t", "one"))
	if listed := r.run("task", "list", "--project", projectID); !strings.Contains(listed, taskID) {
		t.Fatalf("task did not land in the configured project %s:\n%s", projectID, listed)
	}
}
