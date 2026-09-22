package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Output bounding thresholds: stdout over 32 KiB or 400 lines, whichever comes
// first, is spilled and replaced by a head+tail window.
const (
	maxOutputBytes  = 32 * 1024
	maxOutputLines  = 400
	outputHeadLines = 60
	outputTailLines = 20
)

// outputLimit is the point at which stdout is truncated.
type outputLimit struct {
	Bytes int
	Lines int
}

// outputPolicy decides whether stdout is bounded and at what limit. Precedence:
// --full wins, then FACTOTUM_MAX_OUTPUT, then the interactive default (full),
// then the built-in threshold. Interactive means any of stdout/stderr is a TTY.
// FACTOTUM_MAX_OUTPUT is a byte budget only, so an explicit limit is not
// second-guessed by the built-in line threshold.
func outputPolicy(full bool, env string, interactive bool) (outputLimit, bool, error) {
	if full {
		return outputLimit{}, false, nil
	}
	if env = strings.TrimSpace(env); env != "" {
		switch strings.ToLower(env) {
		case "unlimited", "off", "none", "false":
			return outputLimit{}, false, nil
		}
		n, err := strconv.Atoi(env)
		if err != nil {
			return outputLimit{}, false, fmt.Errorf("invalid FACTOTUM_MAX_OUTPUT %q, want a byte count or \"unlimited\"", env)
		}
		if n < 0 {
			return outputLimit{}, false, fmt.Errorf("invalid FACTOTUM_MAX_OUTPUT %q, want a non-negative byte count", env)
		}
		if n == 0 {
			return outputLimit{}, false, nil
		}
		return outputLimit{Bytes: n}, true, nil
	}
	if interactive {
		return outputLimit{}, false, nil
	}
	return outputLimit{Bytes: maxOutputBytes, Lines: maxOutputLines}, true, nil
}

// boundedOutput buffers stdout until the command finishes, then either writes it
// through unchanged or spills the full text to a temp file and prints a
// head+tail window. The spill file is left for the OS temp cleaner: the printed
// path and the structured envelope's full_output_path stay readable. No repo- or
// home-relative state is created.
type boundedOutput struct {
	real    io.Writer
	format  string
	limit   outputLimit
	buf     bytes.Buffer
	flushed bool
}

func newBoundedOutput(real io.Writer, format string, limit outputLimit) *boundedOutput {
	return &boundedOutput{real: real, format: format, limit: limit}
}

func (b *boundedOutput) Write(p []byte) (int, error) { return b.buf.Write(p) }

// Flush emits the buffered output. It is idempotent.
func (b *boundedOutput) Flush() error {
	if b.flushed {
		return nil
	}
	b.flushed = true
	data := b.buf.Bytes()
	if len(data) == 0 || !exceedsLimit(data, b.limit) {
		_, err := b.real.Write(data)
		return err
	}

	path, _ := spillOutput(data)
	window := truncateWindow(data, b.limit, path)

	if b.format == "json" || b.format == "yaml" {
		return b.writeEnvelope(window, path)
	}
	_, err := io.WriteString(b.real, window)
	return err
}

// outputEnvelope is the structured shape emitted when `-o json|yaml` output is
// truncated: a valid document that names the spill file and carries the window.
type outputEnvelope struct {
	Truncated      bool   `json:"truncated" yaml:"truncated"`
	FullOutputPath string `json:"full_output_path" yaml:"full_output_path"`
	Preview        string `json:"preview" yaml:"preview"`
}

func (b *boundedOutput) writeEnvelope(window, path string) error {
	env := outputEnvelope{Truncated: true, FullOutputPath: path, Preview: window}
	if b.format == "json" {
		encoder := json.NewEncoder(b.real)
		encoder.SetIndent("", "  ")
		return encoder.Encode(env)
	}
	encoder := yaml.NewEncoder(b.real)
	encoder.SetIndent(2)
	defer func() { _ = encoder.Close() }()
	return encoder.Encode(env)
}

// spillOutput writes the full text to a per-invocation temp file and returns its
// path. The file is left in place for the OS temp cleaner so the caller can read
// it after the process exits.
func spillOutput(data []byte) (string, error) {
	file, err := os.CreateTemp("", "ft-output-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func exceedsLimit(data []byte, limit outputLimit) bool {
	if limit.Bytes > 0 && len(data) > limit.Bytes {
		return true
	}
	return limit.Lines > 0 && countLines(data) > limit.Lines
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	lines := bytes.Count(data, []byte("\n"))
	if data[len(data)-1] != '\n' {
		lines++
	}
	return lines
}

// truncateWindow returns the full text when it fits the limit, otherwise the
// first 60 and last 20 lines joined by a marker that names the spill file. The
// window is byte-capped so a single huge line cannot defeat the bound.
func truncateWindow(data []byte, limit outputLimit, path string) string {
	if !exceedsLimit(data, limit) {
		return string(data)
	}
	head, tail := headTailWindow(string(data), limit)
	omittedBytes := len(data) - len(head) - len(tail)
	if omittedBytes < 0 {
		omittedBytes = 0
	}
	omittedLines := countLines(data) - countLines([]byte(head)) - countLines([]byte(tail))
	if omittedLines < 0 {
		omittedLines = 0
	}
	marker := fmt.Sprintf("\n[ft: output truncated; %d bytes / %d lines omitted", omittedBytes, omittedLines)
	if path != "" {
		marker += "; full output: " + path
	}
	marker += "]\n"
	return head + marker + tail
}

func headTailWindow(text string, limit outputLimit) (string, string) {
	lines := strings.SplitAfter(text, "\n")
	head := strings.Join(lines[:min(outputHeadLines, len(lines))], "")
	tail := strings.Join(lines[max(0, len(lines)-outputTailLines):], "")
	if limit.Bytes > 0 {
		head = capBytes(head, limit.Bytes*3/4, true)
		tail = capBytes(tail, limit.Bytes/4, false)
	}
	return head, tail
}

// capBytes trims text to at most budget bytes, from the start when head is true
// and from the end otherwise, keeping the result valid UTF-8.
func capBytes(text string, budget int, head bool) string {
	if budget < 0 {
		budget = 0
	}
	if len(text) <= budget {
		return text
	}
	if head {
		return strings.ToValidUTF8(text[:budget], "")
	}
	return strings.ToValidUTF8(text[len(text)-budget:], "")
}
