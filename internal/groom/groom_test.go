package groom

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReportTemplateSectionsInOrder(t *testing.T) {
	got := sectionTitles(ReportTemplate())
	want := ReportSections()
	if !slices.Equal(got, want) {
		t.Fatalf("report template sections = %v, want %v", got, want)
	}
}

func TestDeferredQuestionsTemplateSectionsInOrder(t *testing.T) {
	got := sectionTitles(DeferredQuestionsTemplate())
	want := DeferredQuestionsSections()
	if !slices.Equal(got, want) {
		t.Fatalf("deferred-questions template sections = %v, want %v", got, want)
	}
}

// TestFeatureDocTemplatesSectionsInOrder pins the three feature documents to
// their fixed section contracts: a template is versioned data, so its section
// set and ordering must not drift from what Kickoff and the skill promise.
func TestFeatureDocTemplatesSectionsInOrder(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"spec", SpecTemplate(), SpecSections()},
		{"plan", PlanTemplate(), PlanSections()},
		{"tech design", TechDesignTemplate(), TechDesignSections()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sectionTitles(tt.body); !slices.Equal(got, tt.want) {
				t.Fatalf("%s template sections = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestFeatureDocSectionsAreNonEmpty pins that each feature document carries a
// real contract, so the session is never handed an empty template.
func TestFeatureDocSectionsAreNonEmpty(t *testing.T) {
	for name, sections := range map[string][]string{
		"spec":        SpecSections(),
		"plan":        PlanSections(),
		"tech design": TechDesignSections(),
	} {
		if len(sections) == 0 {
			t.Fatalf("%s has no sections", name)
		}
	}
}

// TestReportTemplateReferencesFeatureDocs pins that the report the session
// writes names the feature documents it sits alongside, so the report, the
// spec, and the plan travel together as one feature's document set.
func TestReportTemplateReferencesFeatureDocs(t *testing.T) {
	body := ReportTemplate()
	for _, want := range []string{SpecFileName, PlanFileName, TechDesignFileName} {
		if !strings.Contains(body, want) {
			t.Errorf("report template does not reference the feature doc %q:\n%s", want, body)
		}
	}
}

func TestTemplatesAreMarkdown(t *testing.T) {
	for name, body := range map[string]string{
		"report":             ReportTemplate(),
		"deferred questions": DeferredQuestionsTemplate(),
		"spec":               SpecTemplate(),
		"plan":               PlanTemplate(),
		"tech design":        TechDesignTemplate(),
	} {
		if strings.TrimSpace(body) == "" {
			t.Fatalf("%s template is empty", name)
		}
		if !strings.HasPrefix(body, "#") {
			t.Fatalf("%s template is not markdown:\n%s", name, body)
		}
	}
}

// TestSessionPromptIsDurableRepoData guards the committed grooming-session
// prompt: it must live at PromptPath in the repo, be markdown, and carry the
// protocol so a session launched from it behaves. The prompt is data, so this
// test reads the file from the tree rather than an embedded copy.
func TestSessionPromptIsDurableRepoData(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", PromptPath))
	if err != nil {
		t.Fatalf("read durable session prompt %q: %v", PromptPath, err)
	}
	body := string(data)
	if strings.TrimSpace(body) == "" {
		t.Fatal("session prompt is empty")
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "#") {
		t.Fatalf("session prompt is not markdown:\n%s", body)
	}
	lower := strings.ToLower(body)
	for _, want := range []string{
		"team lead", "product owner", "defer", "groomed", "assigned", "agent-ready",
	} {
		if !strings.Contains(lower, want) {
			t.Errorf("session prompt does not mention %q", want)
		}
	}
}

// TestSessionPromptIsProjectAgnostic pins that the durable session prompt names
// no concrete project. `ft groom` appends a kickoff that names the project, so
// the prompt itself is generic and a second project can commit it unchanged.
func TestSessionPromptIsProjectAgnostic(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", PromptPath))
	if err != nil {
		t.Fatalf("read durable session prompt %q: %v", PromptPath, err)
	}
	for _, forbidden := range []string{"Factotum project", "-p factotum", "--project factotum"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("session prompt hardcodes a project (%q); keep it project-agnostic", forbidden)
		}
	}
}

// TestSessionPromptReportTemplateMatchesVersioned pins the report template that
// prompt.md embeds to the versioned templates/report.md, so a session launched
// from the prompt cannot drift from the contract the report is checked against.
func TestSessionPromptReportTemplateMatchesVersioned(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", PromptPath))
	if err != nil {
		t.Fatalf("read durable session prompt %q: %v", PromptPath, err)
	}
	inline := fencedBlock(t, string(data), "# Grooming report")
	if got, want := strings.TrimSpace(inline), strings.TrimSpace(ReportTemplate()); got != want {
		t.Fatalf("prompt report template drifted from the versioned template:\n--- prompt ---\n%s\n--- versioned ---\n%s", got, want)
	}
}

// TestSessionPromptFeatureDocTemplatesMatchVersioned pins the feature document
// templates prompt.md embeds to the versioned templates/*.md, so a session
// launched from the durable prompt cannot drift from the contract a session's
// docs are checked against.
func TestSessionPromptFeatureDocTemplatesMatchVersioned(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", PromptPath))
	if err != nil {
		t.Fatalf("read durable session prompt %q: %v", PromptPath, err)
	}
	for _, doc := range []struct {
		firstLine string
		want      string
	}{
		{"# Feature spec", SpecTemplate()},
		{"# Feature plan", PlanTemplate()},
		{"# Feature tech design", TechDesignTemplate()},
	} {
		inline := fencedBlock(t, string(data), doc.firstLine)
		if got := strings.TrimSpace(inline); got != strings.TrimSpace(doc.want) {
			t.Fatalf("prompt %s template drifted from the versioned template:\n--- prompt ---\n%s\n--- versioned ---\n%s", doc.firstLine, got, doc.want)
		}
	}
}

// fencedBlock returns the first triple-backtick fenced block in body whose first
// non-empty line begins with firstLine.
func fencedBlock(t *testing.T, body, firstLine string) string {
	t.Helper()
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "```" {
			continue
		}
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j >= len(lines) || !strings.HasPrefix(lines[j], firstLine) {
			continue
		}
		for k := j; k < len(lines); k++ {
			if strings.TrimSpace(lines[k]) == "```" {
				return strings.Join(lines[j:k], "\n")
			}
		}
	}
	t.Fatalf("no fenced block starting with %q", firstLine)
	return ""
}

func TestSessionPathsAreDeterministicUnderDataDir(t *testing.T) {
	dir := SessionDir("/data", "groom-1")
	if want := filepath.Join("/data", "grooming-sessions", "groom-1"); dir != want {
		t.Fatalf("SessionDir = %q, want %q", dir, want)
	}
	if got, want := ReportPath("/data", "groom-1"), filepath.Join(dir, "report.md"); got != want {
		t.Fatalf("ReportPath = %q, want %q", got, want)
	}
	if got, want := DeferredQuestionsPath("/data", "groom-1"), filepath.Join(dir, "deferred-questions.md"); got != want {
		t.Fatalf("DeferredQuestionsPath = %q, want %q", got, want)
	}
	for name, got := range map[string]string{
		"spec":        SpecPath("/data", "groom-1"),
		"plan":        PlanPath("/data", "groom-1"),
		"tech design": TechDesignPath("/data", "groom-1"),
	} {
		want := filepath.Join(dir, map[string]string{
			"spec":        "spec.md",
			"plan":        "plan.md",
			"tech design": "tech-design.md",
		}[name])
		if got != want {
			t.Fatalf("%s path = %q, want %q", name, got, want)
		}
	}
}

// TestStagedOutputPathsAreWorkspaceRelative pins the fix for the capture bug:
// the kickoff must name outputs inside the agent's workspace, never an absolute
// path outside it (which a harness denies in a headless run). The staged paths
// are relative, deterministic per session, and named from the report and
// deferred-questions file names.
func TestStagedOutputPathsAreWorkspaceRelative(t *testing.T) {
	report := StagedReportPath("groom-1")
	deferred := StagedDeferredQuestionsPath("groom-1")
	if path.IsAbs(report) || path.IsAbs(deferred) {
		t.Fatalf("staged paths must be workspace-relative: report=%q deferred=%q", report, deferred)
	}
	if want := path.Join(StagingDirName, "groom-1", ReportFileName); report != want {
		t.Fatalf("StagedReportPath = %q, want %q", report, want)
	}
	if want := path.Join(StagingDirName, "groom-1", DeferredQuestionsFileName); deferred != want {
		t.Fatalf("StagedDeferredQuestionsPath = %q, want %q", deferred, want)
	}
	for name, got := range map[string]string{
		"spec":        StagedSpecPath("groom-1"),
		"plan":        StagedPlanPath("groom-1"),
		"tech design": StagedTechDesignPath("groom-1"),
	} {
		want := path.Join(StagingDirName, "groom-1", map[string]string{
			"spec":        SpecFileName,
			"plan":        PlanFileName,
			"tech design": TechDesignFileName,
		}[name])
		if path.IsAbs(got) {
			t.Fatalf("staged %s path must be workspace-relative: %q", name, got)
		}
		if got != want {
			t.Fatalf("staged %s path = %q, want %q", name, got, want)
		}
	}
}

// TestStagedPathsGroupsEveryOutput pins the bundled OutputPaths the CLI uses to
// capture a session: one struct naming all five workspace-relative documents, so
// report, deferred questions, and the feature documents cannot drift apart.
func TestStagedPathsGroupsEveryOutput(t *testing.T) {
	got := StagedPaths("groom-1")
	want := OutputPaths{
		Report:     StagedReportPath("groom-1"),
		Deferred:   StagedDeferredQuestionsPath("groom-1"),
		Spec:       StagedSpecPath("groom-1"),
		Plan:       StagedPlanPath("groom-1"),
		TechDesign: StagedTechDesignPath("groom-1"),
	}
	if got != want {
		t.Fatalf("StagedPaths = %+v, want %+v", got, want)
	}
}

// TestKickoffNamesScopeAndOutputs pins the acceptance contract: the kickoff the
// session prompt carries names every scoped item and the workspace-relative
// output paths, so the harness never has to guess where to write.
func TestKickoffNamesScopeAndOutputs(t *testing.T) {
	out := StagedPaths("groom-1")
	kick := Kickoff("factotum", []ScopeItem{
		{ID: "t-1", Kind: "task", Title: "Add widget"},
		{ID: "t-2", Kind: "idea", Title: "Maybe cache"},
	}, out, "groom-1", false)

	for _, want := range []string{
		"factotum", "t-1", "Add widget", "t-2", "Maybe cache",
		out.Report, out.Deferred, out.Spec, out.Plan, out.TechDesign,
	} {
		if !strings.Contains(kick, want) {
			t.Errorf("kickoff does not name %q:\n%s", want, kick)
		}
	}
	if !strings.Contains(kick, "relative to your working directory") {
		t.Errorf("kickoff does not state the outputs are relative to the working directory:\n%s", kick)
	}
}

// TestKickoffNamesFeatureDocOutputs pins the feature documents the session must
// emit alongside the task breakdown: the kickoff names each one's output path
// and its section contract, so a session cannot silently skip a document.
func TestKickoffNamesFeatureDocOutputs(t *testing.T) {
	out := StagedPaths("groom-1")
	kick := Kickoff("factotum", nil, out, "groom-1", false)
	for _, doc := range []struct {
		label string
		path  string
	}{
		{"Write the feature spec to: ", out.Spec},
		{"Write the feature plan to: ", out.Plan},
		{"Write the feature tech design to: ", out.TechDesign},
	} {
		if got := kickoffPath(kick, doc.label); got != doc.path {
			t.Errorf("kickoff %q = %q, want %q:\n%s", doc.label, got, doc.path, kick)
		}
	}
}

// TestKickoffStatesSectionContract pins the templates' section contract into the
// kickoff, in order per document, so the harness writes files whose headings
// match the versioned templates.
func TestKickoffStatesSectionContract(t *testing.T) {
	kick := Kickoff("p", nil, StagedPaths("groom-1"), "groom-1", false)
	for _, doc := range []struct {
		label    string
		sections []string
	}{
		{"Report sections, in order: ", ReportSections()},
		{"Deferred-questions sections, in order: ", DeferredQuestionsSections()},
		{"Feature spec sections, in order: ", SpecSections()},
		{"Feature plan sections, in order: ", PlanSections()},
		{"Feature tech design sections, in order: ", TechDesignSections()},
	} {
		line := kickoffLine(kick, doc.label)
		if line == "" {
			t.Fatalf("kickoff does not state %q:\n%s", doc.label, kick)
		}
		if want := strings.Join(doc.sections, ", "); line != want {
			t.Fatalf("kickoff line for %q = %q, want the ordered sections %q:\n%s", doc.label, line, want, kick)
		}
	}
}

// kickoffLine returns the kickoff line starting with label, trimmed.
func kickoffLine(body, label string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, label) {
			return strings.TrimSpace(strings.TrimPrefix(line, label))
		}
	}
	return ""
}

