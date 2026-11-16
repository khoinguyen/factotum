package cli

import (
	"strings"
	"testing"
)

// TestDetectCallerHintFromEnv pins the environment half of the heuristic: each
// supported harness is recognised by the marker it exports, and the evidence
// names the variable rather than echoing a value that may carry a session id.
func TestDetectCallerHintFromEnv(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		want     string
		evidence string
	}{
		{name: "claude code", env: map[string]string{"CLAUDECODE": "1"}, want: "claude", evidence: "CLAUDECODE"},
		{name: "claude code entrypoint", env: map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, want: "claude", evidence: "CLAUDE_CODE_ENTRYPOINT"},
		{name: "codex", env: map[string]string{"CODEX_THREAD_ID": "0f4a"}, want: "codex", evidence: "CODEX_THREAD_ID"},
		{name: "cursor", env: map[string]string{"CURSOR_AGENT": "1"}, want: "cursor", evidence: "CURSOR_AGENT"},
		{name: "opencode", env: map[string]string{"OPENCODE": "1"}, want: "opencode", evidence: "OPENCODE"},
		{name: "opencode session", env: map[string]string{"OPENCODE_SESSION_ID": "ses_x"}, want: "opencode", evidence: "OPENCODE_SESSION_ID"},
		{name: "pi", env: map[string]string{"PI_CODING_AGENT": "true"}, want: "pi", evidence: "PI_CODING_AGENT"},
		{name: "gemini", env: map[string]string{"GEMINI_CLI": "1"}, want: "gemini", evidence: "GEMINI_CLI"},
		{name: "qwen", env: map[string]string{"QWEN_CODE": "1"}, want: "qwen", evidence: "QWEN_CODE"},
		{name: "ai agent claude code", env: map[string]string{"AI_AGENT": "claude-code_2-1-310_agent"}, want: "claude", evidence: "AI_AGENT"},
		{name: "ai agent opencode", env: map[string]string{"AI_AGENT": "opencode"}, want: "opencode", evidence: "AI_AGENT"},
		{name: "ai agent pi", env: map[string]string{"AI_AGENT": "pi"}, want: "pi", evidence: "AI_AGENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint := detectCallerHint(callerProbe{getenv: envLookup(tc.env)})
			if hint == nil {
				t.Fatalf("detectCallerHint = nil, want %s", tc.want)
			}
			if hint.Agent != tc.want {
				t.Fatalf("agent = %q, want %q", hint.Agent, tc.want)
			}
			if !strings.Contains(hint.Evidence, tc.evidence) {
				t.Fatalf("evidence = %q, want it to name %s", hint.Evidence, tc.evidence)
			}
		})
	}
}

// TestDetectCallerHintFromAncestry pins the process half: a known agent binary
// among the ancestors is a guess, the nearest one wins when two are present,
// and a wrapper name that starts with the binary's name counts.
func TestDetectCallerHintFromAncestry(t *testing.T) {
	cases := []struct {
		name    string
		parents []string
		want    string
	}{
		{name: "opencode pty wrapper", parents: []string{"opencode-pty", "zsh", "ghostty"}, want: "opencode"},
		{name: "full path", parents: []string{"/usr/local/bin/claude", "-zsh"}, want: "claude"},
		{name: "nearest agent wins", parents: []string{"sh", "codex", "claude"}, want: "codex"},
		{name: "windows suffix", parents: []string{"gemini.exe"}, want: "gemini"},
		{name: "cursor agent", parents: []string{"cursor-agent", "zsh"}, want: "cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint := detectCallerHint(callerProbe{getenv: envLookup(nil), parents: tc.parents})
			if hint == nil {
				t.Fatalf("detectCallerHint = nil, want %s", tc.want)
			}
			if hint.Agent != tc.want {
				t.Fatalf("agent = %q, want %q", hint.Agent, tc.want)
			}
			if !strings.Contains(hint.Evidence, "parent process") {
				t.Fatalf("evidence = %q, want a parent-process trace", hint.Evidence)
			}
		})
	}
}

// TestDetectCallerHintEnvBeatsAncestry pins the order: an explicit marker names
// the harness this command runs under, while an ancestor can be a wrapper the
// marker is more precise than.
func TestDetectCallerHintEnvBeatsAncestry(t *testing.T) {
	hint := detectCallerHint(callerProbe{
		getenv:  envLookup(map[string]string{"OPENCODE": "1"}),
		parents: []string{"claude", "zsh"},
	})
	if hint == nil || hint.Agent != "opencode" {
		t.Fatalf("hint = %+v, want opencode from the env marker", hint)
	}
}

// TestDetectCallerHintUnknown is the unknown outcome: no marker and no agent in
// the ancestry yields no guess at all, so no warning is printed.
func TestDetectCallerHintUnknown(t *testing.T) {
	hint := detectCallerHint(callerProbe{
		getenv:  envLookup(nil),
		parents: []string{"zsh", "ghostty", "login"},
		tty:     true,
	})
	if hint != nil {
		t.Fatalf("detectCallerHint = %+v, want nil with no signal", hint)
	}
}

// TestDetectCallerHintTerminalAloneIsNotAGuess pins that a terminal on stdin
// is evidence, never a guess: a human shell is exactly the unguessable case.
func TestDetectCallerHintTerminalAloneIsNotAGuess(t *testing.T) {
	if hint := detectCallerHint(callerProbe{getenv: envLookup(nil), tty: true}); hint != nil {
		t.Fatalf("detectCallerHint = %+v, want nil for a terminal alone", hint)
	}
}

