package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

func newGroomListCommand(deps *Deps) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List past grooming sessions",
		Long: "List the grooming sessions recorded under the project data dir, newest first, with\n" +
			"the session id, its date and mode, and the size of its scope and the number of tasks\n" +
			"it produced. Filter with --project; without one the configured project is used.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved := string(deps.resolveProject(projectID))
			dataDir, err := projectDataDir(deps.Config)
			if err != nil {
				return err
			}
			sessions, err := groom.ListSessions(dataDir)
			if err != nil {
				return err
			}
			docs := make([]groomSessionListDoc, 0, len(sessions))
			rows := make([][]string, 0, len(sessions))
			// Newest first: the store returns oldest first.
			for i := len(sessions) - 1; i >= 0; i-- {
				session := sessions[i]
				if resolved != "" && session.Project != resolved {
					continue
				}
				docs = append(docs, groomSessionListDocFrom(session))
				rows = append(rows, []string{
					session.ID,
					session.CreatedAt.UTC().Format("2006-01-02"),
					session.Mode,
					fmt.Sprintf("%d", len(session.Scope)),
					fmt.Sprintf("%d", len(session.Produced)),
				})
			}
			return deps.emit(docs, func() {
				deps.printTable([]string{"SESSION", "DATE", "MODE", "SCOPE", "PRODUCED"}, rows)
			},
				hint{Command: "ft groom show <session>", About: "read one session in full"},
				hint{Command: "ft groom", About: "start a new session"})
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "filter by project id (defaults to the configured project)")
	return cmd
}

func newGroomShowCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <session>",
		Short: "Show a grooming session's report, deferred questions, feature docs, and produced tasks",
		Long: "Print one recorded grooming session: its scope and mode, the captured report, the\n" +
			"deferred questions, the feature spec, plan, and tech design, and the tasks the session\n" +
			"produced. The bodies come from the doc artifacts `ft groom` recorded; the produced tasks\n" +
			"are read live from the graph.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := projectDataDir(deps.Config)
			if err != nil {
				return err
			}
			session, err := groom.ReadSession(dataDir, args[0])
			if err != nil {
				return err
			}
			reportBody, err := deps.sessionOutputBody(cmd.Context(), session.Report, groom.ReportPath(dataDir, session.ID))
			if err != nil {
				return err
			}
			deferredBody, err := deps.sessionOutputBody(cmd.Context(), session.Deferred, groom.DeferredQuestionsPath(dataDir, session.ID))
			if err != nil {
				return err
			}
			specBody, err := deps.optionalSessionOutputBody(cmd.Context(), session.Spec, groom.SpecPath(dataDir, session.ID))
			if err != nil {
				return err
			}
			planBody, err := deps.optionalSessionOutputBody(cmd.Context(), session.Plan, groom.PlanPath(dataDir, session.ID))
			if err != nil {
				return err
			}
			techDesignBody, err := deps.optionalSessionOutputBody(cmd.Context(), session.TechDesign, groom.TechDesignPath(dataDir, session.ID))
			if err != nil {
				return err
			}
			produced, err := deps.producedTaskDocs(cmd.Context(), session.Produced)
			if err != nil {
				return err
			}
			doc := groomSessionDoc{
				Session:        session.ID,
				Created:        session.CreatedAt.UTC().Format(time.RFC3339),
				Mode:           session.Mode,
				Project:        session.Project,
				Scope:          scopeItemIDs(session.Scope),
				Report:         session.Report,
				Deferred:       session.Deferred,
				Spec:           session.Spec,
				Plan:           session.Plan,
				TechDesign:     session.TechDesign,
				ReportBody:     reportBody,
				DeferredBody:   deferredBody,
				SpecBody:       specBody,
				PlanBody:       planBody,
				TechDesignBody: techDesignBody,
				Produced:       produced,
			}
			return deps.emit(doc, func() {
				deps.printFields(
					f("session", doc.Session),
					f("created", doc.Created),
					f("mode", doc.Mode),
					f("project", doc.Project),
					f("scope", strings.Join(doc.Scope, ", ")),
					f("report", doc.Report),
					f("deferred", doc.Deferred),
					f("spec", doc.Spec),
					f("plan", doc.Plan),
					f("tech_design", doc.TechDesign),
				)
				deps.printf("\n=== Report ===\n%s\n", strings.TrimRight(doc.ReportBody, "\n"))
				deps.printf("\n=== Deferred questions ===\n%s\n", strings.TrimRight(doc.DeferredBody, "\n"))
				deps.printf("\n=== Feature spec ===\n%s\n", strings.TrimRight(doc.SpecBody, "\n"))
				deps.printf("\n=== Feature plan ===\n%s\n", strings.TrimRight(doc.PlanBody, "\n"))
				deps.printf("\n=== Feature tech design ===\n%s\n", strings.TrimRight(doc.TechDesignBody, "\n"))
				deps.printf("\n=== Produced tasks ===\n")
				if len(doc.Produced) == 0 {
					deps.printf("(none)\n")
				} else {
					rows := make([][]string, 0, len(doc.Produced))
					for _, task := range doc.Produced {
						rows = append(rows, []string{task.TicketID, task.Kind, task.Status, task.Title})
					}
					deps.printTable([]string{"TASK", "KIND", "STATUS", "TITLE"}, rows)
				}
			}, groomShowHints(doc.Project)...)
		},
	}
	return cmd
}

