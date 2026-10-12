package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// newQACommand is the auto-QA surface of the loop: the read the chief uses to
// dispatch a QA for a finished feature, and the command a QA agent uses to
// record its verdict. A feature is an idea or bug whose tasks all resolve.
func newQACommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "qa",
		Short: "Auto-QA: find features ready for QA and record QA verdicts",
		Long: "The auto-QA surface of the build loop. A feature (an idea or bug) is ready for QA\n" +
			"once every task produced from it is done (or cancelled); `ft qa ready` lists the ones\n" +
			"that have not passed QA yet, so the loop can dispatch a QA for each. A QA agent records\n" +
			"its verdict with `ft qa record`; the latest verdict wins, and a passing verdict that no\n" +
			"later task change invalidated is what unlocks accepting the capture.",
	}
	cmd.AddCommand(newQAReadyCommand(deps), newQARecordCommand(deps))
	return cmd
}

type qaReadyDoc struct {
	Capture string `json:"capture" yaml:"capture"`
	Kind    string `json:"kind" yaml:"kind"`
	Title   string `json:"title" yaml:"title"`
	Project string `json:"project" yaml:"project"`
	Repo    string `json:"repo" yaml:"repo"`
}

func newQAReadyCommand(deps *Deps) *cobra.Command {
	var projectID string
	cmd := &cobra.Command{
		Use:   "ready",
		Short: "List features whose every task is done and that still need QA",
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := deps.resolveProject(projectID)
			features, err := deps.QA.Ready(cmd.Context(), project)
			if err != nil {
				return err
			}
			docs := make([]qaReadyDoc, 0, len(features))
			for _, feature := range features {
				docs = append(docs, qaReadyDoc{
					Capture: string(feature.ID),
					Kind:    string(feature.Kind),
					Title:   feature.Title,
					Project: string(feature.ProjectID),
					Repo:    deps.repoValue(feature.Repo),
				})
			}
			return deps.emit(docs, func() {
				if len(docs) == 0 {
					deps.printf("no features ready for QA in project %s\n", project)
					return
				}
				rows := make([][]string, 0, len(docs))
				for _, doc := range docs {
					rows = append(rows, []string{doc.Capture, doc.Kind, doc.Title, doc.Project, doc.Repo})
				}
				deps.printTable([]string{"CAPTURE", "KIND", "TITLE", "PROJECT", "REPO"}, rows)
			})
		},
	}
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (defaults to the configured project)")
	return cmd
}

type qaRecordOptions struct {
	project  string
	verdict  string
	body     string
	bodyFile string
}

type qaRecordDoc struct {
	Capture  string `json:"capture" yaml:"capture"`
	Verdict  string `json:"verdict" yaml:"verdict"`
	Artifact string `json:"artifact" yaml:"artifact"`
	Project  string `json:"project" yaml:"project"`
	Repo     string `json:"repo" yaml:"repo"`
}

func newQARecordCommand(deps *Deps) *cobra.Command {
	var opts qaRecordOptions
	cmd := &cobra.Command{
		Use:   "record <capture>",
		Short: "Record a feature QA verdict (pass or fail)",
		Long: "Record a QA run's verdict on a capture (an idea or bug). A pass certifies the\n" +
			"feature end to end and unlocks accepting the capture; it requires every task produced\n" +
			"from the capture to be done. A fail records the findings without unlocking anything, so\n" +
			"the follow-up work gates the next attempt.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.runQARecord(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVarP(&opts.project, "project", "p", "", "project id (defaults to the configured project)")
	cmd.Flags().StringVar(&opts.verdict, "verdict", "", "QA verdict: pass or fail (required)")
	cmd.Flags().StringVarP(&opts.body, "body", "b", "", "the QA findings")
	cmd.Flags().StringVarP(&opts.bodyFile, "body-file", "f", "", "read the QA findings from a file")
	return cmd
}

func (d *Deps) runQARecord(cmd *cobra.Command, captureID string, opts qaRecordOptions) error {
	ctx := cmd.Context()
	if err := requireFlags(cmd, "verdict"); err != nil {
		return err
	}
	verdict := app.QAVerdict(opts.verdict)
	if !verdict.Valid() {
		return usageError(cmd, "unknown verdict %q; want pass or fail", opts.verdict)
	}
	findings, err := qaFindings(opts)
	if err != nil {
		return usageError(cmd, "%v", err)
	}
	capture, err := d.Tasks.Get(ctx, core.TicketID(captureID))
	if err != nil {
		return err
	}
	if project := d.resolveProject(opts.project); project != "" && capture.ProjectID != project {
		return usageError(cmd, "capture %s belongs to project %s, not %s", capture.ID, capture.ProjectID, project)
	}
	artifact, err := d.QA.Record(ctx, capture.ID, verdict, findings)
	if err != nil {
		return err
	}
	doc := qaRecordDoc{
		Capture:  string(capture.ID),
		Verdict:  string(verdict),
		Artifact: string(artifact.ID),
		Project:  string(capture.ProjectID),
		Repo:     d.repoValue(capture.Repo),
	}
	return d.emit(doc, func() {
		d.printFields(
			f("capture", doc.Capture),
			f("verdict", doc.Verdict),
			f("artifact", doc.Artifact),
			f("project", doc.Project),
			f("repo", doc.Repo),
		)
	}, qaRecordHints(capture, verdict)...)
}

// qaFindings resolves the QA findings from --body-file or --body.
func qaFindings(opts qaRecordOptions) (string, error) {
	if opts.body != "" && opts.bodyFile != "" {
		return "", fmt.Errorf("qa record: give either --body or --body-file, not both")
	}
	findings := opts.body
	if opts.bodyFile != "" {
		data, err := os.ReadFile(opts.bodyFile)
		if err != nil {
			return "", fmt.Errorf("read QA findings %s: %w", opts.bodyFile, err)
		}
		findings = string(data)
	}
	return strings.TrimSpace(findings), nil
}

func qaRecordHints(capture *core.Ticket, verdict app.QAVerdict) []hint {
	if verdict == app.QAPass {
		return []hint{
			{Command: fmt.Sprintf("ft task done %s", capture.ID), About: "accept the verified feature"},
		}
	}
	return []hint{
		{Command: fmt.Sprintf("ft task note create %s -b %q", capture.ID, "QA findings: ..."), About: "keep the findings on the feature"},
		{Command: fmt.Sprintf("ft task create -p %s -t %q", capture.ProjectID, "Fix: ..."), About: "file the follow-up the QA found"},
	}
}
