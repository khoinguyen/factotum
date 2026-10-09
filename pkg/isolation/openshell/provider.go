package openshell

// providerHosts is ft's checked-in map of OpenShell provider profile to the
// egress host that profile authorizes. The entries mirror the inference
// providers the pinned catalog (DefaultProfileCatalog) declares, so ft can name
// the host a run reaches without querying a gateway; the warning that reports a
// deny-all run uses it instead of echoing only the provider id. A provider
// absent here has no known host, so a caller falls back to the provider id
// rather than guessing one.
var providerHosts = map[string]string{
	"anthropic":  "api.anthropic.com",
	"deepinfra":  "api.deepinfra.com",
	"nvidia":     "integrate.api.nvidia.com",
	"openai":     "api.openai.com",
	"openrouter": "openrouter.ai",
}

// ProviderHost returns the egress host the named OpenShell provider profile
// authorizes, or "" when ft has no checked-in mapping for the provider type.
func ProviderHost(providerType string) string {
	return providerHosts[providerType]
}
