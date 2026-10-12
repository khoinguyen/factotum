package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/internal/serve"
	"github.com/khoinguyen/factotum/internal/up"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// upOptions are the per-invocation overrides of the `ft up` daemon.
type upOptions struct {
	bind        string
	project     string
	noAutoGroom bool
	unattended  bool
}

func newUpCommand(deps *Deps) *cobra.Command {
	var opts upOptions

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Run the factory daemon: serve the dashboard and drive captures through the pipeline",
		Long: "Run the software-factory daemon: `ft serve` (the dashboard, the token-gated capture\n" +
			"write side, and the message transport) plus the controller that turns each stored\n" +
			"capture into a feature in flight. A capture stored through /api/capture starts a\n" +
			"pipeline automatically (config `up.auto_groom`, default on; disable with\n" +
			"--no-auto-groom). The controller runs each phase as a stage, stops at every human\n" +
			"gate, and reflects each transition into the store. It is project-scoped: one project\n" +
			"per process. `ft serve` is unchanged; `ft up` is additive.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return deps.runUp(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.bind, "bind", defaultServeBind, "host:port to bind (loopback only by default)")
	cmd.Flags().StringVarP(&opts.project, "project", "p", "", "project id (defaults to the configured project)")
	cmd.Flags().BoolVar(&opts.noAutoGroom, "no-auto-groom", false, "do not start a pipeline when a capture is stored")
	cmd.Flags().BoolVar(&opts.unattended, "unattended", false, "run groom and agent stages headless (overrides up.unattended)")
	cmd.AddCommand(newUpStatusCommand(deps), newUpListCommand(deps), newUpStartCommand(deps))
	return cmd
}

// runUp starts the daemon: the serve server with the controller wired to the
// capture write path, and the controller's supervisor loop.
func (d *Deps) runUp(cmd *cobra.Command, opts upOptions) error {
	ctx := cmd.Context()
	d.warnIncompleteHub()
	projectID := d.resolveProject(opts.project)
	if err := requireProject(cmd, projectID); err != nil {
		return err
	}
	target, err := d.openGroomTarget(ctx, projectID)
	if err != nil {
		return err
	}
	defer target.close()
	backend := target.backend
	tasks := app.NewTicketService(backend, d.Clock, d.IDs)
	messages := app.NewMessageService(backend, d.Clock, d.IDs)

	autoGroom := d.Config.Up.AutoGroom
	if opts.noAutoGroom {
		autoGroom = false
	}
	unattended := d.Config.Up.Unattended
	if cmd.Flags().Changed("unattended") {
		unattended = opts.unattended
	}

	// A groom session needs a harness and sandbox. With them configured, resolve
	// the selection once so a bad name fails the daemon immediately. Without
	// them the daemon still serves and captures; the first groom parks the
	// pipeline failed with a clear message instead of prompting on a goroutine.
	session, err := d.upGroomSessionFor(cmd, target, unattended)
	if err != nil {
		return err
	}
	controller, err := up.New(up.Options{
		Backend: backend,
		Clock:   d.Clock,
		IDs:     d.IDs,
		Project: projectID,
		Stages: map[core.PipelineState]up.StageRunner{
			core.PipelineGrooming: &up.GroomRunner{DataDir: target.dataDir, IDs: d.IDs, Session: session},
		},
		Logf: func(format string, args ...any) {
			_, _ = fmt.Fprintf(d.Err, "ft up: "+format+"\n", args...)
		},
	})
	if err != nil {
		return err
	}

	ranker, err := d.Rankers.MustLookup("composite")
	if err != nil {
		return err
	}
	server, err := serve.New(serve.Options{
		Backend:    backend,
		Clock:      d.Clock,
		Ranker:     ranker,
		Project:    projectID,
		Token:      d.Config.Serve.Token,
		Tasks:      tasks,
		Messages:   messages,
		AutoGroom:  autoGroom,
		Controller: controller,
	})
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", opts.bind)
	if err != nil {
		return fmt.Errorf("up: listen %s: %w", opts.bind, err)
	}

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	var worker sync.WaitGroup
	worker.Add(1)
	go func() {
		defer worker.Done()
		_ = controller.Start(runCtx)
	}()

	dashboard := &http.Server{Handler: server.Handler()}
	_, _ = fmt.Fprintf(d.Err, "factotum factory: http://%s (%s; auto-groom %s; Ctrl-C to stop)\n",
		listener.Addr().String(), serveMode(false, d.Config.Serve.Token), onOff(autoGroom))
	go func() {
		<-runCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dashboard.Shutdown(shutdownCtx)
	}()
	if err := dashboard.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Drain the controller so an interrupted stage leaves the pipeline in its
	// in-flight state (resumable) before the process exits.
	worker.Wait()
	return nil
}

