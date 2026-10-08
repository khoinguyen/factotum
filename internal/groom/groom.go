// Package groom holds the versioned data that scaffolds a grooming session: the
// report template, the deferred-questions file format, and the feature spec,
// plan, and tech-design templates. The data lives in templates/*.md so changing
// a template is separate from changing code; the groom skill documents the
// protocol around it.
package groom

import (
	_ "embed"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

const (
	// SessionsDirName is the directory, under the project data dir, that holds
	// every grooming session's outputs.
	SessionsDirName = "grooming-sessions"
	// StagingDirName is the workspace-relative directory a session writes its
	// outputs into. It lives inside the run workspace (never the project data
	// dir), so a sandboxed harness is allowed to write it; `ft groom` copies the
	// files out to SessionsDirName when the session ends.
	StagingDirName = ".ft-groom"
	// ReportFileName is the deterministic name of a session's report.
	ReportFileName = "report.md"
	// DeferredQuestionsFileName is the deterministic name of a session's
	// deferred-questions file.
	DeferredQuestionsFileName = "deferred-questions.md"
	// SpecFileName is the deterministic name of a session's feature spec.
	SpecFileName = "spec.md"
	// PlanFileName is the deterministic name of a session's feature plan.
	PlanFileName = "plan.md"
	// TechDesignFileName is the deterministic name of a session's feature tech
	// design.
	TechDesignFileName = "tech-design.md"
)

// PromptPath is the committed repo path of the grooming-session prompt. It is
// durable data (not code) so a session can be launched with
// `ft run --prompt-file <PromptPath>` from a stable location; the artifact copy
// it supersedes pointed at an ephemeral temp file.
const PromptPath = "docs/grooming/prompt.md"

//go:embed templates/report.md
var reportTemplate string

//go:embed templates/deferred-questions.md
var deferredQuestionsTemplate string

//go:embed templates/spec.md
var specTemplate string

//go:embed templates/plan.md
var planTemplate string

//go:embed templates/tech-design.md
var techDesignTemplate string

// ReportSections is the fixed set of top-level report sections, in the order the
// session must render them. It is the contract the report template is checked
// against.
func ReportSections() []string {
	return []string{
		"Summary",
		"Per item",
		"Product questions (grill)",
		"Deferred (for stakeholders)",
		"DAG changes",
	}
}

// DeferredQuestionsSections is the fixed set of sections of the
// deferred-questions file, in order.
func DeferredQuestionsSections() []string {
	return []string{"Questions", "Resolved"}
}

// SpecSections is the fixed set of sections of the feature spec, in order.
func SpecSections() []string {
	return []string{"Summary", "Problem", "Goals", "Non-goals", "Requirements", "Acceptance"}
}

// PlanSections is the fixed set of sections of the feature plan, in order.
func PlanSections() []string {
	return []string{"Summary", "Milestones", "Tasks", "Dependencies", "Verification", "Rollout"}
}

// TechDesignSections is the fixed set of sections of the feature tech design, in
// order.
func TechDesignSections() []string {
	return []string{"Summary", "Context", "Design", "Interfaces", "Data", "Cross-cutting impact", "Risks"}
}

// ReportTemplate returns the deterministic grooming report template (markdown).
func ReportTemplate() string { return reportTemplate }

// DeferredQuestionsTemplate returns the deferred-questions file format (markdown).
func DeferredQuestionsTemplate() string { return deferredQuestionsTemplate }

// SpecTemplate returns the deterministic feature spec template (markdown).
func SpecTemplate() string { return specTemplate }

// PlanTemplate returns the deterministic feature plan template (markdown).
func PlanTemplate() string { return planTemplate }

// TechDesignTemplate returns the deterministic feature tech-design template
// (markdown).
func TechDesignTemplate() string { return techDesignTemplate }

// SessionDir returns the directory a session's outputs live in: a deterministically
// named subdirectory of the project data dir. The session id keeps concurrent or
// repeated sessions from overwriting each other's report.
func SessionDir(dataDir, sessionID string) string {
	return filepath.Join(dataDir, SessionsDirName, sessionID)
}

// ReportPath returns the absolute durable path of a session's captured report:
// where `ft groom` copies the report the session staged inside its workspace.
func ReportPath(dataDir, sessionID string) string {
	return filepath.Join(SessionDir(dataDir, sessionID), ReportFileName)
}

// DeferredQuestionsPath returns the absolute durable path of a session's
// captured deferred-questions file.
func DeferredQuestionsPath(dataDir, sessionID string) string {
	return filepath.Join(SessionDir(dataDir, sessionID), DeferredQuestionsFileName)
}

// SpecPath returns the absolute durable path of a session's captured feature
// spec.
func SpecPath(dataDir, sessionID string) string {
	return filepath.Join(SessionDir(dataDir, sessionID), SpecFileName)
}

// PlanPath returns the absolute durable path of a session's captured feature
// plan.
func PlanPath(dataDir, sessionID string) string {
	return filepath.Join(SessionDir(dataDir, sessionID), PlanFileName)
}

// TechDesignPath returns the absolute durable path of a session's captured
// feature tech design.
func TechDesignPath(dataDir, sessionID string) string {
	return filepath.Join(SessionDir(dataDir, sessionID), TechDesignFileName)
}

// OutputPaths names the workspace-relative path of each deterministic session
// document. It is the bundle Kickoff names and the CLI captures, so the set of
// outputs lives in one place.
type OutputPaths struct {
	Report     string
	Deferred   string
	Spec       string
	Plan       string
	TechDesign string
}

// StagedPaths returns the workspace-relative paths a session writes its five
// documents to, under the session's staging directory.
func StagedPaths(sessionID string) OutputPaths {
	return OutputPaths{
		Report:     StagedReportPath(sessionID),
		Deferred:   StagedDeferredQuestionsPath(sessionID),
		Spec:       StagedSpecPath(sessionID),
		Plan:       StagedPlanPath(sessionID),
		TechDesign: StagedTechDesignPath(sessionID),
	}
}

// StagedReportPath returns the workspace-relative path a session writes its
// report to. It is relative so the harness writes inside its own workspace,
// which a sandboxed backend allows; `ft groom` reads it back and copies it to
// ReportPath.
func StagedReportPath(sessionID string) string {
	return path.Join(StagingDirName, sessionID, ReportFileName)
}

// StagedDeferredQuestionsPath returns the workspace-relative path a session
// writes its deferred-questions file to.
func StagedDeferredQuestionsPath(sessionID string) string {
	return path.Join(StagingDirName, sessionID, DeferredQuestionsFileName)
}

// StagedSpecPath returns the workspace-relative path a session writes its
// feature spec to.
func StagedSpecPath(sessionID string) string {
	return path.Join(StagingDirName, sessionID, SpecFileName)
}

// StagedPlanPath returns the workspace-relative path a session writes its
// feature plan to.
func StagedPlanPath(sessionID string) string {
	return path.Join(StagingDirName, sessionID, PlanFileName)
}

// StagedTechDesignPath returns the workspace-relative path a session writes its
// feature tech design to.
func StagedTechDesignPath(sessionID string) string {
	return path.Join(StagingDirName, sessionID, TechDesignFileName)
}

// ScopeItem is one item a grooming session covers, as named in the kickoff.
type ScopeItem struct {
	ID    string `json:"id" yaml:"id"`
	Kind  string `json:"kind" yaml:"kind"`
	Title string `json:"title" yaml:"title"`
}

// unattendedOverride is appended to the kickoff when no product owner is
// present. It suspends the pre-answer timing guard, so the session defers every
// product question instead of blocking, and still finishes agent-ready.
const unattendedOverride = `## Unattended mode

No product owner is present and there is no interactive channel: never ask a question or wait for an
answer. Defer every product question to the deferred-questions file above instead of blocking the
run, and record every engineering decision as a note on the item.

The pre-answer timing guard is suspended: promote, split, and mark every produced task groomed and
assigned to an agent so the session still completes agent-ready. Any item you cannot complete is a
human item - name it in the report and in the deferred-questions file. A run that leaves an item
neither agent-ready nor deferred is incomplete.
`

// Kickoff returns the block `ft groom` appends to the durable session prompt. It
// names the project, every scoped item, and the workspace-relative paths the
// session must write, and restates the templates' section contract, so the
// harness has the scope and the deterministic outputs without re-reading the
// code. The paths are relative to the session's working directory, so a
// sandboxed harness writes inside its own workspace and `ft groom` captures the
// files from there. When unattended is set it also carries the no-product-owner
// override.
func Kickoff(project string, items []ScopeItem, out OutputPaths, unattended bool) string {
	var b strings.Builder
	b.WriteString("## Session kickoff\n\n")
	fmt.Fprintf(&b, "Project: %s\n\n", project)
	if len(items) == 0 {
		b.WriteString("Scope: (no items)\n\n")
	} else {
		fmt.Fprintf(&b, "Scope (%d items):\n", len(items))
		for _, item := range items {
			fmt.Fprintf(&b, "- %s (%s) %s\n", item.ID, item.Kind, item.Title)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Write the report to: %s\n", out.Report)
	fmt.Fprintf(&b, "Write the deferred questions to: %s\n", out.Deferred)
	fmt.Fprintf(&b, "Write the feature spec to: %s\n", out.Spec)
	fmt.Fprintf(&b, "Write the feature plan to: %s\n", out.Plan)
	fmt.Fprintf(&b, "Write the feature tech design to: %s\n", out.TechDesign)
	b.WriteString("All paths are relative to your working directory; create their parent directory if it is absent.\n\n")
	fmt.Fprintf(&b, "Report sections, in order: %s\n", strings.Join(ReportSections(), ", "))
	fmt.Fprintf(&b, "Deferred-questions sections, in order: %s\n", strings.Join(DeferredQuestionsSections(), ", "))
	fmt.Fprintf(&b, "Feature spec sections, in order: %s\n", strings.Join(SpecSections(), ", "))
	fmt.Fprintf(&b, "Feature plan sections, in order: %s\n", strings.Join(PlanSections(), ", "))
	fmt.Fprintf(&b, "Feature tech design sections, in order: %s\n", strings.Join(TechDesignSections(), ", "))
	if unattended {
		b.WriteString("\n")
		b.WriteString(unattendedOverride)
	}
	return b.String()
}
