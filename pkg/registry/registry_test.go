package registry

import (
	"errors"
	"testing"
)

func TestRegisterAndLookup(t *testing.T) {
	r := New[int]()
	if err := r.Register("a", 1); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	got, ok := r.Lookup("a")
	if !ok || got != 1 {
		t.Fatalf("Lookup(a) = (%d, %v), want (1, true)", got, ok)
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("Lookup(missing) ok = true, want false")
	}
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	r := New[int]()
	if err := r.Register("a", 1); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := r.Register("a", 2); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Register() error = %v, want ErrDuplicate", err)
	}
}

func TestRegisterRejectsEmptyName(t *testing.T) {
	r := New[int]()
	if err := r.Register("", 1); !errors.Is(err, ErrEmptyName) {
		t.Fatalf("Register() error = %v, want ErrEmptyName", err)
	}
}

func TestMustLookup(t *testing.T) {
	r := New[string]()
	if _, err := r.MustLookup("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MustLookup() error = %v, want ErrNotFound", err)
	}
	if err := r.Register("a", "x"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	got, err := r.MustLookup("a")
	if err != nil || got != "x" {
		t.Fatalf("MustLookup(a) = (%q, %v), want (x, nil)", got, err)
	}
}

func TestNamesAreSorted(t *testing.T) {
	r := New[int]()
	for _, name := range []string{"c", "a", "b"} {
		if err := r.Register(name, 1); err != nil {
			t.Fatalf("Register(%s) error = %v", name, err)
		}
	}
	names := r.Names()
	want := []string{"a", "b", "c"}
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", names, want)
		}
	}
}
