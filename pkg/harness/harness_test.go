package harness_test

import (
	"testing"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/harness/fake"
)

func TestRegistryBacksHarnesses(t *testing.T) {
	reg := harness.NewRegistry()
	if err := reg.Register("opencode", fake.New("opencode")); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	got, ok := reg.Lookup("opencode")
	if !ok {
		t.Fatal("Lookup(opencode) not found")
	}
	if got.Name() != "opencode" {
		t.Fatalf("Lookup(opencode).Name() = %q, want opencode", got.Name())
	}
	if err := reg.Register("opencode", fake.New("opencode")); err == nil {
		t.Fatal("duplicate Register() = nil, want error")
	}
}