// groomSessionListDoc is the lossless structured shape of `ft groom list`.
type groomSessionListDoc struct {
	Session  string   `json:"session" yaml:"session"`
	Created  string   `json:"created" yaml:"created"`
	Mode     string   `json:"mode" yaml:"mode"`
	Project  string   `json:"project" yaml:"project"`
	Scope    []string `json:"scope" yaml:"scope"`
	Produced []string `json:"produced" yaml:"produced"`
}

func groomSessionListDocFrom(session groom.SessionRecord) groomSessionListDoc {
	return groomSessionListDoc{
		Session:  session.ID,
		Created:  session.CreatedAt.UTC().Format(time.RFC3339),
		Mode:     session.Mode,
		Project:  session.Project,
		Scope:    scopeItemIDs(session.Scope),
		Produced: nonNilStrings(session.Produced),
	}
}

// nonNilStrings returns values, or an empty slice when it is nil, so JSON and
// YAML render an empty array rather than null.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// groomTaskDoc is one produced task as `ft groom show` reports it.
type groomTaskDoc struct {
	TicketID string `json:"task_id" yaml:"task_id"`
	Kind     string `json:"kind" yaml:"kind"`
	Title    string `json:"title" yaml:"title"`
	Status   string `json:"status" yaml:"status"`
}

// groomSessionDoc is the lossless structured shape of `ft groom show`.
type groomSessionDoc struct {
	Session        string         `json:"session" yaml:"session"`
	Created        string         `json:"created" yaml:"created"`
	Mode           string         `json:"mode" yaml:"mode"`
	Project        string         `json:"project" yaml:"project"`
	Scope          []string       `json:"scope" yaml:"scope"`
	Report         string         `json:"report" yaml:"report"`
	Deferred       string         `json:"deferred" yaml:"deferred"`
	Spec           string         `json:"spec" yaml:"spec"`
	Plan           string         `json:"plan" yaml:"plan"`
	TechDesign     string         `json:"tech_design" yaml:"tech_design"`
	ReportBody     string         `json:"report_body" yaml:"report_body"`
	DeferredBody   string         `json:"deferred_body" yaml:"deferred_body"`
	SpecBody       string         `json:"spec_body" yaml:"spec_body"`
	PlanBody       string         `json:"plan_body" yaml:"plan_body"`
	TechDesignBody string         `json:"tech_design_body" yaml:"tech_design_body"`
	Produced       []groomTaskDoc `json:"produced" yaml:"produced"`
}

func scopeItemIDs(items []groom.ScopeItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

// sessionOutputBody reads one captured output: the doc artifact body, falling
// back to the session file when the artifact is gone.
func (d *Deps) sessionOutputBody(ctx context.Context, artifactID, path string) (string, error) {
	if artifactID != "" {
		artifact, err := d.Artifacts.Get(ctx, core.ArtifactID(artifactID))
		if err == nil {
			return artifact.Body, nil
		}
		if !errors.Is(err, core.ErrNotFound) {
			return "", err
		}
	}
	if path == "" {
		return "", nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// optionalSessionOutputBody reads one captured feature document, returning an
// empty body when neither the artifact nor the session file exists. A session
// recorded before the feature documents existed has no artifact id and no file,
// and must still be readable.
func (d *Deps) optionalSessionOutputBody(ctx context.Context, artifactID, path string) (string, error) {
	body, err := d.sessionOutputBody(ctx, artifactID, path)
	if artifactID == "" && errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return body, err
}

// producedTaskDocs reads the produced task ids live from the graph, so a task
// renamed or completed after the session shows its current state. A task since
// deleted is reported by id alone.
func (d *Deps) producedTaskDocs(ctx context.Context, ids []string) ([]groomTaskDoc, error) {
	docs := make([]groomTaskDoc, 0, len(ids))
	for _, id := range ids {
		task, err := d.Tasks.Get(ctx, core.TicketID(id))
		if errors.Is(err, core.ErrNotFound) {
			docs = append(docs, groomTaskDoc{TicketID: id})
			continue
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, groomTaskDoc{
			TicketID: string(task.ID),
			Kind:     string(task.Kind),
			Title:    task.Title,
			Status:   string(task.Status),
		})
	}
	return docs, nil
}

// projectTaskIDs snapshots the ids in a project before a session runs, so the
// capture can tell which tasks the session produced.
func (d *Deps) projectTaskIDs(ctx context.Context, projectID core.ProjectID) (map[core.TicketID]bool, error) {
	tasks, err := d.Tasks.List(ctx, store.TicketFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	ids := make(map[core.TicketID]bool, len(tasks))
	for _, task := range tasks {
		ids[task.ID] = true
	}
	return ids, nil
}

// producedTaskIDs returns the ids present after a session that were absent
// before it, sorted for a deterministic manifest. It is a window diff, not
// authorship: a task any other writer creates in the project while the session
// runs is indistinguishable from one the session made and is also included.
// This is deliberate for a single-operator tool; if exact attribution is ever
// needed the session must record its own produced ids.
func producedTaskIDs(ctx context.Context, tasks *app.TicketService, projectID core.ProjectID, before map[core.TicketID]bool) ([]string, error) {
	after, err := tasks.List(ctx, store.TicketFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	var produced []string
	for _, task := range after {
		if !before[task.ID] {
			produced = append(produced, string(task.ID))
		}
	}
	sort.Strings(produced)
	return produced, nil
}

func groomShowHints(projectID string) []hint {
	return []hint{
		{Command: fmt.Sprintf("ft groom list -p %s", projectID), About: "see every session"},
		{Command: "ft groom", About: "start a new session"},
	}
}
