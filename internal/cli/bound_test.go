package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// bigBody builds a deterministic body with the given number of lines, each
// padded to width bytes so byte and line thresholds can be exercised separately.
func bigBody(lines, width int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "line %04d %s\n", i, strings.Repeat("x", width))
	}
	return b.String()
}

func createTaskWithBody(t *testing.T, r *runner, projectID, body string) string {
	t.Helper()
	bodyFile := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(bodyFile, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return firstField(t, r.run("task", "create", "--project", projectID, "--title", "big", "--body-file", bodyFile))
}

// spillPath extracts the full-output path the truncation marker prints.
func spillPath(t *testing.T, out string) string {
	t.Helper()
	_, rest, ok := strings.Cut(out, "full output: ")
	if !ok {
		t.Fatalf("no full-output path in output:\n%s", out)
	}
	path, _, ok := strings.Cut(rest, "]")
	if !ok {
		t.Fatalf("malformed truncation marker:\n%s", out)
	}
	return strings.TrimSpace(path)
}

func TestOutputPolicy(t *testing.T) {
	tests := []struct {
		name        string
		full        bool
		env         string
		interactive bool
		wantBound   bool
		wantBytes   int
		wantLines   int
		wantErr     bool
	}{
		{"default non-interactive", false, "", false, true, maxOutputBytes, maxOutputLines, false},
		{"interactive defaults full", false, "", true, false, 0, 0, false},
		{"full flag wins", true, "", false, false, 0, 0, false},
		{"env is a byte budget, no line cap", false, "1024", true, true, 1024, 0, false},
		{"env unlimited", false, "unlimited", false, false, 0, 0, false},
		{"env zero disables", false, "0", false, false, 0, 0, false},
		{"env off disables", false, "off", false, false, 0, 0, false},
		{"full flag beats env", true, "1024", false, false, 0, 0, false},
		{"invalid env", false, "nope", false, false, 0, 0, true},
		{"negative env", false, "-5", false, false, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, bound, err := outputPolicy(tt.full, tt.env, tt.interactive)
			if (err != nil) != tt.wantErr {
				t.Fatalf("outputPolicy() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if bound != tt.wantBound {
				t.Fatalf("bound = %v, want %v", bound, tt.wantBound)
			}
			if bound && (limit.Bytes != tt.wantBytes || limit.Lines != tt.wantLines) {
				t.Fatalf("limit = %+v, want bytes=%d lines=%d", limit, tt.wantBytes, tt.wantLines)
			}
		})
	}
}

func TestTruncateWindowKeepsHeadAndTail(t *testing.T) {
	data := []byte(bigBody(200, 10))
	limit := outputLimit{Bytes: 500, Lines: 50}
	got := truncateWindow(data, limit, "/tmp/ft-out.txt")
	if !strings.Contains(got, "line 0000") {
		t.Fatalf("window is missing the head:\n%s", got)
	}
	if !strings.Contains(got, "line 0199") {
		t.Fatalf("window is missing the tail:\n%s", got)
	}
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "/tmp/ft-out.txt") {
		t.Fatalf("window is missing the truncation marker:\n%s", got)
	}
	if len(got) > limit.Bytes+512 {
		t.Fatalf("window = %d bytes, want bounded near %d", len(got), limit.Bytes)
	}
}

func TestTruncateWindowPassesSmallOutputThrough(t *testing.T) {
	data := []byte("short\nbody\n")
	got := truncateWindow(data, outputLimit{Bytes: maxOutputBytes, Lines: maxOutputLines}, "/tmp/ft-out.txt")
	if got != string(data) {
		t.Fatalf("small output changed:\ngot  %q\nwant %q", got, string(data))
	}
}

func TestExceedsLimitBoundary(t *testing.T) {
	limit := outputLimit{Bytes: 32, Lines: 4}
	if exceedsLimit([]byte(strings.Repeat("a", 32)), limit) {
		t.Fatal("output exactly at the byte limit must not be truncated")
	}
	if !exceedsLimit([]byte(strings.Repeat("a", 33)), limit) {
		t.Fatal("output one byte over the limit must be truncated")
	}
	if exceedsLimit([]byte("a\nb\nc\nd"), limit) {
		t.Fatal("output exactly at the line limit must not be truncated")
	}
	if !exceedsLimit([]byte("a\nb\nc\nd\ne"), limit) {
		t.Fatal("output one line over the limit must be truncated")
	}
}

func TestCapBytesKeepsValidUTF8(t *testing.T) {
	text := strings.Repeat("é", 10)
	if got := capBytes(text, 5, true); !utf8.ValidString(got) {
		t.Fatalf("head cap produced invalid UTF-8: %q", got)
	}
	if got := capBytes(text, 5, false); !utf8.ValidString(got) {
		t.Fatalf("tail cap produced invalid UTF-8: %q", got)
	}
}

func TestTruncateWindowWithoutPath(t *testing.T) {
	data := []byte(bigBody(200, 10))
	got := truncateWindow(data, outputLimit{Bytes: 500, Lines: 50}, "")
	if !strings.Contains(got, "truncated") {
		t.Fatalf("window is missing the truncation marker:\n%s", got)
	}
	if strings.Contains(got, "full output:") {
		t.Fatalf("no path should be printed when the spill failed:\n%s", got)
	}
}

func TestOutputUnderThresholdIsByteIdentical(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, "small body\nwith two lines\n")

	bounded := r.run("task", "get", taskID)
	full := r.run("task", "get", taskID, "--full")
	if bounded != full {
		t.Fatalf("under-threshold output changed:\nbounded:\n%q\nfull:\n%q", bounded, full)
	}
	if strings.Contains(bounded, "truncated") {
		t.Fatalf("under-threshold output should not be marked truncated:\n%s", bounded)
	}
}