// upGroomSessionFor resolves the groom stage's session function. It requires a
// configured [run] harness and sandbox, because a groom session is a real agent
// run; without them it returns a session function that fails the stage with a
// clear message, so the daemon can still serve and capture.
func (d *Deps) upGroomSessionFor(cmd *cobra.Command, target groomTarget, unattended bool) (up.SessionFunc, error) {
	if d.Config.Run.Sandbox == "" || d.Config.Run.Harness == "" {
		return func(context.Context, *core.Pipeline, string) (up.SessionResult, error) {
			return up.SessionResult{}, errors.New("up: run.sandbox and run.harness are required to groom a capture; set them in the [run] config table")
		}, nil
	}
	sel, err := d.prepareRun(cmd, runOptions{})
	if err != nil {
		return nil, err
	}
	return d.upGroomSession(cmd, target, sel, unattended), nil
}

// upGroomSession builds the groom stage's session function: it scopes a groom
// session to the pipeline's capture, runs it through the shared groom path, and
// reports the produced tasks and whether product questions were deferred.
func (d *Deps) upGroomSession(cmd *cobra.Command, target groomTarget, sel *runSelection, unattended bool) up.SessionFunc {
	return func(ctx context.Context, p *core.Pipeline, sessionID string) (up.SessionResult, error) {
		project, err := target.projects.Get(ctx, p.ProjectID)
		if err != nil {
			return up.SessionResult{}, err
		}
		capture, err := target.tasks.Get(ctx, p.CaptureID)
		if err != nil {
			return up.SessionResult{}, err
		}
		promptBytes, err := os.ReadFile(groom.PromptPath)
		if err != nil {
			return up.SessionResult{}, fmt.Errorf("read session prompt %s: %w", groom.PromptPath, err)
		}
		record, err := d.groomSession(ctx, cmd, target, project, []*core.Ticket{capture}, sessionID, promptBytes, unattended, sel, false)
		if err != nil {
			return up.SessionResult{}, err
		}
		return up.SessionResult{
			Produced: producedTicketIDs(record.Produced),
			Deferred: deferredQuestions(groom.DeferredQuestionsPath(target.dataDir, record.ID)),
			Detail:   "groom session " + record.ID,
		}, nil
	}
}

// deferredQuestions reports whether a session's deferred-questions file holds an
// open question (an unchecked box that is not the template placeholder). It
// decides the advisory groom_questions gate.
func deferredQuestions(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [ ]") && !strings.Contains(trimmed, "<question>") {
			return true
		}
	}
	return false
}

// producedTicketIDs converts a session record's produced ids into ticket ids.
func producedTicketIDs(ids []string) []core.TicketID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]core.TicketID, 0, len(ids))
	for _, id := range ids {
		out = append(out, core.TicketID(id))
	}
	return out
}

// upPipelineDoc is the lossless structured shape of a pipeline row.
type upPipelineDoc struct {
	Pipeline string `json:"pipeline" yaml:"pipeline"`
	Capture  string `json:"capture" yaml:"capture"`
	State    string `json:"state" yaml:"state"`
	Gate     string `json:"gate" yaml:"gate"`
	// Error is the reason a failed pipeline parked, so a parked pipeline is
	// diagnosed from the read surface, not only the event log.
	Error    string   `json:"error,omitempty" yaml:"error,omitempty"`
	Session  string   `json:"session,omitempty" yaml:"session,omitempty"`
	Produced []string `json:"produced,omitempty" yaml:"produced,omitempty"`
	Project  string   `json:"project" yaml:"project"`
	Repo     string   `json:"repo" yaml:"repo"`
}

func newUpStatusCommand(deps *Deps) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show each pipeline's phase and open gate",
		Long: "Show the project's pipelines: the origin capture, the phase, the open gate (when\n" +
			"one is waiting on a human), the groom session, and the tasks produced so far.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return deps.runUpStatus(cmd, projectID)
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	return cmd
}

