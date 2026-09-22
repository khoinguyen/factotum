package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/khoinguyen/factotum/internal/skills"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/judge"
)

// errSkillDrift marks a lint run that found stale references, so the process exits
// non-zero instead of looking like a usage or internal error.
var errSkillDrift = errors.New("skill drift found")

// flagTokenPattern matches a standalone backticked flag such as `--no-rerank`.
var flagTokenPattern = regexp.MustCompile(`^--?[A-Za-z][A-Za-z0-9-]*$`)

type skillLintFlagDoc struct {
	Skill       string  `json:"skill" yaml:"skill"`
	Token       string  `json:"token" yaml:"token"`
	Probability float64 `json:"probability" yaml:"probability"`
}

type skillLintDoc struct {
	Semantic    bool               `json:"semantic" yaml:"semantic"`
	Findings    []string           `json:"findings,omitempty" yaml:"findings,omitempty"`
	Flags       []skillLintFlagDoc `json:"flags,omitempty" yaml:"flags,omitempty"`
	Unavailable bool               `json:"unavailable,omitempty" yaml:"unavailable,omitempty"`
}

func newSkillLintCommand(deps *Deps) *cobra.Command {
	var semantic bool
	cmd := &cobra.Command{
		Use:   "lint [name]",
		Short: "Check embedded skills for references to commands or flags that no longer exist",
		Long: "Check the embedded skills against the live CLI. The deterministic pass walks the\n" +
			"cobra command tree and reports any command or flag a skill names that no longer\n" +
			"exists; it needs no network and is the CI gate. --semantic adds an opt-in judge\n" +
			"pass over prose references the parser cannot see. Lint reports only; it never\n" +
			"edits a skill.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageError(cmd, "expected at most one skill name")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := lintSkills(args)
			if err != nil {
				return err
			}
			report, err := runSkillLint(cmd.Context(), deps.Judge, cmd.Root(), list, semantic)
			if err != nil {
				return err
			}
			doc := skillLintDoc{
				Semantic:    report.Semantic,
				Findings:    report.Findings,
				Unavailable: report.Unavailable,
			}
			for _, flag := range report.Flags {
				doc.Flags = append(doc.Flags, skillLintFlagDoc{
					Skill: flag.Skill, Token: flag.Token, Probability: flag.Probability,
				})
			}
			if err := deps.emit(doc, func() { deps.printSkillLint(report) }); err != nil {
				return err
			}
			if len(report.Findings) > 0 || len(report.Flags) > 0 {
				return fmt.Errorf("%w: %d stale reference(s), %d semantic flag(s)",
					errSkillDrift, len(report.Findings), len(report.Flags))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&semantic, "semantic", false, "also judge prose references with the configured judge")
	return cmd
}

// lintSkills resolves the optional skill name to the skills to lint.
func lintSkills(args []string) ([]skills.Skill, error) {
	if len(args) == 1 {
		skill, err := skills.Get(args[0])
		if err != nil {
			return nil, err
		}
		return []skills.Skill{skill}, nil
	}
	return skills.All()
}

// skillLintReport is the outcome of one lint run.
type skillLintReport struct {
	Semantic    bool
	Findings    []string
	Flags       []app.SkillTokenFlag
	Unavailable bool
}

// runSkillLint runs the deterministic pass over every skill, then the opt-in
// semantic pass. The semantic pass reports judge.ErrUnavailable as Unavailable so
// the deterministic findings still stand when no key is configured.
func runSkillLint(ctx context.Context, j judge.Judge, root *cobra.Command, list []skills.Skill, semantic bool) (skillLintReport, error) {
	report := skillLintReport{Semantic: semantic}
	for _, skill := range list {
		for _, stale := range staleSkillReferences(root, skill.Body) {
			report.Findings = append(report.Findings, fmt.Sprintf("skill %q: %s", skill.Name, stale))
		}
	}
	if !semantic {
		return report, nil
	}
	inventory := commandInventory(root)
	service := app.NewSkillLintService(j)
	for _, skill := range list {
		flags, err := service.Lint(ctx, app.SkillTokenSet{
			Skill:     skill.Name,
			Tokens:    skillReferenceTokens(root, skill.Body),
			Inventory: inventory,
		})
		if errors.Is(err, judge.ErrUnavailable) {
			report.Unavailable = true
			return report, nil
		}
		if err != nil {
			return report, err
		}
		report.Flags = append(report.Flags, flags...)
	}
	return report, nil
}

func (d *Deps) printSkillLint(report skillLintReport) {
	for _, finding := range report.Findings {
		d.printf("%s\n", finding)
	}
	for _, flag := range report.Flags {
		d.printf("skill %q: %q is not a real ft command or flag (P(real) = %.2f)\n",
			flag.Skill, flag.Token, flag.Probability)
	}
	if report.Unavailable {
		_, _ = fmt.Fprintf(d.Err, "ft: semantic skill lint unavailable: no judge configured; set TYPESAFE_API_KEY to enable it (deterministic check only)\n")
	}
}

// skillReferenceTokens returns the candidate tokens for the semantic pass: every
// command- and flag-position token in an ft invocation found in a fence, an inline
// code span, or plain prose, plus any standalone backticked flag. Flag values,
// placeholders, and arguments are not candidates.
func skillReferenceTokens(root *cobra.Command, body string) []string {
	lines := append(skillCommandLines(body), proseCommandLines(body)...)
	seen := map[string]bool{}
	var out []string
	add := func(token string) {
		if token == "ft" || seen[token] {
			return
		}
		seen[token] = true
		out = append(out, token)
	}
	for _, line := range lines {
		for _, token := range referenceTokensIn(root, line) {
			add(token)
		}
	}
	for _, token := range proseFlagTokens(body) {
		add(token)
	}
	return out
}

// referenceTokensIn walks one `ft ...` invocation and returns every token in a
// command or flag position, whether or not it exists in root.
func referenceTokensIn(root *cobra.Command, line string) []string {
	refs := skillReferences(root, line)
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.token)
	}
	return out
}

