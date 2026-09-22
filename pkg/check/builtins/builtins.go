// Package builtins registers the built-in task checks.
package builtins

import (
	"github.com/khoinguyen/factotum/pkg/check"
	"github.com/khoinguyen/factotum/pkg/check/grooming"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/registry"
)

// RegisterAll registers every built-in check over the given judge. A check that
// needs no judge ignores it, so the framework works without a key.
func RegisterAll(reg *registry.Registry[check.Check], j judge.Judge) {
	register(reg, grooming.New(j))
}

func register(reg *registry.Registry[check.Check], c check.Check) {
	if err := reg.Register(c.Name(), c); err != nil {
		panic(err)
	}
}