// TestDetectCallerHintRecordsTheTerminal pins that the terminal is carried as
// evidence alongside the match, not used to invent one.
func TestDetectCallerHintRecordsTheTerminal(t *testing.T) {
	hint := detectCallerHint(callerProbe{
		getenv: envLookup(map[string]string{"CLAUDECODE": "1"}),
		tty:    true,
	})
	if hint == nil || !hint.Terminal {
		t.Fatalf("hint = %+v, want a hint that records the terminal", hint)
	}
}

// TestCallerAncestryReadsTheProcessTree keeps the real probe honest: the test
// process always has an ancestry, so an empty chain means the ps parse broke.
func TestCallerAncestryReadsTheProcessTree(t *testing.T) {
	chain := callerAncestry(4)
	if len(chain) == 0 {
		t.Fatal("callerAncestry = empty, want this process's parents")
	}
	for _, name := range chain {
		if strings.TrimSpace(name) == "" {
			t.Fatalf("callerAncestry returned a blank command name: %q", chain)
		}
	}
}

// TestHeuristicCallerWarningIsUnverifiedAndUnattributed is the headline
// behaviour: with no identity named, a detected guess is warned about as an
// unverified heuristic and the event stays unattributed - the guess is never
// stamped, registered, or used as an actor.
func TestHeuristicCallerWarningIsUnverifiedAndUnattributed(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	r.callerHint = func() *callerHint {
		return &callerHint{Agent: "opencode", Evidence: "environment variable OPENCODE", Terminal: true}
	}

	_, stderr := r.runSplit("project", "create", "Acme")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By != nil {
		t.Fatalf("project.created By = %v, want none for a heuristic guess", created.By)
	}
	for _, want := range []string{"unverified heuristic", "opencode", "unattributed"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
	if actors := r.run("actor", "list"); strings.Contains(actors, "opencode") {
		t.Fatalf("a heuristic guess was registered as an actor:\n%s", actors)
	}
}

// TestHeuristicCallerWarningIsSilentWithoutAGuess is the unknown-outcome end to
// end: nothing detected, no warning, still unattributed.
func TestHeuristicCallerWarningIsSilentWithoutAGuess(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }

	_, stderr := r.runSplit("project", "create", "Acme")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By != nil {
		t.Fatalf("project.created By = %v, want none", created.By)
	}
	if strings.Contains(stderr, "heuristic") || strings.Contains(stderr, "probable") {
		t.Fatalf("stderr = %q, want no caller guess", stderr)
	}
}

// TestHeuristicCallerDoesNotOverrideExplicitCaller pins precedence: a named,
// registered caller is attributed and the heuristic stays quiet.
func TestHeuristicCallerDoesNotOverrideExplicitCaller(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	r.run("actor", "create", "alice")
	r.callerHint = func() *callerHint {
		return &callerHint{Agent: "opencode", Evidence: "environment variable OPENCODE"}
	}

	r.getenv = func(key string) string {
		if key == "FACTOTUM_CALLER" {
			return "alice"
		}
		return ""
	}
	_, stderr := r.runSplit("project", "create", "Acme")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By == nil || *created.By != "alice" {
		t.Fatalf("project.created By = %v, want alice", created.By)
	}
	if strings.Contains(stderr, "heuristic") {
		t.Fatalf("stderr = %q, want no heuristic warning when a caller is named", stderr)
	}
}

// TestHeuristicCallerDoesNotOverrideConfiguredDefault pins the same precedence
// for a configured default actor.
func TestHeuristicCallerDoesNotOverrideConfiguredDefault(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	r.run("actor", "create", "def")
	r.callerHint = func() *callerHint {
		return &callerHint{Agent: "claude", Evidence: "environment variable CLAUDECODE"}
	}

	r.getenv = func(key string) string {
		if key == "FACTOTUM_DEFAULT_ACTOR" {
			return "def"
		}
		return ""
	}
	_, stderr := r.runSplit("project", "create", "Acme")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By == nil || *created.By != "def" {
		t.Fatalf("project.created By = %v, want def", created.By)
	}
	if strings.Contains(stderr, "heuristic") {
		t.Fatalf("stderr = %q, want no heuristic warning when a default is configured", stderr)
	}
}

// TestHeuristicCallerStaysQuietForADroppedCaller pins that a named identity
// which was dropped keeps its own warning: the heuristic does not pile a second,
// contradictory guess on top of a typo the user can fix.
func TestHeuristicCallerStaysQuietForADroppedCaller(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_CALLER" {
			return "ghost"
		}
		return ""
	}
	r.callerHint = func() *callerHint {
		return &callerHint{Agent: "opencode", Evidence: "environment variable OPENCODE"}
	}

	_, stderr := r.runSplit("project", "create", "Acme")

	created := findEvent(t, listEvents(t, r), "project.created")
	if created.By != nil {
		t.Fatalf("project.created By = %v, want none for an unregistered caller", created.By)
	}
	if !strings.Contains(stderr, `caller "ghost" is not a registered actor`) {
		t.Fatalf("stderr = %q, want the unregistered-caller warning", stderr)
	}
	if strings.Contains(stderr, "heuristic") {
		t.Fatalf("stderr = %q, want no heuristic warning for a dropped caller", stderr)
	}
}

func envLookup(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}
