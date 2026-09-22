package judge

import "fmt"

// Provider builds a Judge from provider-specific options. The judge capability does
// not know any provider by name; each provider parses its own options and reads its
// own environment variables (getenv), so adding a provider does not touch this port.
type Provider func(getenv func(string) string, options map[string]string) (Judge, error)

var providers = map[string]Provider{}

// Register adds a provider by name. It panics on an empty or duplicate name, like the
// other plugin registries in the project, so misconfiguration fails at startup.
func Register(name string, provider Provider) {
	if name == "" || provider == nil {
		panic("judge: invalid provider registration")
	}
	if _, exists := providers[name]; exists {
		panic("judge: duplicate provider " + name)
	}
	providers[name] = provider
}

// New builds the named provider from its options. An empty name means no judge is
// configured and returns Disabled; an unknown name is an error.
func New(name string, getenv func(string) string, options map[string]string) (Judge, error) {
	if name == "" {
		return Disabled{}, nil
	}
	provider, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("judge: unknown provider %q", name)
	}
	return provider(getenv, options)
}
