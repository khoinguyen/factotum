package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

// TestParseMessageLinks pins the --link grammar: kind=url, with an optional
// `title=text` field after a single space. The url is one RFC 3986 token, so a
// raw space is never part of it; the title takes the rest of the argument, so a
// title may contain spaces (and equals signs, common in a url but also legal in
// a title). A field other than title= is rejected rather than guessed at.
func TestParseMessageLinks(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  core.Link
	}{
		{
			name:  "kind and url only",
			value: "issue=https://example.invalid/2",
			want:  core.Link{Kind: core.LinkIssue, URL: "https://example.invalid/2"},
		},
		{
			name:  "url keeps its query string equals signs",
			value: "url=https://example.invalid/search?q=a=b&n=1",
			want:  core.Link{Kind: core.LinkURL, URL: "https://example.invalid/search?q=a=b&n=1"},
		},
		{
			name:  "title after one space",
			value: "doc=https://example.invalid/spec title=Design doc",
			want:  core.Link{Kind: core.LinkDoc, URL: "https://example.invalid/spec", Title: "Design doc"},
		},
		{
			name:  "title may contain spaces and equals signs",
			value: "doc=https://example.invalid/spec title=Design=doc, draft 2",
			want:  core.Link{Kind: core.LinkDoc, URL: "https://example.invalid/spec", Title: "Design=doc, draft 2"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			links, err := parseMessageLinks([]string{tc.value})
			if err != nil {
				t.Fatalf("parseMessageLinks(%q) error = %v", tc.value, err)
			}
			if len(links) != 1 || links[0] != tc.want {
				t.Fatalf("parseMessageLinks(%q) = %+v, want %+v", tc.value, links, tc.want)
			}
		})
	}
}

// TestParseMessageLinksRejectsBadLinks pins the reject side against the same
// rule the HTTP send path enforces through core.Link.Validate: a title is
// optional, but when present it is one line - not blank, no control rune - and
// the url is one line too, so the rendered `[kind] url - title` line cannot be
// forged or broken. The separator is one literal space: a tab is not a
// boundary, so it stays in the url and the url rule refuses it.
func TestParseMessageLinksRejectsBadLinks(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"missing equals", "https://example.invalid/2", "must be kind=url"},
		{"empty url", "issue=", "must be kind=url"},
		{"unknown trailing field", "doc=https://example.invalid/spec label=x", "title=text"},
		{"empty title value", "doc=https://example.invalid/spec title=", "title="},
		{"blank title", "doc=https://example.invalid/spec title=   ", "must not be blank"},
		{"newline in title", "doc=https://example.invalid/spec title=two\nlines", "control characters"},
		{"tab in title", "doc=https://example.invalid/spec title=two\tfields", "control characters"},
		{"newline in url", "doc=https://example.invalid/a\nb", "control characters"},
		{"tab in url", "doc=https://example.invalid/a\tb", "control characters"},
		{"tab before title is not a separator", "doc=https://example.invalid/spec\ttitle=x", "control characters"},
		{"unknown kind", "bogus=https://example.invalid/2", "unknown link kind"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseMessageLinks([]string{tc.value})
			if err == nil {
				t.Fatalf("parseMessageLinks(%q) error = nil, want a rejection", tc.value)
			}
			if !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("parseMessageLinks(%q) error = %v, want core.ErrInvalid", tc.value, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseMessageLinks(%q) error = %q, want it to mention %q", tc.value, err, tc.want)
			}
		})
	}
}