func (d *Deps) runUpStatus(cmd *cobra.Command, projectID string) error {
	// A read: with no project resolved, fall back to the whole store (the
	// configured project's), like the other list commands.
	project := d.resolveProject(projectID)
	pipelines, err := d.upPipelines(cmd.Context(), project)
	if err != nil {
		return err
	}
	docs := make([]upPipelineDoc, 0, len(pipelines))
	for _, p := range pipelines {
		docs = append(docs, upPipelineDocOf(p))
	}
	return d.emit(docs, func() {
		if len(docs) == 0 {
			d.printf("no pipelines for project %s\n", project)
			return
		}
		for _, doc := range docs {
			fields := []field{
				f("pipeline", doc.Pipeline),
				f("capture", doc.Capture),
				f("state", doc.State),
				f("gate", gateValue(doc.Gate)),
				f("session", doc.Session),
				f("produced", strings.Join(doc.Produced, ", ")),
			}
			if doc.Error != "" {
				fields = append(fields, f("error", doc.Error))
			}
			fields = append(fields,
				f("project", doc.Project),
				f("repo", d.repoValue(doc.Repo)),
			)
			d.printFields(fields...)
			d.printf("\n")
		}
	})
}

func newUpListCommand(deps *Deps) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's pipelines",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return deps.runUpList(cmd, projectID)
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	return cmd
}

func (d *Deps) runUpList(cmd *cobra.Command, projectID string) error {
	project := d.resolveProject(projectID)
	pipelines, err := d.upPipelines(cmd.Context(), project)
	if err != nil {
		return err
	}
	docs := make([]upPipelineDoc, 0, len(pipelines))
	for _, p := range pipelines {
		docs = append(docs, upPipelineDocOf(p))
	}
	return d.emit(docs, func() {
		rows := make([][]string, 0, len(docs))
		for _, doc := range docs {
			rows = append(rows, []string{doc.Pipeline, doc.Capture, doc.State, gateValue(doc.Gate), doc.Project})
		}
		d.printTable([]string{"PIPELINE", "CAPTURE", "STATE", "GATE", "PROJECT"}, rows)
	})
}

func newUpStartCommand(deps *Deps) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "start <capture>",
		Short: "Enqueue a pipeline for a stored capture by hand",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.runUpStart(cmd, projectID, args[0])
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	return cmd
}

func (d *Deps) runUpStart(cmd *cobra.Command, projectID, captureID string) error {
	ctx := cmd.Context()
	project := d.resolveProject(projectID)
	if err := requireProject(cmd, project); err != nil {
		return err
	}
	target, err := d.openGroomTarget(ctx, project)
	if err != nil {
		return err
	}
	defer target.close()
	controller, err := up.New(up.Options{Backend: target.backend, Clock: d.Clock, IDs: d.IDs, Project: project})
	if err != nil {
		return err
	}
	capture, err := target.tasks.Get(ctx, core.TicketID(captureID))
	if err != nil {
		return err
	}
	if capture.ProjectID != project {
		return usageError(cmd, "capture %s belongs to project %s, not %s", capture.ID, capture.ProjectID, project)
	}
	pipeline, err := controller.Enqueue(ctx, capture)
	if err != nil {
		return err
	}
	doc := upPipelineDoc{Project: string(project), Repo: d.repoValue("")}
	if pipeline != nil {
		doc = upPipelineDocOf(pipeline)
		doc.Repo = d.repoValue("")
	}
	return d.emit(doc, func() {
		if pipeline == nil {
			d.printf("pipeline: (none)\ncapture: %s\nproject: %s\nrepo: %s\n", capture.ID, project, d.repoValue(""))
			return
		}
		d.printFields(
			f("pipeline", doc.Pipeline),
			f("capture", doc.Capture),
			f("state", doc.State),
			f("project", doc.Project),
			f("repo", doc.Repo),
		)
	})
}

// upPipelines lists a project's pipelines, resolving the named project's store
// (the configured one, or another registered project's), so `-p` reads the
// project it names rather than the caller's.
func (d *Deps) upPipelines(ctx context.Context, project core.ProjectID) ([]*core.Pipeline, error) {
	target, err := d.openGroomTarget(ctx, project)
	if err != nil {
		return nil, err
	}
	defer target.close()
	return target.backend.Pipelines().List(ctx, store.PipelineFilter{ProjectID: project})
}

func upPipelineDocOf(p *core.Pipeline) upPipelineDoc {
	produced := make([]string, 0, len(p.Produced))
	for _, id := range p.Produced {
		produced = append(produced, string(id))
	}
	return upPipelineDoc{
		Pipeline: string(p.ID),
		Capture:  string(p.CaptureID),
		State:    string(p.State),
		Gate:     string(p.Gate),
		Error:    p.Error,
		Session:  p.Session,
		Produced: produced,
		Project:  string(p.ProjectID),
	}
}

// gateValue renders a gate for display: a dash when none is open.
func gateValue(gate string) string {
	if gate == "" {
		return "-"
	}
	return gate
}

// onOff renders a boolean as on or off for a startup line.
func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
