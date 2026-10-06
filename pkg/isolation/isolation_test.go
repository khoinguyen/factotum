package isolation_test

import (
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/fake"
)

func TestRegistryBacksIsolationBackends(t *testing.T) {
	reg := isolation.NewRegistry()
	if err := reg.Register("mem", fake.New("mem")); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	got, ok := reg.Lookup("mem")
	if !ok {
		t.Fatal("Lookup(mem) not found")
	}
	if got.Name() != "mem" {
		t.Fatalf("Lookup(mem).Name() = %q, want mem", got.Name())
	}
	if err := reg.Register("mem", fake.New("mem")); err == nil {
		t.Fatal("duplicate Register() = nil, want error")
	}
}
