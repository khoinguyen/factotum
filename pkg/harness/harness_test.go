package harness_test

import (
	"reflect"
	"slices"
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

// TestPortIsFullyConsumed pins the port to what a production caller invokes.
// A run's completion is its process exit code (app.RunOutcome.Complete is
// exitCode == 0), so the port carries no completion hook and no harness-reported
// completion flag for `ft run` to ignore.
func TestPortIsFullyConsumed(t *testing.T) {
	t.Run("result fields", func(t *testing.T) {
		typ := reflect.TypeOf(harness.Result{})
		var got []string
		for i := 0; i < typ.NumField(); i++ {
			got = append(got, typ.Field(i).Name)
		}
		if want := []string{"Output"}; !slices.Equal(got, want) {
			t.Fatalf("harness.Result fields = %v, want %v", got, want)
		}
	})
	t.Run("harness methods", func(t *testing.T) {
		typ := reflect.TypeOf((*harness.Harness)(nil)).Elem()
		var got []string
		for i := 0; i < typ.NumMethod(); i++ {
			got = append(got, typ.Method(i).Name)
		}
		if want := []string{"Command", "Name", "Result", "Spec"}; !slices.Equal(got, want) {
			t.Fatalf("harness.Harness methods = %v, want %v", got, want)
		}
	})
}