// TestMessageSendLinkTitleRoundTrips walks the CLI shape this task adds: a
// sender sets a title on --link, and every read surface shows it - the get/read
// text block, get -o json, and the inbox entry. An untitled link beside it
// keeps its exact previous line, so the new field does not churn output.
func TestMessageSendLinkTitleRoundTrips(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	messageID := firstField(t, r.run("msg", "send", "actor:linker", "see the doc", "--project", projectID,
		"--from", "builder-t-1",
		"--link", "doc=https://example.invalid/spec title=Design doc",
		"--link", "issue=https://example.invalid/2"))

	wantBlock := "\n=== Links ===\n" +
		"[doc] https://example.invalid/spec - Design doc\n" +
		"[issue] https://example.invalid/2\n"
	if got := r.run("msg", "get", messageID); !strings.Contains(got, wantBlock) {
		t.Fatalf("get should print the title the sender set with --link:\n%s", got)
	}
	if got := r.run("msg", "read", messageID); !strings.Contains(got, wantBlock) {
		t.Fatalf("read should print the same titled link as get:\n%s", got)
	}

	detail := runJSON(t, r, "msg", "get", messageID)
	links, ok := detail["links"].([]any)
	if !ok || len(links) != 2 {
		t.Fatalf("get -o json links = %v, want two links", detail["links"])
	}
	first, _ := links[0].(map[string]any)
	if first["title"] != "Design doc" {
		t.Fatalf("get -o json lost the title: %v", first)
	}
	second, _ := links[1].(map[string]any)
	if _, ok := second["title"]; ok {
		t.Fatalf("an untitled link should carry no title key: %v", second)
	}

	var entries []map[string]any
	out := r.run("msg", "inbox", "--project", projectID, "-o", "json")
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("inbox -o json: %v\n%s", err, out)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox entries = %v, want one", entries)
	}
	entryLinks, _ := entries[0]["links"].([]any)
	firstEntry, _ := entryLinks[0].(map[string]any)
	if firstEntry["title"] != "Design doc" {
		t.Fatalf("inbox lost the title: %v", entryLinks)
	}
}

// TestMessageGetDropsLegacyBlankLinkTitle proves the text block never prints a
// dangling " - ". A title of only spaces is blank by the current rule but
// carries no refused rune, so only the repair that drops an all-whitespace
// title clears it; every text surface then takes the untitled line, and read
// persists the repair.
func TestMessageGetDropsLegacyBlankLinkTitle(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	messageID := firstField(t, r.run("msg", "send", "actor:linker", "see the doc", "--project", projectID,
		"--link", "doc=https://example.invalid/spec title=Design doc"))

	data, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	legacy := bytes.Replace(data, []byte(`"Design doc"`), []byte(`"   "`), 1)
	if bytes.Equal(legacy, data) {
		t.Fatal("fixture did not rewrite the stored title")
	}
	if err := os.WriteFile(r.path, legacy, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	wantBlock := "\n=== Links ===\n[doc] https://example.invalid/spec\n"
	if got := r.run("msg", "get", messageID); !strings.Contains(got, wantBlock) {
		t.Fatalf("get should drop a blank legacy title rather than print a dangling \" - \":\n%s", got)
	}
	read := r.run("msg", "read", messageID)
	if !strings.Contains(read, wantBlock) {
		t.Fatalf("read should print the same dropped title:\n%s", read)
	}
	if strings.Contains(read, " - \n") {
		t.Fatalf("read printed a dangling \" - \":\n%s", read)
	}
	if got := r.run("msg", "get", messageID); !strings.Contains(got, wantBlock) {
		t.Fatalf("read should persist the dropped title:\n%s", got)
	}
}

// TestMessageSendRejectsBadLinkTitle proves the CLI rejects at send time with
// the same rule the HTTP path enforces, so a bad title never reaches the store.
func TestMessageSendRejectsBadLinkTitle(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	for _, spec := range []string{
		"doc=https://example.invalid/spec title=   ",
		"doc=https://example.invalid/spec title=two\nlines",
		"doc=https://example.invalid/spec title=",
	} {
		err := r.runErr("msg", "send", "actor:linker", "see the doc", "--project", projectID, "--link", spec)
		if err == nil {
			t.Fatalf("msg send --link %q should fail", spec)
		}
	}
	var remaining []map[string]any
	out := r.run("msg", "inbox", "--project", projectID, "-o", "json")
	if err := json.Unmarshal([]byte(out), &remaining); err != nil {
		t.Fatalf("inbox -o json: %v\n%s", err, out)
	}
	if len(remaining) != 0 {
		t.Fatalf("a rejected send must not store a message: %v", remaining)
	}
}
