// Package openshell holds the NVIDIA OpenShell isolation backend for `ft run`.
//
// This file owns the safe default policy for an OpenCode agent run: the
// checked-in template (policy.yaml) and the rules that render the effective
// sandbox policy from it. It is deliberately a pure, gateway-free unit so the
// policy can be tested without a live OpenShell runtime.
//
// The rendered policy is deny-by-default. Egress is allowed only for hosts the
// project names explicitly; the model provider endpoint is the one exception,
// attached separately by the OpenShell credential provider (from run.provider),
// not listed here. OpenCode's phone-home to models.opencode.ai and
// registry.npmjs.org is optional and denied until a project opts in. Each
// allowed host becomes a rule scoped to the harness binary, not to every
// process in the sandbox. The workload runs as an explicit non-root identity,
// and no credential value is ever written: secrets are attached through an
// OpenShell provider, which injects a placeholder and substitutes the real
// value only at the provider-authorized endpoint.
//
// A project opts in through a committed override file,
// .factotum/openshell-policy.yaml, whose only permitted key is allow_hosts:
//
//	allow_hosts:
//	  - models.opencode.ai
//	  - registry.npmjs.org
//
// Anything else is rejected, so a project can widen egress but can never
// weaken filesystem, identity, or credential handling. The policy advisor's
// auto-approval stays off: sandboxes are created with ApprovalArgs, which pins
// --approval-mode manual and never auto.
package openshell

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

//go:embed policy.yaml
var basePolicy []byte

const (
	// DefaultRunAsUser and DefaultRunAsGroup are the non-root identity the
	// shipped OpenCode image runs as. The image declares no USER, so leaving
	// these to the image default would run the agent as root.
	DefaultRunAsUser  = "1000"
	DefaultRunAsGroup = "1000"
	// DefaultHarnessBinary is the in-image path the egress rules are scoped to
	// for the shipped OpenCode image.
	DefaultHarnessBinary = "/usr/local/bin/opencode"
	// OpenCodeModelsHost and OpenCodeRegistryHost are OpenCode's phone-home
	// destinations. They are denied by default and allowed only when a project
	// names them in its override.
	OpenCodeModelsHost   = "models.opencode.ai"
	OpenCodeRegistryHost = "registry.npmjs.org"
	// ApprovalModeFlag and ApprovalModeManual pin the policy advisor to human
	// review. auto-approval is never enabled by ft.
	ApprovalModeFlag   = "--approval-mode"
	ApprovalModeManual = "manual"
	// overrideFileName is the committed project override, relative to the
	// project root's .factotum directory.
	overrideFileName = "openshell-policy.yaml"
	allowPort        = 443
)

// Options configures the rendered policy and the backend that applies it.
type Options struct {
	// OverridePath is the project's committed override file. An empty path, or
	// a path that does not exist, means no opt-in.
	OverridePath string
	// AllowHosts are additional egress hosts for this run, merged with the
	// override and deduplicated.
	AllowHosts []string
	// RunAsUser and RunAsGroup override the non-root identity. A root value is
	// rejected.
	RunAsUser  string
	RunAsGroup string
	// HarnessBinary is the in-image path egress rules are scoped to. Defaults
	// to DefaultHarnessBinary.
	HarnessBinary string

	// ReadOnly and ReadWrite are additional filesystem paths merged into the
	// hardened template's read-only and read-write sets. They are additive only,
	// so a run can mount extra paths but can never drop the hardened baseline.
	ReadOnly  []string
	ReadWrite []string

	// CLI is the openshell executable the backend shells out to. Defaults to
	// DefaultCLI.
	CLI string
	// Image is the sandbox image used when Spec.Image.Ref is empty. Defaults to
	// DefaultImage, so a spec with no image (for example the conformance suite)
	// still prepares.
	Image string
	// Workdir is the in-sandbox directory a run executes in. Defaults to
	// DefaultWorkdir.
	Workdir string
	// Gateway selects a named openShell gateway. Empty uses the CLI default.
	Gateway string
	// Credentials resolves a provider credential reference to its secret value,
	// which is passed to the gateway as provider material. A nil resolver makes
	// AttachCredential return ErrUnsupported: the backend never reads a secret
	// on its own.
	Credentials CredentialResolver
	// ReadyTimeout bounds sandbox readiness polling. Zero uses
	// DefaultReadyTimeout.
	ReadyTimeout time.Duration
	// ProviderCleanupTimeout bounds retrying provider deletion after a sandbox
	// is deleted (the gateway detaches providers asynchronously). Zero uses
	// providerDeleteAttempts * providerDeleteDelay.
	ProviderCleanupTimeout time.Duration
	// ProviderCleanupInterval is the pause between provider-delete retries.
	// Zero uses providerDeleteDelay.
	ProviderCleanupInterval time.Duration
	// NewName returns a sandbox name; nil generates a random one. The gateway
	// caps sandbox names at 19 characters.
	NewName func() string
	// Runner runs the openshell CLI. Nil execs the real binary; tests inject a
	// fake so no gateway is needed.
	Runner Runner
}

