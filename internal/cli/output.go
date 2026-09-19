package cli

import (
	"fmt"
	"os"

	isatty "github.com/mattn/go-isatty"
)

// nextEntry is the lossless structured shape of `task next`, so a JSON
// consumer gets the same fields as the text table.
type nextEntry struct {
	TaskID  string   `json:"task_id" yaml:"task_id"`
	Score   float64  `json:"score" yaml:"score"`
	Title   string   `json:"title" yaml:"title"`
	Kind    string   `json:"kind" yaml:"kind"`
	Status  string   `json:"status" yaml:"status"`
	Project string   `json:"project" yaml:"project"`
	Repo    string   `json:"repo" yaml:"repo"`
	Labels  []string `json:"labels" yaml:"labels"`
}

// field is one `key: value` line of single-result text output.
type field struct {
	Key   string
	Value string
}

func f(key string, value any) field {
	return field{Key: key, Value: fmt.Sprintf("%v", value)}
}

// printFields writes yaml-like `key: value` lines. The project, when relevant,
// is always followed by the repo.
func (d *Deps) printFields(fields ...field) {
	for _, field := range fields {
		d.printf("%s: %s\n", field.Key, field.Value)
	}
}

// warnf writes an advisory warning to stderr. Warnings never touch stdout, so
// structured output stays parseable.
func (d *Deps) warnf(format string, args ...any) {
	_, _ = fmt.Fprintf(d.Err, "ft: warning: "+format+"\n", args...)
}

// repoValue renders a single repository reference, shortened and clickable.
func (d *Deps) repoValue(value string) string {
	return d.hyperlink(repoURL(value), shortRepo(value))
}

// repoCellValue renders a repository whose remote URL and local path are stored
// separately.
func (d *Deps) repoCellValue(remote, path string) string {
	return d.hyperlink(repoURL(remote), repoCell(remote, path))
}

// hyperlink wraps text in an OSC 8 escape so terminals render it clickable. It
// is a no-op when there is no URL or stdout is not a terminal, keeping piped
// output and tests byte-clean.
func (d *Deps) hyperlink(url, text string) string {
	if url == "" || !d.terminal() {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

func (d *Deps) terminal() bool {
	file, ok := d.Out.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(file.Fd()) || isatty.IsCygwinTerminal(file.Fd())
}
