package main

import (
	"errors"
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
	// The success path flushes and closes in PersistentPostRunE; on the error
	// path neither ran, so flush any buffered stdout here.
	if ferr := deps.FlushOutput(); ferr != nil && err == nil {
		err = ferr
	}
	if deps.Backend != nil {
		_ = deps.Backend.Close()
	}
	if err != nil {
		if errors.Is(err, cli.ErrUsage) {
			return 2
		}
		cli.PrintError(stderr, deps.OutputFormat, err)
		return cli.ExitCode(err)
	}
	return 0
}
