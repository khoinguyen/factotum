package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// callerHint is a best-effort guess at the agent CLI this invocation runs
// under. It is evidence, never identity: bindCaller warns about a hint and
// leaves the invocation unattributed, so a guess can never stamp an event or
// become an assignee, note author, or reminder target.
type callerHint struct {
	// Agent is the harness name the guess points at, e.g. "claude" or "opencode".
	Agent string
	// Evidence names the signal the guess rests on, e.g. "environment variable
	// CLAUDECODE" or `parent process "codex"`. It never includes a variable's
	// value, which may carry a session id.
	Evidence string
	// Terminal records whether stdin is attached to a terminal, so the warning
	// can say whether a person is plausibly at the keyboard.
	Terminal bool
}

// callerProbe is the raw observation detection reasons over. Splitting it from
// the operating-system probes keeps the decision table testable without a real
// process tree or environment.
type callerProbe struct {
	// getenv reads an environment variable; nil means an empty environment.
	getenv func(string) string
	// parents are the ancestor command names, nearest first (callerAncestry).
	parents []string
	// tty reports whether stdin is attached to a terminal.
	tty bool
}

// callerHintEnvMarkers is the ordered table of agent-CLI environment markers.
// A harness that sets one of these identifies itself; the first match in table
// order wins, so a more specific marker (OpenCode's own variable) beats the
// inherited generic AI_AGENT below. Presence is the signal, so no value is read.
var callerHintEnvMarkers = []struct {
	agent string
	vars  []string
}{
	{agent: "claude", vars: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_CHILD_SESSION"}},
	{agent: "codex", vars: []string{"CODEX_THREAD_ID", "CODEX_CI"}},
	{agent: "cursor", vars: []string{"CURSOR_AGENT", "CURSOR_TRACE_ID"}},
	{agent: "opencode", vars: []string{"OPENCODE", "OPENCODE_SESSION_ID"}},
	{agent: "pi", vars: []string{"PI_CODING_AGENT"}},
	{agent: "gemini", vars: []string{"GEMINI_CLI"}},
	{agent: "qwen", vars: []string{"QWEN_CODE"}},
}

// callerHintAIAgentPrefixes maps AI_AGENT's value to a harness. AI_AGENT is the
// generic marker several harnesses set, and a nested agent keeps the outer
// value, so it is consulted only after no harness-specific marker matched.
var callerHintAIAgentPrefixes = []struct {
	prefix string
	agent  string
}{
	{prefix: "claude-code", agent: "claude"},
	{prefix: "opencode", agent: "opencode"},
	{prefix: "pi", agent: "pi"},
	{prefix: "gemini-cli", agent: "gemini"},
}

// callerHintProcesses maps an ancestor command's base name to the harness it
// names. A base name matches exactly or as "<name>-<suffix>", which is how a
// wrapper such as opencode-pty and a Windows binary such as gemini.exe match.
var callerHintProcesses = []struct {
	name  string
	agent string
}{
	{name: "claude", agent: "claude"},
	{name: "codex", agent: "codex"},
	{name: "cursor-agent", agent: "cursor"},
	{name: "opencode", agent: "opencode"},
	{name: "gemini", agent: "gemini"},
	{name: "qwen", agent: "qwen"},
	{name: "aider", agent: "aider"},
	{name: "goose", agent: "goose"},
	{name: "pi", agent: "pi"},
}

// detectCallerHint guesses which agent CLI launched this invocation, or reports
// none. The order is deliberate: a harness-specific environment marker is the
// most precise signal, then the generic AI_AGENT, then the nearest agent binary
// in the process ancestry. A terminal on stdin is evidence only - it never
// produces a guess, because a human shell is exactly the case no heuristic can
// name.
func detectCallerHint(probe callerProbe) *callerHint {
	getenv := probe.getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	for _, marker := range callerHintEnvMarkers {
		for _, name := range marker.vars {
			if getenv(name) != "" {
				return &callerHint{
					Agent:    marker.agent,
					Evidence: "environment variable " + name,
					Terminal: probe.tty,
				}
			}
		}
	}
	if ai := getenv("AI_AGENT"); ai != "" {
		for _, prefix := range callerHintAIAgentPrefixes {
			if strings.HasPrefix(ai, prefix.prefix) {
				return &callerHint{Agent: prefix.agent, Evidence: "environment variable AI_AGENT", Terminal: probe.tty}
			}
		}
	}
	for _, parent := range probe.parents {
		base := strings.TrimSuffix(filepath.Base(parent), ".exe")
		for _, known := range callerHintProcesses {
			if base == known.name || strings.HasPrefix(base, known.name+"-") {
				return &callerHint{
					Agent:    known.agent,
					Evidence: "parent process " + strconv.Quote(base),
					Terminal: probe.tty,
				}
			}
		}
	}
	return nil
}

// probeCallerHint is the real probe behind Deps.CallerHint.
func probeCallerHint(getenv func(string) string, tty bool) *callerHint {
	return detectCallerHint(callerProbe{
		getenv:  getenv,
		parents: callerAncestry(callerAncestryDepth),
		tty:     tty,
	})
}

// callerAncestryDepth bounds the process walk: an agent CLI launches the shell
// that runs ft, so the nearest few ancestors cover it while a deeper walk would
// pay another ps call for no better guess.
const callerAncestryDepth = 4

// callerAncestry returns the command names of the processes that started this
// one, nearest first, up to limit. One ps reads the whole tree, so a miss costs
// a single subprocess. A failed probe returns what was read so far: a heuristic
// that cannot see the tree simply has no hint.
func callerAncestry(limit int) []string {
	out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "comm=").Output()
	if err != nil {
		return nil
	}
	parents := map[int]int{}
	names := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		parents[pid] = parent
		names[pid] = strings.Join(fields[2:], " ")
	}
	var chain []string
	for pid := os.Getppid(); len(chain) < limit && pid > 1; {
		name, ok := names[pid]
		if !ok {
			break
		}
		chain = append(chain, name)
		pid = parents[pid]
	}
	return chain
}
