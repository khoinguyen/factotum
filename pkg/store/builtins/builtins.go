// Package builtins registers the built-in storage backends.
package builtins

import (
	"github.com/khoinguyen/factotum/pkg/registry"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
	"github.com/khoinguyen/factotum/pkg/store/memory"
	"github.com/khoinguyen/factotum/pkg/store/sqlite"
)

func RegisterAll(reg *registry.Registry[store.Factory]) {
	register(reg, "memory", memory.Open)
	register(reg, "jsonfile", jsonfile.Open)
	register(reg, "sqlite", sqlite.Open)
}

func register(reg *registry.Registry[store.Factory], name string, factory store.Factory) {
	if err := reg.Register(name, factory); err != nil {
		panic(err)
	}
}