// proseFlagTokens returns standalone backticked flags in the body, the prose
// references the deterministic parser cannot see.
func proseFlagTokens(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		for _, span := range inlineCode(strings.TrimSpace(raw)) {
			token := strings.TrimSpace(span)
			if flagTokenPattern.MatchString(token) && !seen[token] {
				seen[token] = true
				out = append(out, token)
			}
		}
	}
	return out
}

// proseCommandLines extracts `ft ...` invocations from plain prose: text outside
// code fences and backticked spans. skillCommandLines covers fences and spans, so
// the two together see every reference the body makes.
func proseCommandLines(body string) []string {
	var out []string
	inFence := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, plain := range plainSegments(line) {
			out = append(out, ftInvocations(plain)...)
		}
	}
	return out
}

// plainSegments splits a line into the text outside backticked spans, so an `ft`
// inside a span is not mistaken for a prose invocation.
func plainSegments(line string) []string {
	var out []string
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			out = append(out, line)
			return out
		}
		out = append(out, line[:start])
		rest := line[start+1:]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			return out
		}
		line = rest[end+1:]
	}
}

// ftInvocations returns every `ft <words>` command in a plain segment, each
// running up to the next `ft` word or the end of the segment.
func ftInvocations(segment string) []string {
	fields := strings.Fields(segment)
	var out []string
	for i := 0; i < len(fields); i++ {
		if fields[i] != "ft" {
			continue
		}
		end := i + 1
		for end < len(fields) && fields[end] != "ft" {
			end++
		}
		if end > i+1 {
			out = append(out, "ft "+strings.Join(fields[i+1:end], " "))
		}
		i = end - 1
	}
	return out
}

// commandInventory walks the cobra command tree and serializes the real command
// paths and flag names into the judge's state.
func commandInventory(root *cobra.Command) app.SkillInventory {
	commands := map[string]bool{}
	flags := map[string]bool{}
	var walk func(*cobra.Command, string)
	walk = func(cmd *cobra.Command, path string) {
		commands[path] = true
		addFlagNames(flags, cmd)
		for _, child := range cmd.Commands() {
			walk(child, path+" "+child.Name())
		}
	}
	walk(root, root.Name())
	// Cobra adds help lazily, so name it explicitly: hasFlag already treats it as
	// real and the semantic pass must agree.
	flags["--help"] = true
	flags["-h"] = true

	inventory := app.SkillInventory{
		Commands: sortedKeys(commands),
		Flags:    sortedKeys(flags),
	}
	return inventory
}

