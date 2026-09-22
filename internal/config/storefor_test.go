package config

import (
	"os"
	"path/filepath"
	"testing"
)

func storeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestStoreForProjectEntry(t *testing.T) {
	path := storeConfigFile(t, `
default_project = "acme"

[projects.factotum]
db_path = "~/.factotum/factotum.db"
`)
	store, ok, err := StoreFor(path, "factotum")
	if err != nil {
		t.Fatalf("StoreFor() error = %v", err)
	}
	if !ok {
		t.Fatal("StoreFor() ok = false, want a configured store")
	}
	if store.Backend != "sqlite" {
		t.Errorf("backend = %q, want sqlite", store.Backend)
	}
	if store.Options["path"] == "" || store.Options["path"][0] == '~' {
		t.Errorf("path = %q, want an expanded path", store.Options["path"])
	}
}

func TestStoreForProjectEntryStoreTable(t *testing.T) {
	sink := filepath.Join(t.TempDir(), "sink.json")
	path := storeConfigFile(t, `
[projects.factotum.store]
backend = "jsonfile"
options = { path = "`+sink+`" }
`)
	store, ok, err := StoreFor(path, "factotum")
	if err != nil {
		t.Fatalf("StoreFor() error = %v", err)
	}
	if !ok {
		t.Fatal("StoreFor() ok = false, want a configured store")
	}
	if store.Backend != "jsonfile" || store.Options["path"] != sink {
		t.Errorf("store = %+v, want jsonfile at %s", store, sink)
	}
}

func TestStoreForFallsBackToUserStore(t *testing.T) {
	path := storeConfigFile(t, `
[store]
backend = "jsonfile"
options = { path = "/tmp/shared.json" }
`)
	store, ok, err := StoreFor(path, "factotum")
	if err != nil {
		t.Fatalf("StoreFor() error = %v", err)
	}
	if !ok {
		t.Fatal("StoreFor() ok = false, want the user-level store")
	}
	if store.Backend != "jsonfile" || store.Options["path"] != "/tmp/shared.json" {
		t.Errorf("store = %+v, want the user-level jsonfile", store)
	}
}

func TestStoreForNotConfigured(t *testing.T) {
	path := storeConfigFile(t, `
default_project = "acme"

[projects.acme]
db_path = "~/.factotum/acme.db"
`)
	_, ok, err := StoreFor(path, "factotum")
	if err != nil {
		t.Fatalf("StoreFor() error = %v", err)
	}
	if ok {
		t.Fatal("StoreFor() ok = true, want false when the project has no store")
	}
}

func TestStoreForMissingFile(t *testing.T) {
	_, ok, err := StoreFor(filepath.Join(t.TempDir(), "absent.toml"), "factotum")
	if err != nil {
		t.Fatalf("StoreFor() error = %v, want nil for a missing file", err)
	}
	if ok {
		t.Fatal("StoreFor() ok = true, want false")
	}
}

func TestStoreForExpandsProjectPlaceholder(t *testing.T) {
	path := storeConfigFile(t, `
[projects.factotum.store]
backend = "jsonfile"
options = { path = "/tmp/{project}.json" }
`)
	store, ok, err := StoreFor(path, "factotum")
	if err != nil {
		t.Fatalf("StoreFor() error = %v", err)
	}
	if !ok {
		t.Fatal("StoreFor() ok = false")
	}
	if store.Options["path"] != "/tmp/factotum.json" {
		t.Errorf("path = %q, want /tmp/factotum.json", store.Options["path"])
	}
}
