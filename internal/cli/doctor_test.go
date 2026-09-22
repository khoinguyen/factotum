package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/doctor"
	"github.com/khoinguyen/factotum/pkg/vector"
)

// fakeDoctorProbe answers doctor's external checks from canned outcomes so the
// command is tested without a socket. Fields are mutable so a fake fix can "repair"
// the subsystem between the diagnose and the re-diagnose.
type fakeDoctorProbe struct {
	reachErr   error
	modelErr   error
	commandErr error
}

func (p *fakeDoctorProbe) Reachable(context.Context, doctor.Endpoint) error { return p.reachErr }
func (p *fakeDoctorProbe) HasModel(context.Context, doctor.Endpoint) error  { return p.modelErr }
func (p *fakeDoctorProbe) Resolvable(context.Context, string) error         { return p.commandErr }

func writeUserConfig(t *testing.T, r *runner, body string) {
	t.Helper()
	if err := os.WriteFile(r.userPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
}

// runDoctorWithInput executes a command with stdin so the interactive consent path
// can be exercised.
func (r *runner) runDoctorWithInput(input string, args ...string) (string, error) {
	r.t.Helper()
	var out bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &out, &out, r.getenv)
	base := r.setup(deps)
	root := NewRoot(deps)
	root.SetArgs(append(append([]string{}, base...), args...))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(input))
	err := root.Execute()
	_ = deps.FlushOutput()
	return out.String(), err
}

const healthyEmbedConfig = `[embed]
provider = "openai"
endpoint = "http://127.0.0.1:8080"
model    = "nomic-embed-text-v1.5"

[typesafe]
secret_api_key = "test-key"
`