// kickoffPath returns the path named on the kickoff line starting with label.
func kickoffPath(body, label string) string {
	return kickoffLine(body, label)
}

// TestKickoffUnattendedMode pins the defer-and-complete contract: an unattended
// kickoff tells the session there is no product owner, to defer every product
// question instead of blocking, and to still finish agent-ready; an interactive
// kickoff must not carry that override.
func TestKickoffUnattendedMode(t *testing.T) {
	tests := []struct {
		name       string
		unattended bool
		want       []string
		notWant    []string
	}{
		{
			name:       "interactive omits the unsolicited override",
			unattended: false,
			want:       []string{"## Session kickoff", "Write the report to: "},
			notWant:    []string{"## Unattended mode", "No product owner", "timing guard"},
		},
		{
			name:       "unattended defers and completes",
			unattended: true,
			want: []string{
				"## Unattended mode",
				"No product owner",
				"never ask",
				"Defer every product question",
				"deferred-questions",
				"groomed",
				"assigned",
				"timing guard",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kick := Kickoff("p", nil, StagedPaths("groom-1"), "groom-1", tt.unattended)
			for _, want := range tt.want {
				if !strings.Contains(kick, want) {
					t.Errorf("kickoff missing %q:\n%s", want, kick)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(kick, notWant) {
					t.Errorf("kickoff unexpectedly contains %q:\n%s", notWant, kick)
				}
			}
		})
	}
}

// sectionTitles returns the level-2 section titles of a markdown document, in
// order.
func sectionTitles(body string) []string {
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(line, "## ") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "## ")))
		}
	}
	return out
}