// Policy is an OpenShell version-1 policy document.
type Policy struct {
	Version          int                      `yaml:"version"`
	FilesystemPolicy FilesystemPolicy         `yaml:"filesystem_policy"`
	Process          ProcessPolicy            `yaml:"process"`
	NetworkPolicies  map[string]NetworkPolicy `yaml:"network_policies,omitempty"`
}

// FilesystemPolicy mirrors OpenShell's filesystem_policy section.
type FilesystemPolicy struct {
	ReadOnly       []string `yaml:"read_only"`
	ReadWrite      []string `yaml:"read_write"`
	IncludeWorkdir bool     `yaml:"include_workdir"`
}

// ProcessPolicy mirrors OpenShell's process section.
type ProcessPolicy struct {
	RunAsUser  string `yaml:"run_as_user"`
	RunAsGroup string `yaml:"run_as_group"`
}

// NetworkPolicy is one binary-scoped egress rule.
type NetworkPolicy struct {
	Name      string     `yaml:"name"`
	Endpoints []Endpoint `yaml:"endpoints"`
	Binaries  []Binary   `yaml:"binaries"`
}

// Endpoint is one allowed host and port.
type Endpoint struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Protocol    string `yaml:"protocol"`
	Enforcement string `yaml:"enforcement"`
	Access      string `yaml:"access"`
}

// Binary scopes a rule to one executable, resolved by the kernel, so another
// process in the sandbox cannot borrow the rule.
type Binary struct {
	Path string `yaml:"path"`
}

// Override is the schema of a project's committed override file. Decoding is
// strict, so allow_hosts is the only key a project may set.
type Override struct {
	AllowHosts []string `yaml:"allow_hosts"`
}

// Build renders the effective policy: the checked-in template plus the
// project override and per-run hosts, with the non-root identity applied.
func Build(opts Options) (Policy, error) {
	var p Policy
	if err := yaml.Unmarshal(basePolicy, &p); err != nil {
		return Policy{}, fmt.Errorf("openshell: parse embedded policy: %w", err)
	}

	user, group, err := identity(opts)
	if err != nil {
		return Policy{}, err
	}
	p.Process = ProcessPolicy{RunAsUser: user, RunAsGroup: group}
	p.FilesystemPolicy.ReadOnly = mergePaths(p.FilesystemPolicy.ReadOnly, opts.ReadOnly)
	p.FilesystemPolicy.ReadWrite = mergePaths(p.FilesystemPolicy.ReadWrite, opts.ReadWrite)

	hosts := append([]string(nil), opts.AllowHosts...)
	if opts.OverridePath != "" {
		ov, err := LoadOverride(opts.OverridePath)
		if err != nil {
			return Policy{}, err
		}
		hosts = append(hosts, ov.AllowHosts...)
	}
	hosts, err = normalizeHosts(hosts)
	if err != nil {
		return Policy{}, err
	}
	if len(hosts) > 0 {
		p.NetworkPolicies = rulesFor(hosts, firstNonEmpty(opts.HarnessBinary, DefaultHarnessBinary))
	}
	return p, nil
}

// LoadOverride reads a project override. A missing file is not an error: it
// means the project did not opt in. Unknown keys are rejected.
func LoadOverride(path string) (Override, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Override{}, nil
	}
	if err != nil {
		return Override{}, fmt.Errorf("openshell: read override %s: %w", path, err)
	}
	var ov Override
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&ov); err != nil {
		if errors.Is(err, io.EOF) {
			return Override{}, nil
		}
		return Override{}, fmt.Errorf("openshell: parse override %s: %w", path, err)
	}
	return ov, nil
}

// YAML renders the policy document.
func (p Policy) YAML() ([]byte, error) {
	raw, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("openshell: render policy: %w", err)
	}
	return raw, nil
}

// Isolation projects the policy onto the isolation port. DefaultDeny is always
// true and Credentials is always empty: this is the policy, not a secret store.
func (p Policy) Isolation() (isolation.Policy, error) {
	raw, err := p.YAML()
	if err != nil {
		return isolation.Policy{}, err
	}
	return isolation.Policy{
		ReadOnly:    append([]string(nil), p.FilesystemPolicy.ReadOnly...),
		ReadWrite:   append([]string(nil), p.FilesystemPolicy.ReadWrite...),
		AllowHosts:  allowedHosts(p.NetworkPolicies),
		DefaultDeny: true,
		RunAsUser:   p.Process.RunAsUser,
		RunAsGroup:  p.Process.RunAsGroup,
		Raw:         raw,
	}, nil
}

// ApprovalArgs are the sandbox-create flags that keep the policy advisor's
// proposals pending human review. ft never enables auto-approval.
func ApprovalArgs() []string {
	return []string{ApprovalModeFlag, ApprovalModeManual}
}

