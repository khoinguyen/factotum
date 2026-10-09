package openshell_test

import (
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation/openshell"
)

// TestProviderHost pins the checked-in provider-to-host map: a provider with a
// known OpenShell profile resolves to the host that profile authorizes, and an
// unknown provider has no host (so the caller falls back to the provider id).
func TestProviderHost(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{"openrouter", "openrouter", "openrouter.ai"},
		{"openai", "openai", "api.openai.com"},
		{"unknown provider", "acme", ""},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := openshell.ProviderHost(tc.provider); got != tc.want {
				t.Errorf("ProviderHost(%q) = %q, want %q", tc.provider, got, tc.want)
			}
		})
	}
}
