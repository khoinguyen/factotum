// Package feedback collects agent-reported bugs and friction and delivers them to
// a sink. The report is a plain value; a Transport decides where it lands. The
// near-term transport writes a task into the Factotum database, and the interface
// leaves room for a GitHub issue or an outbox transport without changing callers.
package feedback

import (
	"fmt"
	"regexp"
	"strings"
)

// LabelExternalFeedback marks a stored report so maintainers can find every
// incoming report with one label query.
const LabelExternalFeedback = "external-feedback"

const titleLimit = 80

// Report is one piece of feedback plus the context that makes it actionable.
type Report struct {
	// Message is what the reporter wrote.
	Message string
	// Flow names the command or flow involved (for example "ft task next").
	Flow string
	// Version is the ft version that produced the report.
	Version string
	// Project is the originating project, when the caller had one.
	Project string
	// Repo is the originating repository within that project, when known.
	Repo string
}

// Title is the task title: the first line of the message, whitespace-collapsed
// and clamped so one long line cannot become the whole title.
func (r Report) Title() string {
	line := r.Message
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = line[:index]
	}
	line = strings.Join(strings.Fields(line), " ")
	runes := []rune(line)
	if len(runes) > titleLimit {
		return string(runes[:titleLimit]) + "..."
	}
	return line
}

// Body is the stored description: the message first, then the context lines that
// make the report reproducible. Empty optional fields are omitted.
func (r Report) Body() string {
	var b strings.Builder
	b.WriteString(r.Message)
	b.WriteString("\n\n--- feedback context ---\n")
	if r.Flow != "" {
		fmt.Fprintf(&b, "flow: %s\n", r.Flow)
	}
	fmt.Fprintf(&b, "ft version: %s\n", r.Version)
	if r.Project != "" {
		fmt.Fprintf(&b, "project: %s\n", r.Project)
	}
	if r.Repo != "" {
		fmt.Fprintf(&b, "repo: %s\n", r.Repo)
	}
	return b.String()
}

// Sanitize redacts the home directory and token-shaped strings from every
// free-text field, so a report can be stored without leaking local paths or
// secrets. home is the caller's $HOME; an empty home skips path redaction.
func (r Report) Sanitize(home string) Report {
	r.Message = redact(r.Message, home)
	r.Flow = redact(r.Flow, home)
	r.Project = redact(r.Project, home)
	r.Repo = redact(r.Repo, home)
	return r
}

// tokenPattern matches secrets whose shape is distinctive enough to redact: a
// known provider prefix followed by an opaque run, or any long run of
// identifier characters (which covers hex digests and base64 blobs).
var tokenPattern = regexp.MustCompile(
	`(?:sk|ghp|gho|ghs|ghr|github_pat|apikey|xox[baprs])[-_][A-Za-z0-9_-]{8,}` +
		`|[A-Za-z0-9_-]{32,}`)

func redact(value, home string) string {
	if home != "" {
		homePattern := regexp.MustCompile(regexp.QuoteMeta(home) + `([/\\]|$)`)
		value = homePattern.ReplaceAllString(value, "$$HOME$1")
	}
	return tokenPattern.ReplaceAllString(value, "[redacted]")
}
