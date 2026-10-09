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
		Short: "Serve the live PO dashboard, token-gated capture, and the message transport",
		Long: "Serve a local dashboard of project and task graph state, with a capture write side.\n" +
			"The read side reloads itself over Server-Sent Events as tasks change; the read pages\n" +
			"have no mutating endpoints. The app's /capture page turns a natural-language sentence\n" +
			"into a stored idea or bug, gated by a shared token (machine config serve.token, or\n" +
			"FACTOTUM_SERVE_TOKEN). With no token configured, capture is disabled and the read side\n" +
			"stays open. The message port is exposed at the same token-gated /api/msg/* so a receiver\n" +
			"on another host can message through the project backend. It binds to localhost by\n" +
			"default; pass --bind 0.0.0.0:PORT to reach it from another device on the network.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps.warnIncompleteHub()
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
				Backend:  deps.Backend,
				Clock:    deps.Clock,
				Ranker:   ranker,
				Project:  project,
				All:      serveAll,
				Token:    deps.Config.Serve.Token,
				Tasks:    deps.Tasks,
				Messages: deps.Messages,
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
			_, _ = fmt.Fprintf(deps.Err, "factotum dashboard: http://%s (reads live; %s; Ctrl-C to stop)\n", listener.Addr().String(), serveMode(serveAll, deps.Config.Serve.Token))

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

// serveMode describes the write side in the startup line: capture needs both a
// scoped project and a configured token, so all-projects serving or a missing
// token reports capture off rather than implying it is reachable.
func serveMode(allProjects bool, token string) string {
	switch {
	case allProjects:
		return "capture off (all projects)"
	case token == "":
		return "capture off (set serve.token)"
	default:
		return "capture on at /capture"
	}
}