func TestOutputOverByteThresholdTruncatesAndSpills(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, bigBody(500, 100))

	full := r.run("task", "get", taskID, "--full")
	out := r.run("task", "get", taskID)
	if !strings.Contains(out, "truncated") {
		t.Fatalf("large output was not truncated:\n%s", out)
	}
	if len(out) >= len(full) {
		t.Fatalf("bounded output (%d) not smaller than full (%d)", len(out), len(full))
	}
	if len(out) > maxOutputBytes+512 {
		t.Fatalf("bounded output = %d bytes, want <= %d", len(out), maxOutputBytes+512)
	}
	if !strings.Contains(out, "line 0000") || !strings.Contains(out, "line 0499") {
		t.Fatalf("bounded output is missing head or tail:\n%s", out)
	}
	path := spillPath(t, out)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("spill file %s should be gone after a normal exit, stat err = %v", path, err)
	}
}

func TestOutputOverLineThresholdTruncates(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, bigBody(500, 5))

	out := r.run("task", "get", taskID)
	if !strings.Contains(out, "truncated") {
		t.Fatalf("500-line output was not truncated:\n%s", out)
	}
	if !strings.Contains(out, "line 0000") || !strings.Contains(out, "line 0499") {
		t.Fatalf("bounded output is missing head or tail:\n%s", out)
	}
}

func TestOutputFullFlagWins(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, bigBody(500, 100))

	out := r.run("task", "get", taskID, "--full")
	if strings.Contains(out, "truncated") {
		t.Fatalf("--full output should not be truncated:\n%s", out)
	}
	if !strings.Contains(out, "line 0499") {
		t.Fatalf("--full output is missing the tail:\n%s", out)
	}
}

func TestOutputMaxOutputEnvTruncates(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_MAX_OUTPUT" {
			return "120"
		}
		return ""
	}
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, bigBody(50, 20))

	out := r.run("task", "get", taskID)
	if !strings.Contains(out, "truncated") {
		t.Fatalf("FACTOTUM_MAX_OUTPUT=120 did not truncate:\n%s", out)
	}
}

func TestOutputMaxOutputEnvUnlimitedWins(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_MAX_OUTPUT" {
			return "unlimited"
		}
		return ""
	}
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, bigBody(500, 100))

	out := r.run("task", "get", taskID)
	if strings.Contains(out, "truncated") {
		t.Fatalf("FACTOTUM_MAX_OUTPUT=unlimited should not truncate:\n%s", out)
	}
}

func TestOutputTTYDefaultsToFull(t *testing.T) {
	r := newRunner(t)
	r.isTerminal = func(io.Writer) bool { return true }
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, bigBody(500, 100))

	out := r.run("task", "get", taskID)
	if strings.Contains(out, "truncated") {
		t.Fatalf("interactive output should default to full:\n%s", out)
	}
}

func TestStructuredOutputTruncationEnvelope(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, bigBody(500, 100))

	out := r.run("task", "get", taskID, "-o", "json")
	var env struct {
		Truncated      bool   `json:"truncated"`
		FullOutputPath string `json:"full_output_path"`
		Preview        string `json:"preview"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("truncated json output is not valid JSON: %v\n%s", err, out)
	}
	if !env.Truncated {
		t.Fatalf("json envelope did not report truncation:\n%s", out)
	}
	if env.FullOutputPath == "" {
		t.Fatalf("json envelope did not carry the full-output path:\n%s", out)
	}
	if env.Preview == "" {
		t.Fatalf("json envelope preview is empty:\n%s", out)
	}
	if len(out) > maxOutputBytes+1024 {
		t.Fatalf("json envelope = %d bytes, want bounded", len(out))
	}
}

func TestSpillOutputWritesFullText(t *testing.T) {
	data := []byte(bigBody(10, 10))
	path, err := spillOutput(data)
	if err != nil {
		t.Fatalf("spillOutput() error = %v", err)
	}
	defer func() { _ = os.Remove(path) }()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if string(got) != string(data) {
		t.Fatalf("spill file does not hold the full text")
	}
}

func TestStructuredFullFlagStaysOriginal(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, bigBody(500, 100))

	out := r.run("task", "get", taskID, "-o", "json", "--full")
	var value map[string]any
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("json output is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := value["truncated"]; ok {
		t.Fatalf("--full json should not be an envelope:\n%s", out)
	}
	if value["id"] != taskID {
		t.Fatalf("--full json lost its shape: %v", value)
	}
}

func TestStructuredOutputUnderThresholdUnchanged(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, "small body\n")

	out := r.run("task", "get", taskID, "-o", "json")
	var value map[string]any
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("json output is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := value["truncated"]; ok {
		t.Fatalf("under-threshold json should not be an envelope:\n%s", out)
	}
	if value["id"] != taskID {
		t.Fatalf("json output lost its shape: %v", value)
	}
}

func TestStructuredYAMLTruncationEnvelope(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := createTaskWithBody(t, r, projectID, bigBody(500, 100))

	out := r.run("task", "get", taskID, "-o", "yaml")
	var env struct {
		Truncated      bool   `yaml:"truncated"`
		FullOutputPath string `yaml:"full_output_path"`
		Preview        string `yaml:"preview"`
	}
	if err := yaml.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("truncated yaml output is not valid YAML: %v\n%s", err, out)
	}
	if !env.Truncated || env.FullOutputPath == "" {
		t.Fatalf("yaml envelope missing truncation metadata:\n%s", out)
	}
}