// OverridePath returns the committed override path for a project root.
func OverridePath(projectRoot string) string {
	return filepath.Join(projectRoot, ".factotum", overrideFileName)
}

// OverridePathForConfig returns the committed override path for a loaded config
// file. The override is the config file's sibling, so a custom -c path resolves
// the policy next to the config it actually loaded instead of assuming a
// <root>/.factotum/config.toml layout.
func OverridePathForConfig(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), overrideFileName)
}

func identity(opts Options) (string, string, error) {
	user := strings.TrimSpace(firstNonEmpty(opts.RunAsUser, DefaultRunAsUser))
	if !validIdentity(user) {
		return "", "", fmt.Errorf("openshell: run_as_user %q is not a non-root identity; use numeric UID 1..4294967294 or \"sandbox\"", user)
	}
	group := strings.TrimSpace(firstNonEmpty(opts.RunAsGroup, DefaultRunAsGroup))
	if !validIdentity(group) {
		return "", "", fmt.Errorf("openshell: run_as_group %q is not a non-root identity; use numeric GID 1..4294967294 or \"sandbox\"", group)
	}
	return user, group, nil
}

// validIdentity mirrors the gateway's process identity rule: the literal
// "sandbox" or a numeric uid/gid in [1, 4294967294]. Root (0 in any spelling)
// and the u32::MAX sentinel are rejected locally so the invariant holds before
// the gateway sees the policy.
func validIdentity(value string) bool {
	if value == "sandbox" {
		return true
	}
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return false
	}
	return n >= 1 && n <= uint64(^uint32(0))-1
}

// mergePaths appends extra to base, preserving base order and dropping empty
// and duplicate paths (cleaned), so the rendered filesystem policy stays
// deterministic and additive.
func mergePaths(base, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, group := range [][]string{base, extra} {
		for _, p := range group {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			p = path.Clean(p)
			if seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func normalizeHosts(hosts []string) ([]string, error) {
	seen := make(map[string]bool, len(hosts))
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if !validHost(host) {
			return nil, fmt.Errorf("openshell: invalid allow host %q", host)
		}
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	sort.Strings(out)
	return out, nil
}

// validHost accepts the documented OpenShell network-endpoint host grammar: a
// DNS hostname whose labels are letters, digits, and interior hyphens, or a
// wildcard pattern. A single `*` may appear inside the first label, and `*` or
// `**` may be a whole label, but a wildcard host needs at least three labels
// (rejecting bare `*`/`**` and TLD wildcards like `*.com`), `**` only as the
// whole first label, and no wildcard in the last (TLD) label. The check is at
// least as strict as the gateway, so a malformed host fails in Build instead of
// surfacing later as an opaque sandbox-create error.
func validHost(host string) bool {
	if host == "" || strings.ContainsAny(host, " \t/\\@:#?{}") {
		return false
	}
	labels := strings.Split(host, ".")
	wildcard := false
	for i, label := range labels {
		switch {
		case label == "":
			return false
		case label == "**":
			if i != 0 {
				return false
			}
			wildcard = true
		case label == "*":
			if i == len(labels)-1 {
				return false
			}
			wildcard = true
		case strings.Contains(label, "*"):
			if i != 0 || strings.Contains(label, "**") || !validHostLabel(label, true) {
				return false
			}
			wildcard = true
		default:
			if !validHostLabel(label, false) {
				return false
			}
		}
	}
	return !wildcard || len(labels) >= 3
}

// validHostLabel reports whether label is a valid DNS label. With wildcard set,
// `*` characters are treated as ordinary label characters (validated in place,
// so a wildcard cannot sit on a hyphen edge).
func validHostLabel(label string, wildcard bool) bool {
	for i := 0; i < len(label); i++ {
		c := label[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', wildcard && c == '*':
		case c == '-':
			if i == 0 || i == len(label)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func rulesFor(hosts []string, binary string) map[string]NetworkPolicy {
	rules := make(map[string]NetworkPolicy, len(hosts))
	for _, host := range hosts {
		name := ruleName(host, allowPort)
		rules[name] = NetworkPolicy{
			Name: name,
			Endpoints: []Endpoint{{
				Host:        host,
				Port:        allowPort,
				Protocol:    "rest",
				Enforcement: "enforce",
				Access:      "read-write",
			}},
			Binaries: []Binary{{Path: binary}},
		}
	}
	return rules
}

func ruleName(host string, port int) string {
	var b strings.Builder
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return fmt.Sprintf("allow_%s_%d", b.String(), port)
}

func allowedHosts(rules map[string]NetworkPolicy) []string {
	seen := map[string]bool{}
	for _, rule := range rules {
		for _, ep := range rule.Endpoints {
			seen[ep.Host] = true
		}
	}
	out := make([]string, 0, len(seen))
	for host := range seen {
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
