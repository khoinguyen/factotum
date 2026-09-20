package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/render"
	"github.com/khoinguyen/factotum/pkg/store"
)

func newGraphCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "graph", Short: "Render the task graph"}

	var projectID, format, layout, out, theme string
	var updates int

	renderCmd := &cobra.Command{
		Use:   "render",
		Short: "Render the DAG as summary, agent text, json, tree, html, dot, or mermaid",
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectID = string(deps.resolveProject(projectID))
			if err := requireProject(cmd, core.ProjectID(projectID)); err != nil {
				return err
			}
			snapshot, err := app.LoadSnapshot(cmd.Context(), deps.Backend, core.ProjectID(projectID), deps.Clock.Now())
			if err != nil {
				return err
			}
			ranker, err := deps.Rankers.MustLookup("composite")
			if err != nil {
				return err
			}
			events, err := deps.Backend.Events().List(cmd.Context(), store.EventFilter{
				ProjectID: core.ProjectID(projectID),
				Limit:     updates,
			})
			if err != nil {
				return err
			}
			renderer, err := deps.Renderers.MustLookup(format)
			if err != nil {
				return err
			}

			view := render.View{
				Project: snapshot.Project,
				Tasks:   snapshot.Tasks,
				Graph:   snapshot.Graph,
				Actors:  snapshot.Actors,
				Ready:   snapshot.Ready,
				Events:  events,
				Ranker:  ranker,
				Layout:  layout,
				Theme:   theme,
				Now:     deps.Clock.Now(),
			}

			writer := deps.Out
			if out != "" && out != "-" {
				file, err := os.Create(out)
				if err != nil {
					return err
				}
				defer func() { _ = file.Close() }()
				writer = file
			}
			if err := renderer.Render(cmd.Context(), writer, view); err != nil {
				return err
			}
			deps.suggest(
				hint{Command: fmt.Sprintf("ft task next --project %s", projectID), About: "see what to start"},
				hint{Command: fmt.Sprintf("ft graph render --project %s --format html --out dag.html", projectID), About: "share an interactive report"},
			)
			return nil
		},
	}
	renderCmd.Flags().StringVarP(&projectID, "project", "p", "", "project id (required)")
	renderCmd.Flags().StringVarP(&format, "format", "f", "tree", "output format: summary, agent, json, tree, html, dot, mermaid")
	renderCmd.Flags().StringVarP(&layout, "layout", "l", "tree", "graph layout: tree or waves")
	renderCmd.Flags().StringVar(&out, "out", "", "write to a file instead of stdout (- for stdout)")
	renderCmd.Flags().StringVar(&theme, "theme", "", "html theme: auto, light, or dark")
	renderCmd.Flags().IntVar(&updates, "updates", 20, "number of recent events to include in the report")

	cmd.AddCommand(renderCmd)
	return cmd
}
