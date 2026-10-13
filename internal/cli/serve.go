package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/serve"
)

// defaultServeBind keeps the dashboard on the loopback interface, so a bare `ft
// serve` is only reachable from the local machine.
const defaultServeBind = "127.0.0.1:8484"

func newServeCommand(deps *Deps) *cobra.Command {
	var bind, projectID, token string
	var all bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the live PO dashboard, token-gated capture, and the message transport",
		Long: "Serve a local dashboard of project and task graph state, with a capture write side.\n" +
			"The read side reloads itself over Server-Sent Events as tasks change; the read pages\n" +
			"have no mutating endpoints. The app's /capture page turns a natural-language sentence\n" +
			"into a stored idea or bug, gated by a shared token sent as 'Authorization: Bearer <token>'.\n" +
			"The token is resolved from --token, then FACTOTUM_SERVE_TOKEN, then serve.token in the\n" +
			"machine config; when none is set, a fresh random token is generated for each start and\n" +
			"printed to the console for you to copy (the same header authenticates /api/prompt and\n" +
			"the message port at /api/msg/*). An explicitly provided token is never printed. With no\n" +
			"token and --all, capture is disabled and the read side stays open. It binds to localhost\n" +
			"by default; pass --bind 0.0.0.0:PORT to reach it from another device on the network.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps.warnIncompleteHub()
			project := deps.resolveProject(projectID)
			serveAll := all || project == ""
			if !serveAll {
				if _, err := deps.Projects.Get(cmd.Context(), project); err != nil {
					return err
				}
			}
			explicit := deps.Config.Serve.Token
			if cmd.Flags().Changed("token") {
				explicit = token
			}
			serveToken, generated := resolveServeToken(explicit, serveAll, newServeToken)
			ranker, err := deps.Rankers.MustLookup("composite")
			if err != nil {
				return err
			}
			server, err := serve.New(serve.Options{
				Backend:   deps.Backend,
				Clock:     deps.Clock,
				Ranker:    ranker,
				Project:   project,
				All:       serveAll,
				Token:     serveToken,
				Tasks:     deps.Tasks,
				Messages:  deps.Messages,
				Artifacts: deps.Artifacts,
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
			for _, line := range serveAnnouncement(listener.Addr().String(), serveAll, serveToken, generated) {
				_, _ = fmt.Fprintln(deps.Err, line)
			}

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
	cmd.Flags().StringVar(&token, "token", "", "shared capture token (overrides serve.token and FACTOTUM_SERVE_TOKEN; never printed)")
	return cmd
}

// resolveServeToken returns the effective capture token for `ft serve` and
// whether it was minted for this run. An explicitly configured token - the
// --token flag, FACTOTUM_SERVE_TOKEN, or serve.token, which the caller folds
// into explicit - is used as-is and never printed. With none and a scoped
// project, a fresh random token is generated; serving all projects has no
// single capture target, so the token stays empty and capture stays off.
func resolveServeToken(explicit string, all bool, gen func() string) (string, bool) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, false
	}
	if all {
		return "", false
	}
	return gen(), true
}

// newServeToken mints a random capture token for one `ft serve` run. It uses
// crypto/rand, so a token is unpredictable and independent of the process clock.
func newServeToken() string { return rand.Text() }

// serveAnnouncement returns the startup lines `ft serve` writes: the dashboard
// line (with the resolved capture mode), plus a second line carrying a
// generated token so the user can copy it. An explicit token is never echoed,
// and an all-projects server prints only the dashboard line.
func serveAnnouncement(addr string, all bool, token string, generated bool) []string {
	lines := []string{fmt.Sprintf(
		"factotum dashboard: http://%s (reads live; %s; Ctrl-C to stop)",
		addr, serveMode(all, token))}
	if generated {
		lines = append(lines, fmt.Sprintf(
			"capture token: %s (generated for this run; pass --token or set serve.token to pin it)",
			token))
	}
	return lines
}

// serveMode describes the write side in the startup line: capture needs both a
// scoped project and an effective token, so all-projects serving or a missing
// token reports capture off rather than implying it is reachable. `ft serve`
// mints a random token when none is configured (scoped), so the empty-token
// branch is what `ft up` reports when it runs with no token.
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
