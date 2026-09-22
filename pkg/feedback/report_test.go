package feedback

import (
	"strings"
	"testing"
)

func TestReportTitleUsesFirstLine(t *testing.T) {
	report := Report{Message: "next lists blocked tasks\nhappens on ft task next"}
	if got := report.Title(); got != "next lists blocked tasks" {
		t.Fatalf("Title() = %q, want %q", got, "next lists blocked tasks")
	}
}

func TestReportTitleSkipsLeadingBlankLines(t *testing.T) {
	cases := map[string]string{
		"\nreal bug starts on line two": "real bug starts on line two",
		"   \n\t\n  real bug":           "real bug",
		"first line\nsecond line":       "first line",
		"\n\n\nonly on the fourth line": "only on the fourth line",
		"   \t  ":                       "",
	}
	for message, want := range cases {
		if got := (Report{Message: message}).Title(); got != want {
			t.Errorf("Title(%q) = %q, want %q", message, got, want)
		}
	}
}

func TestReportTitleTruncates(t *testing.T) {
	report := Report{Message: strings.Repeat("a", 200)}
	got := report.Title()
	if len(got) > 83 {
		t.Fatalf("Title() length = %d, want <= 83", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("Title() = %q, want a trailing ellipsis", got)
	}
}

func TestReportBodyCollectsMessageFlowVersionProjectRepo(t *testing.T) {
	report := Report{
		Message: "blocked tasks still show up in next",
		Flow:    "ft task next",
		Version: "v1.2.3",
		Project: "acme",
		Repo:    "github:acme/widget",
	}
	body := report.Body()
	for _, want := range []string{
		"blocked tasks still show up in next",
		"flow: ft task next",
		"ft version: v1.2.3",
		"project: acme",
		"repo: github:acme/widget",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Body() = %q, want it to contain %q", body, want)
		}
	}
}

func TestReportBodyOmitsEmptyOptionalFields(t *testing.T) {
	report := Report{Message: "just a note", Version: "dev"}
	body := report.Body()
	if strings.Contains(body, "flow:") {
		t.Errorf("Body() = %q, want no flow line when flow is empty", body)
	}
	if strings.Contains(body, "project:") {
		t.Errorf("Body() = %q, want no project line when project is empty", body)
	}
	if strings.Contains(body, "repo:") {
		t.Errorf("Body() = %q, want no repo line when repo is empty", body)
	}
	if !strings.Contains(body, "ft version: dev") {
		t.Errorf("Body() = %q, want the version line", body)
	}
}

func TestSanitizeRedactsHomePath(t *testing.T) {
	report := Report{Message: "config at /home/khoi/.factotum/config.toml leaked"}
	got := report.Sanitize("/home/khoi").Message
	if strings.Contains(got, "/home/khoi") {
		t.Fatalf("Sanitize() left the home path: %q", got)
	}
	if !strings.Contains(got, "$HOME/.factotum/config.toml") {
		t.Fatalf("Sanitize() = %q, want the path under $HOME", got)
	}
}

func TestSanitizeLeavesLookalikeHomeUntouched(t *testing.T) {
	report := Report{Message: "user /home/khoinguyen is unrelated"}
	got := report.Sanitize("/home/khoi").Message
	if got != report.Message {
		t.Fatalf("Sanitize() = %q, want the message unchanged", got)
	}
}

func TestSanitizeRedactsTokenShapedStrings(t *testing.T) {
	cases := map[string]string{
		"ghp_abcdefghijklmnopqrstuvwxyz012345":  "github token",
		"sk-1234567890abcdefghijklmnop":         "openai token",
		"apikey_26589a41f1404814a8ab8d660eb4c9": "typesafe token",
		"550e8400-e29b-41d4-a716-446655440000":  "opaque identifier",
	}
	for token := range cases {
		report := Report{Message: "leaked " + token + " here"}
		got := report.Sanitize("").Message
		if strings.Contains(got, token) {
			t.Errorf("Sanitize(%q) left the token: %q", token, got)
		}
		if !strings.Contains(got, "[redacted]") {
			t.Errorf("Sanitize(%q) = %q, want a redaction marker", token, got)
		}
	}
}

func TestSanitizeKeepsOrdinaryProse(t *testing.T) {
	report := Report{Message: "the ready_for_review transition does not unblock dependents"}
	if got := report.Sanitize("/home/khoi"); got.Message != report.Message {
		t.Fatalf("Sanitize() = %q, want ordinary prose unchanged", got.Message)
	}
}

func TestSanitizeKeepsLongOrdinaryRuns(t *testing.T) {
	cases := map[string]string{
		"long word":       strings.Repeat("a", 40),
		"hyphenated":      "this-is-a-very-long-descriptive-slug-without-digits",
		"underscore name": "a_descriptive_identifier_that_is_definitely_not_a_secret",
	}
	for name, run := range cases {
		report := Report{Message: "not a secret: " + run}
		if got := report.Sanitize("").Message; got != report.Message {
			t.Errorf("Sanitize(%s) = %q, want the ordinary run unchanged", name, got)
		}
	}
}

func TestSanitizeNormalizesHomeTrailingSlash(t *testing.T) {
	report := Report{Message: "config at /home/khoi/.factotum/config.toml leaked"}
	got := report.Sanitize("/home/khoi/").Message
	if strings.Contains(got, "/home/khoi") {
		t.Fatalf("Sanitize() left the home path: %q", got)
	}
	if !strings.Contains(got, "$HOME/.factotum/config.toml") {
		t.Fatalf("Sanitize() = %q, want the path under $HOME", got)
	}
}
