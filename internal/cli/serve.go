package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/serve"
)

// defaultServeBind keeps the dashboard on the loopback interface, so a bare `ft
// serve` is only reachable from the local machine.
const defaultServeBind = "127.0.0.1:8484"

func newServeCommand(deps *Deps) *cobra.Command {
	var bind, projectID string
	var all bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve a read-only, auto-reloading factory dashboard",
		Long: "Serve a local, read-only dashboard of project and task graph state.\n" +
			"The page reloads itself over Server-Sent Events as tasks change; it has no\n" +
			"mutating endpoints and binds to localhost by default. Pass --bind 0.0.0.0:PORT\n" +
			"to reach it from another device on the network (there is no authentication).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := deps.resolveProject(projectID)
			serveAll := all || project == ""
			if !serveAll {
				if _, err := deps.Projects.Get(cmd.Context(), project); err != nil {
					return err
				}
			}
			ranker, err := deps.Rankers.MustLookup("composite")
			if err != nil {
				return err
			}
			server, err := serve.New(serve.Options{
				Backend: deps.Backend,
				Clock:   deps.Clock,
				Ranker:  ranker,
				Project: project,
				All:     serveAll,
			})
			if err != nil {
				return err
			}
			listener, err := net.Listen("tcp", bind)
			if err != nil {
				return fmt.Errorf("serve: listen %s: %w", bind, err)
			}
			dashboard := &http.Server{Handler: server.Handler()}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			_, _ = fmt.Fprintf(deps.Err, "factotum dashboard: http://%s (read-only; Ctrl-C to stop)\n", listener.Addr().String())

			go func() {
				<-ctx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = dashboard.Shutdown(shutdownCtx)
			}()
			if err := dashboard.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&bind, "bind", defaultServeBind, "host:port to bind (loopback only by default)")
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project, else all)")
	cmd.Flags().BoolVar(&all, "all", false, "serve every registered project")
	return cmd
}
