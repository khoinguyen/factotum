package main

import (
	"fmt"
	"io"
	"os"

	"github.com/khoinguyen/factotum/internal/cli"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/store/builtins"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	deps := cli.NewDeps(app.SystemClock{}, app.RandomIDGen{}, stdout, stderr, getenv)
	builtins.RegisterAll(deps.StoreFactories)

	root := cli.NewRoot(deps)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.Execute()
	if deps.Backend != nil {
		_ = deps.Backend.Close()
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "ft:", err)
		return 1
	}
	return 0
}
