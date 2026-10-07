// Package groom holds the versioned data that scaffolds a grooming session: the
// deterministic report template and the deferred-questions file format. The data
// lives in templates/*.md so changing a template is separate from changing code;
// the groom skill documents the protocol around it.
package groom

import _ "embed"

// PromptPath is the committed repo path of the grooming-session prompt. It is
// durable data (not code) so a session can be launched with
// `ft run --prompt-file <PromptPath>` from a stable location; the artifact copy
// it supersedes pointed at an ephemeral temp file.
const PromptPath = "docs/grooming/prompt.md"

//go:embed templates/report.md
var reportTemplate string

//go:embed templates/deferred-questions.md
var deferredQuestionsTemplate string

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

// ReportTemplate returns the deterministic grooming report template (markdown).
func ReportTemplate() string { return reportTemplate }

// DeferredQuestionsTemplate returns the deferred-questions file format (markdown).
func DeferredQuestionsTemplate() string { return deferredQuestionsTemplate }
