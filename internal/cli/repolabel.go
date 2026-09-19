package cli

import (
	"net/url"
	"strings"
)

// knownProviders maps a git host's first label to the short provider name used
// in display, so https://gitlab.com/org/repo becomes gitlab:org/repo.
var knownProviders = map[string]bool{
	"github":    true,
	"gitlab":    true,
	"bitbucket": true,
	"codeberg":  true,
	"gitea":     true,
}

// providerDomains maps a short provider name back to its public host, so an
// already-shortened github:org/repo is recognised and can be opened.
var providerDomains = map[string]string{
	"github":    "github.com",
	"gitlab":    "gitlab.com",
	"bitbucket": "bitbucket.org",
	"codeberg":  "codeberg.org",
}

// shortRepo renders a repository reference for display:
//
//   - a remote URL becomes "<provider>:<org/repo>", e.g. github:khoinguyen/factotum;
//   - a local checkout path becomes "Local";
//   - an empty value becomes "-".
//
// Plain names ("backend", "org/repo") are returned unchanged.
func shortRepo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	if host, path, ok := parseRemote(value); ok {
		return providerName(host) + ":" + path
	}
	if isLocalPath(value) {
		return "Local"
	}
	return value
}

// repoURL returns the browsable https URL for a remote reference, or "" when
// the value is not a remote URL.
func repoURL(value string) string {
	host, path, ok := parseRemote(strings.TrimSpace(value))
	if !ok {
		return ""
	}
	return "https://" + host + "/" + path
}

// repoCell renders the display value for a repository that stores its remote
// URL and local path separately.
func repoCell(remote, path string) string {
	if strings.TrimSpace(remote) != "" {
		return shortRepo(remote)
	}
	if strings.TrimSpace(path) != "" {
		return "Local"
	}
	return "-"
}

// parseRemote extracts the host and "org/repo" path from a git remote, handling
// both scheme URLs and scp-like syntax (git@github.com:org/repo.git).
func parseRemote(value string) (host, path string, ok bool) {
	if value == "" {
		return "", "", false
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		switch parsed.Scheme {
		case "http", "https", "ssh", "git":
			host = parsed.Hostname()
			path = cleanRepoPath(parsed.Path)
			return host, path, host != "" && path != ""
		}
	}
	// scp-like "[user@]host:org/repo(.git)", or an already-short
	// "provider:org/repo".
	if at := strings.LastIndex(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	colon := strings.Index(value, ":")
	if colon <= 0 {
		return "", "", false
	}
	host = value[:colon]
	path = cleanRepoPath(value[colon+1:])
	if path == "" {
		return "", "", false
	}
	if domain, ok := providerDomains[strings.ToLower(host)]; ok {
		return domain, path, true
	}
	if !strings.Contains(host, ".") {
		return "", "", false
	}
	return host, path, true
}

func cleanRepoPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	return strings.TrimSuffix(path, ".git")
}

func providerName(host string) string {
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	first := host
	if i := strings.IndexByte(host, '.'); i >= 0 {
		first = host[:i]
	}
	if knownProviders[first] {
		return first
	}
	return host
}

func isLocalPath(value string) bool {
	for _, prefix := range []string{"/", "./", "../", "~"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