func TestDoctorReportsHealthySubsystems(t *testing.T) {
	r := newRunner(t)
	r.doctorProbe = &fakeDoctorProbe{}
	writeUserConfig(t, r, healthyEmbedConfig)

	out := r.run("doctor")
	for _, want := range []string{"embed.provider: ok", "embed.endpoint: ok", "embed.model: ok", "judge.key: ok", "overall: ok"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorStructuredOutput(t *testing.T) {
	r := newRunner(t)
	r.doctorProbe = &fakeDoctorProbe{}
	writeUserConfig(t, r, healthyEmbedConfig)

	var report doctor.Report
	if err := json.Unmarshal([]byte(r.run("doctor", "-o", "json")), &report); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if report.Overall != doctor.StatusOK || len(report.Checks) == 0 {
		t.Fatalf("doctor report = %+v", report)
	}
}

func TestDoctorWarnsButExitsZeroWithoutProvider(t *testing.T) {
	r := newRunner(t)
	r.doctorProbe = &fakeDoctorProbe{}

	out := r.run("doctor")
	if !strings.Contains(out, "embed.provider: warn") || !strings.Contains(out, "judge.key: warn") {
		t.Fatalf("doctor output should warn about the unconfigured subsystems:\n%s", out)
	}
	if !strings.Contains(out, "overall: warn") {
		t.Fatalf("overall should be warn:\n%s", out)
	}
}

func TestDoctorStrictPromotesWarningsToFailure(t *testing.T) {
	r := newRunner(t)
	r.doctorProbe = &fakeDoctorProbe{}

	if err := r.runErr("doctor", "--strict"); !errors.Is(err, errDoctorFailed) {
		t.Fatalf("--strict should fail on warnings, got %v", err)
	}
}

func TestDoctorFailsOnUnknownProvider(t *testing.T) {
	r := newRunner(t)
	r.doctorProbe = &fakeDoctorProbe{}
	writeUserConfig(t, r, "[embed]\nprovider = \"bogus\"\nmodel = \"m\"\n")

	err := r.runErr("doctor")
	if !errors.Is(err, errDoctorFailed) {
		t.Fatalf("an unknown provider should fail, got %v", err)
	}
}

func TestDoctorFailsWhenHTTPProviderHasNoModel(t *testing.T) {
	r := newRunner(t)
	r.doctorProbe = &fakeDoctorProbe{}
	writeUserConfig(t, r, "[embed]\nprovider = \"openai\"\nendpoint = \"http://127.0.0.1:8080\"\n[typesafe]\nsecret_api_key = \"k\"\n")

	out, err := r.runDoctorWithInput("", "doctor")
	if !errors.Is(err, errDoctorFailed) {
		t.Fatalf("a missing model leaves vector recall off, so doctor should fail: %v", err)
	}
	if !strings.Contains(out, "embed.model: fail") || !strings.Contains(out, "[embed] model") {
		t.Fatalf("doctor output should flag the missing model:\n%s", out)
	}
}

func TestDoctorModelMissingOffersFixAndFixApplies(t *testing.T) {
	r := newRunner(t)
	probe := &fakeDoctorProbe{modelErr: errors.New("not found")}
	r.doctorProbe = probe
	writeUserConfig(t, r, "[embed]\nprovider = \"ollama\"\nendpoint = \"http://127.0.0.1:11434\"\nmodel = \"m\"\n[typesafe]\nsecret_api_key = \"k\"\n")

	var ran []string
	r.fixRunner = func(_ context.Context, argv []string, _ io.Writer) error {
		ran = append(ran, strings.Join(argv, " "))
		probe.modelErr = nil
		return nil
	}

	readOnly, _ := r.runDoctorWithInput("", "doctor")
	if !strings.Contains(readOnly, "embed.model: fail") || !strings.Contains(readOnly, "ollama pull m") {
		t.Fatalf("read-only doctor should report the model and its fix:\n%s", readOnly)
	}
	if len(ran) != 0 {
		t.Fatalf("doctor without --fix must not run a fix: %v", ran)
	}

	out := r.run("doctor", "--fix")
	if len(ran) != 1 || ran[0] != "ollama pull m" {
		t.Fatalf("--fix ran %v, want `ollama pull m`", ran)
	}
	if !strings.Contains(out, "embed.model: ok") || !strings.Contains(out, "overall: ok") {
		t.Fatalf("after the fix the embedder should be healthy:\n%s", out)
	}
}

func TestDoctorInteractiveConsent(t *testing.T) {
	r := newRunner(t)
	probe := &fakeDoctorProbe{modelErr: errors.New("not found")}
	r.doctorProbe = probe
	r.isTerminal = func(io.Writer) bool { return true }
	writeUserConfig(t, r, "[embed]\nprovider = \"ollama\"\nendpoint = \"http://127.0.0.1:11434\"\nmodel = \"m\"\n[typesafe]\nsecret_api_key = \"k\"\n")

	ran := false
	r.fixRunner = func(_ context.Context, _ []string, _ io.Writer) error {
		ran = true
		probe.modelErr = nil
		return nil
	}

	if _, err := r.runDoctorWithInput("n\n", "doctor"); err == nil {
		t.Fatal("declining the fix should leave the failure and exit non-zero")
	}
	if ran {
		t.Fatal("declining the prompt must not run the fix")
	}

	out, err := r.runDoctorWithInput("y\n", "doctor")
	if err != nil {
		t.Fatalf("accepting the fix should repair the subsystem: %v\n%s", err, out)
	}
	if !ran {
		t.Fatal("accepting the prompt should run the fix")
	}
}

func TestDoctorReindexFixRepairsModelMismatch(t *testing.T) {
	r := newRunner(t)
	r.doctorProbe = &fakeDoctorProbe{}
	r.embedder = &keywordEmbedder{}
	r.vectors = vector.NewMemory()
	configBody := "[embed]\nprovider = \"openai\"\nendpoint = \"http://127.0.0.1:8080\"\nmodel = \"first\"\n[typesafe]\nsecret_api_key = \"k\"\n"
	writeUserConfig(t, r, configBody)

	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "-p", projectID, "-t", "Terraform notes", "--brief", "infra versioning")

	// Change the model: the index still holds "first", so vector recall is broken.
	writeUserConfig(t, r, strings.Replace(configBody, `"first"`, `"second"`, 1))
	mismatch, _ := r.runDoctorWithInput("", "doctor", "-p", projectID)
	if !strings.Contains(mismatch, "embed.index: fail") {
		t.Fatalf("doctor should report the model mismatch:\n%s", mismatch)
	}

	fixed := r.run("doctor", "-p", projectID, "--fix")
	if !strings.Contains(fixed, "embed.index: ok") {
		t.Fatalf("reindex should repair the mismatch:\n%s", fixed)
	}
}