func addFlagNames(flags map[string]bool, cmd *cobra.Command) {
	for _, set := range []*pflag.FlagSet{cmd.Flags(), cmd.InheritedFlags()} {
		if set == nil {
			continue
		}
		set.VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden {
				return
			}
			flags["--"+flag.Name] = true
			if flag.Shorthand != "" {
				flags["-"+flag.Shorthand] = true
			}
		})
	}
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// skillRef is one command or flag token in an `ft ...` invocation and whether the
// command tree recognizes it.
type skillRef struct {
	line  string
	flag  bool
	token string
	valid bool
}

// skillReferences walks one `ft ...` invocation and reports every command and flag
// token in command position, valid or not.
func skillReferences(root *cobra.Command, line string) []skillRef {
	var refs []skillRef
	cmd := root
	descending := true
	skipNext := false
	for _, token := range strings.Fields(line)[1:] {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(token, "-") {
			name := token
			inline := false
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
				inline = true
			}
			refs = append(refs, skillRef{line: line, flag: true, token: name, valid: hasFlag(cmd, name)})
			// A space-separated value belongs to the flag, not to the command
			// chain, so the next token must not be read as a subcommand.
			if !inline && flagTakesValue(cmd, name) {
				skipNext = true
			}
			continue
		}
		if !descending {
			continue
		}
		if child := findChild(cmd, token); child != nil {
			refs = append(refs, skillRef{line: line, token: token, valid: true})
			cmd = child
			continue
		}
		// A plain identifier where a subcommand is expected is stale; a
		// placeholder (<task>), argument (field=value), or the first positional
		// argument ends subcommand descent.
		if isIdentifier(token) && len(cmd.Commands()) > 0 {
			refs = append(refs, skillRef{line: line, token: token, valid: false})
		}
		descending = false
	}
	return refs
}

// staleSkillReferences returns one message per command or flag referenced by an
// `ft ...` invocation in body that no longer exists in root.
func staleSkillReferences(root *cobra.Command, body string) []string {
	var stale []string
	for _, line := range skillCommandLines(body) {
		for _, ref := range skillReferences(root, line) {
			if ref.valid {
				continue
			}
			if ref.flag {
				stale = append(stale, fmt.Sprintf("%q: unknown flag %q", line, ref.token))
			} else {
				stale = append(stale, fmt.Sprintf("%q: unknown command %q", line, ref.token))
			}
		}
	}
	return stale
}

// isIdentifier reports whether token is a bare subcommand-looking word, as
// opposed to a placeholder or an argument value.
func isIdentifier(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return false
	}
	return token[0] >= 'a' && token[0] <= 'z'
}

// skillCommandLines extracts the `ft ...` invocations from a skill: fenced code
// block lines and inline code spans that begin with ft.
func skillCommandLines(body string) []string {
	var out []string
	inFence := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			if command, ok := asFTCommand(line); ok {
				out = append(out, command)
			}
			continue
		}
		for _, span := range inlineCode(line) {
			if command, ok := asFTCommand(span); ok {
				out = append(out, command)
			}
		}
	}
	return out
}

func asFTCommand(line string) (string, bool) {
	if comment := strings.Index(line, " #"); comment >= 0 {
		line = strings.TrimSpace(line[:comment])
	}
	if line == "ft" || strings.HasPrefix(line, "ft ") {
		return line, true
	}
	return "", false
}

func inlineCode(line string) []string {
	var out []string
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			return out
		}
		rest := line[start+1:]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		line = rest[end+1:]
	}
}

func findChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func hasFlag(cmd *cobra.Command, name string) bool {
	if name == "-h" || name == "--help" {
		return true
	}
	return lookupFlag(cmd, name) != nil
}

// lookupFlag finds a long or short flag on cmd or inherited from its parents.
func lookupFlag(cmd *cobra.Command, name string) *pflag.Flag {
	flags := cmd.Flags()
	inherited := cmd.InheritedFlags()
	switch {
	case strings.HasPrefix(name, "--"):
		if flag := flags.Lookup(name[2:]); flag != nil {
			return flag
		}
		return inherited.Lookup(name[2:])
	case strings.HasPrefix(name, "-"):
		if flag := flags.ShorthandLookup(name[1:]); flag != nil {
			return flag
		}
		return inherited.ShorthandLookup(name[1:])
	default:
		return nil
	}
}

// flagTakesValue reports whether a known flag consumes the following token: a
// boolean flag carries a default value and needs none.
func flagTakesValue(cmd *cobra.Command, name string) bool {
	flag := lookupFlag(cmd, name)
	if flag == nil {
		return false
	}
	return flag.NoOptDefVal == ""
}
