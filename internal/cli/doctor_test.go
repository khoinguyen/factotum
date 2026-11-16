package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// doctorObservation is what one doctor run did about a fix: whether it ran one
// and whether it asked first.
type doctorObservation struct {
	ran   bool
	asked bool
}

// observeDoctorFix runs doctor against a broken embedder under one caller shape
// (here: whether stdout is a terminal, and whether --fix was passed) and reports
// what it actually did, so a test never has to assume the interactive matrix.
func observeDoctorFix(t *testing.T, terminal, fix bool) doctorObservation {
	t.Helper()
	r := newRunner(t)
	probe := &fakeDoctorProbe{modelErr: fmt.Errorf("%w: not found", doctor.ErrModelNotServed)}
	r.doctorProbe = probe
	r.isTerminal = func(io.Writer) bool { return terminal }
	writeUserConfig(t, r, "[embed]\nprovider = \"ollama\"\nendpoint = \"http://127.0.0.1:11434\"\nmodel = \"m\"\n[typesafe]\nsecret_api_key = \"k\"\n")

	var observed doctorObservation
	r.fixRunner = func(_ context.Context, _ []string, _ io.Writer) error {
		observed.ran = true
		probe.modelErr = nil
		return nil
	}
	args := []string{"doctor"}
	if fix {
		args = append(args, "--fix")
	}
	out, _ := r.runDoctorWithInput("y\n", args...)
	observed.asked = strings.Contains(out, "apply fix? [y/N]")
	return observed
}

// fixFlowWords names, in the words the help must use, what an observed run did
// with a fix.
func fixFlowWords(o doctorObservation) string {
	switch {
	case o.asked:
		return "asks before"
	case o.ran:
		return "without prompting"
	default:
		return "read-only"
	}
}

// doctorScopes are the caller conditions the help must keep apart. A claim that
// holds for one of them must not be readable as holding for another.
var doctorScopes = []string{"--fix", "non-terminal", "terminal"}

// helpClauses splits help text into the individual statements it makes: on
// sentence ends, semicolons, commas, and " and ". A claim is then matched in the
// clause that governs it, because one sentence covering two callers would
// otherwise satisfy either caller's claim.
func helpClauses(text string) []string {
	flat := strings.Join(strings.Fields(text), " ")
	split := strings.NewReplacer(". ", "\x00", "; ", "\x00", ", ", "\x00", " and ", "\x00")
	clauses := strings.Split(split.Replace(flat), "\x00")
	for i, clause := range clauses {
		clauses[i] = strings.ToLower(clause)
	}
	return clauses
}

// isWordByte extends the word characters with "-", so a scope token cannot match
// inside a hyphenated compound: "terminal" must not match "non-terminal".
func isWordByte(b byte) bool {
	return b == '-' || b == '_' || ('a' <= b && b <= 'z') || ('0' <= b && b <= '9') || ('A' <= b && b <= 'Z')
}

// indexWord finds token in text where it stands as a whole word, or -1.
func indexWord(text, token string) int {
	for from := 0; from <= len(text)-len(token); {
		at := strings.Index(text[from:], token)
		if at < 0 {
			return -1
		}
		at += from
		end := at + len(token)
		if (at == 0 || !isWordByte(text[at-1])) && (end == len(text) || !isWordByte(text[end])) {
			return at
		}
		from = at + 1
	}
	return -1
}

// absenceWords say that a flow or flag is missing, which is the opposite of it
// happening.
var absenceWords = []string{"not", "never", "no", "without", "unless", "lacks", "cannot"}

// negated reports whether text denies what follows it.
func negated(text string) bool {
	if strings.Contains(text, "n't") {
		return true
	}
	for _, word := range absenceWords {
		if indexWord(text, word) >= 0 {
			return true
		}
	}
	return false
}

// statesFlow reports whether the help claims, in one clause governed by this
// caller condition alone, that the flow happens. A clause that denies the claim
// ("does not ask before") or names another caller ("a terminal and a
// non-terminal") does not count, so an unconditional or contradictory statement
// cannot stand in for a flow specific to this caller.
func statesFlow(clauses []string, claim, scope string) bool {
	for _, clause := range clauses {
		at := indexWord(clause, scope)
		if at < 0 || negated(clause[:at]) {
			continue
		}
		claimAt := strings.Index(clause, claim)
		if claimAt < 0 || negated(clause[:claimAt]) {
			continue
		}
		if sharesClauseWithOtherScope(clause, scope) {
			continue
		}
		return true
	}
	return false
}

// sharesClauseWithOtherScope reports whether the clause also names a different
// caller condition, which makes its claim ambiguous about who it describes.
func sharesClauseWithOtherScope(clause, scope string) bool {
	for _, other := range doctorScopes {
		if other != scope && indexWord(clause, other) >= 0 {
			return true
		}
	}
	return false
}

// TestDoctorHelpMatchesBehaviour ties the doctor Long text to the fix flows the
// command implements. Each caller shape is observed by running it, and the words
// the help must use are derived from what it did: a shape that starts or stops
// asking, applying, or staying read-only changes the required wording. The help
// therefore cannot describe a flow the command no longer has, or omit one it
// gained.
func TestDoctorHelpMatchesBehaviour(t *testing.T) {
	shapes := []struct {
		name     string
		terminal bool
		fix      bool
		// scope is the caller condition the help must name in the same clause as
		// the flow it qualifies.
		scope string
	}{
		{name: "non-terminal without --fix", scope: "non-terminal"},
		{name: "terminal without --fix", terminal: true, scope: "terminal"},
		{name: "--fix", fix: true, scope: "--fix"},
	}

	r := newRunner(t)
	var out bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &out, &out, r.getenv)
	r.setup(deps)
	long := newDoctorCommand(deps).Long

	help := strings.Join(strings.Fields(r.run("doctor", "--help")), " ")
	if !strings.Contains(help, strings.Join(strings.Fields(long), " ")) {
		t.Fatalf("--help must print doctor's Long text:\n%s", help)
	}

	clauses := helpClauses(long)
	for _, shape := range shapes {
		observed := observeDoctorFix(t, shape.terminal, shape.fix)
		claim := fixFlowWords(observed)
		if !statesFlow(clauses, claim, shape.scope) {
			t.Errorf("%s behaves as %q (ran=%v, asked=%v), so --help must say so, scoped to %s:\n%s",
				shape.name, claim, observed.ran, observed.asked, shape.scope, long)
		}
	}
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
	probe := &fakeDoctorProbe{modelErr: fmt.Errorf("%w: not found", doctor.ErrModelNotServed)}
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
	probe := &fakeDoctorProbe{modelErr: fmt.Errorf("%w: not found", doctor.ErrModelNotServed)}
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