// TestParseReviewVerdict pins the verdict contract: the three verdicts parse, an
// unknown one is rejected, and only needs-rework blocks the build.
func TestParseReviewVerdict(t *testing.T) {
	for _, v := range ReviewVerdicts() {
		got, err := ParseReviewVerdict(string(v))
		if err != nil || got != v {
			t.Fatalf("ParseReviewVerdict(%q) = %q, %v; want %q, nil", v, got, err, v)
		}
	}
	if _, err := ParseReviewVerdict("lgtm"); err == nil {
		t.Fatal("ParseReviewVerdict(lgtm) error = nil, want an unknown-verdict error")
	}
	if !VerdictNeedsRework.BlocksBuild() {
		t.Fatal("needs-rework must block the build")
	}
	for _, v := range []ReviewVerdict{VerdictApprove, VerdictApproveWithChanges} {
		if v.BlocksBuild() {
			t.Fatalf("%s must not block the build", v)
		}
	}
}

// TestReviewPath pins where a session's architecture review is captured.
func TestReviewPath(t *testing.T) {
	got := ReviewPath("/data", "groom-1")
	want := filepath.Join("/data", SessionsDirName, "groom-1", ReviewFileName)
	if got != want {
		t.Fatalf("ReviewPath = %q, want %q", got, want)
	}
}

// TestKickoffNamesArchitectureReview pins the review step wired into grooming:
// the kickoff names the reviewer role, the origin item(s), the recording command
// and its verdicts, and the build gate.
func TestKickoffNamesArchitectureReview(t *testing.T) {
	kick := Kickoff("factotum", []ScopeItem{
		{ID: "t-idea", Kind: "idea", Title: "Maybe cache"},
	}, StagedPaths("groom-9"), "groom-9", false)
	for _, want := range []string{
		"architecture-reviewer", "ft groom review groom-9",
		string(VerdictApprove), string(VerdictApproveWithChanges), string(VerdictNeedsRework),
		"t-idea", "blocks the feature from build",
	} {
		if !strings.Contains(kick, want) {
			t.Errorf("kickoff does not wire the architecture review (%q missing):\n%s", want, kick)
		}
	}
}
