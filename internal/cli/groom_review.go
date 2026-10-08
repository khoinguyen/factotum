package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// groomReviewOptions is the per-invocation configuration of `ft groom review`.
type groomReviewOptions struct {
	verdict  string
	body     string
	bodyFile string
}

// newGroomReviewCommand records the architecture reviewer's verdict for a
// grooming session and gates the feature's build on it.
func newGroomReviewCommand(deps *Deps) *cobra.Command {
	var opts groomReviewOptions

	cmd := &cobra.Command{
		Use:   "review <session>",
		Short: "Record the architecture reviewer's verdict for a grooming session",
		Long: "Record the independent architecture review of a session's feature documents. The\n" +
			"reviewer's findings are captured as a session artifact and a note on each origin item,\n" +
			"and one tech-design verdict is stored on the session. A needs-rework verdict blocks the\n" +
			"feature from build by blocking the tasks the session produced; a later approving review\n" +
			"unblocks them.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.runGroomReview(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.verdict, "verdict", "", "tech-design verdict: "+verdictChoices())
	cmd.Flags().StringVarP(&opts.body, "body", "b", "", "the reviewer's findings")
	cmd.Flags().StringVarP(&opts.bodyFile, "body-file", "f", "", "read the reviewer's findings from a file")
	return cmd
}

func (d *Deps) runGroomReview(cmd *cobra.Command, sessionID string, opts groomReviewOptions) error {
	ctx := cmd.Context()
	verdict, err := groom.ParseReviewVerdict(opts.verdict)
	if err != nil {
		return usageError(cmd, "%v", err)
	}
	body, err := reviewBody(opts)
	if err != nil {
		return usageError(cmd, "%v", err)
	}

	dataDir, err := projectDataDir(d.Config.Store)
	if err != nil {
		return err
	}
	session, err := groom.ReadSession(dataDir, sessionID)
	if err != nil {
		return err
	}
	projectID := core.ProjectID(session.Project)

	if err := writeGroomOutput(groom.ReviewPath(dataDir, sessionID), body); err != nil {
		return err
	}
	artifact, err := addGroomReviewArtifact(ctx, d.Artifacts, projectID, session, dataDir, body)
	if err != nil {
		return err
	}
	if err := recordReviewFindings(ctx, d.Tasks, session, artifact.ID, verdict, body); err != nil {
		return err
	}

	blocked, err := d.applyReviewGate(ctx, session, verdict)
	if err != nil {
		return err
	}
	session.Review = string(artifact.ID)
	session.ReviewVerdict = string(verdict)
	session.Blocked = blocked
	if err := groom.WriteSession(dataDir, session); err != nil {
		return err
	}

	doc := groomReviewDoc{
		Session: sessionID,
		Verdict: string(verdict),
		Review:  string(artifact.ID),
		Blocked: nonNilStrings(blocked),
		Project: session.Project,
		Repo:    "",
	}
	return d.emit(doc, func() {
		d.printFields(
			f("session", sessionID),
			f("verdict", verdict),
			f("review", artifact.ID),
			f("blocked", strings.Join(blocked, ", ")),
			f("project", session.Project),
			f("repo", d.repoValue("")),
		)
	}, groomReviewHints(session, verdict)...)
}

// reviewBody resolves the reviewer's findings from --body-file or --body. The
// findings are the review, so an empty body is rejected.
func reviewBody(opts groomReviewOptions) (string, error) {
	if opts.body != "" && opts.bodyFile != "" {
		return "", fmt.Errorf("groom review: give either --body or --body-file, not both")
	}
	body := opts.body
	if opts.bodyFile != "" {
		data, err := os.ReadFile(opts.bodyFile)
		if err != nil {
			return "", fmt.Errorf("read review body %s: %w", opts.bodyFile, err)
		}
		body = string(data)
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("groom review: provide the reviewer's findings with --body or --body-file")
	}
	return body, nil
}

// addGroomReviewArtifact records the review as a doc artifact linked to the
// session directory, and to the sole item when the scope is one item.
func addGroomReviewArtifact(ctx context.Context, artifacts *app.ArtifactService, projectID core.ProjectID, session groom.SessionRecord, dataDir, body string) (*core.Artifact, error) {
	input := app.ArtifactInput{
		ProjectID: projectID,
		Kind:      core.ArtifactDoc,
		Title:     "Architecture review " + session.ID,
		Brief:     fmt.Sprintf("Independent architecture review of grooming session %s", session.ID),
		Body:      body,
		Path:      groom.ReviewPath(dataDir, session.ID),
		Links:     []core.Link{{Kind: core.LinkURL, URL: filepath.Dir(groom.ReviewPath(dataDir, session.ID)), Title: "session " + session.ID}},
	}
	if len(session.Scope) == 1 {
		id := core.TicketID(session.Scope[0].ID)
		input.TicketID = &id
	}
	return artifacts.Add(ctx, input)
}

// recordReviewFindings lands the review on each origin item as a note, so the
// idea carries the verdict and a pointer to the full findings. A scope item
// since deleted is skipped, not fatal.
func recordReviewFindings(ctx context.Context, tasks *app.TicketService, session groom.SessionRecord, reviewID core.ArtifactID, verdict groom.ReviewVerdict, body string) error {
	for _, item := range session.Scope {
		note := fmt.Sprintf("Architecture review (%s) for session %s; full review: %s.\n\n%s", verdict, session.ID, reviewID, body)
		if _, err := tasks.AddNote(ctx, core.TicketID(item.ID), app.NoteInput{Body: note, System: true}); err != nil {
			if errors.Is(err, core.ErrNotFound) {
				continue
			}
			return err
		}
	}
	return nil
}

// applyReviewGate enforces the build gate. A needs-rework verdict re-asserts the
// block on every task the session produced and returns the ids this review is
// holding blocked now; an approving verdict unblocks exactly the tasks a prior
// failing review blocked. It re-evaluates each produced id every pass, so a task
// moved out of blocked (started or reopened) is blocked again rather than
// reported blocked while the gate is silently open. A task someone else blocked
// is never claimed, and a task since deleted is dropped.
func (d *Deps) applyReviewGate(ctx context.Context, session groom.SessionRecord, verdict groom.ReviewVerdict) ([]string, error) {
	previouslyBlocked := make(map[string]bool, len(session.Blocked))
	for _, id := range session.Blocked {
		previouslyBlocked[id] = true
	}

	if verdict.BlocksBuild() {
		var blocked []string
		for _, id := range session.Produced {
			task, err := d.Tasks.Get(ctx, core.TicketID(id))
			if errors.Is(err, core.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			switch task.Status {
			case core.StatusTodo, core.StatusInProgress:
				if _, err := d.Tasks.SetStatus(ctx, task.ID, core.StatusBlocked); err != nil {
					return nil, err
				}
				blocked = append(blocked, id)
			case core.StatusBlocked:
				// Keep a block this gate set before; never claim another's.
				if previouslyBlocked[id] {
					blocked = append(blocked, id)
				}
			default:
				// Terminal or past build (done, cancelled, ready_for_review):
				// not buildable work, so the gate no longer holds it.
			}
		}
		return blocked, nil
	}

	for _, id := range session.Blocked {
		task, err := d.Tasks.Get(ctx, core.TicketID(id))
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if task.Status != core.StatusBlocked {
			continue
		}
		if _, err := d.Tasks.SetStatus(ctx, task.ID, core.StatusTodo); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// groomReviewDoc is the lossless structured shape of `ft groom review`.
type groomReviewDoc struct {
	Session string   `json:"session" yaml:"session"`
	Verdict string   `json:"verdict" yaml:"verdict"`
	Review  string   `json:"review" yaml:"review"`
	Blocked []string `json:"blocked" yaml:"blocked"`
	Project string   `json:"project" yaml:"project"`
	Repo    string   `json:"repo" yaml:"repo"`
}

func groomReviewHints(session groom.SessionRecord, verdict groom.ReviewVerdict) []hint {
	if verdict.BlocksBuild() {
		return []hint{
			{Command: fmt.Sprintf("ft groom show %s", session.ID), About: "read the session and its review"},
			{Command: fmt.Sprintf("ft groom review %s --verdict approve ...", session.ID), About: "re-review after the findings are fixed"},
		}
	}
	return []hint{
		{Command: fmt.Sprintf("ft groom show %s", session.ID), About: "read the session and its review"},
		{Command: fmt.Sprintf("ft task next -p %s", session.Project), About: "pick up the unblocked build work"},
	}
}

func verdictChoices() string {
	names := make([]string, 0, len(groom.ReviewVerdicts()))
	for _, v := range groom.ReviewVerdicts() {
		names = append(names, string(v))
	}
	return strings.Join(names, "|")
}
