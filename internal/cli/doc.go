package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

func newDocCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "doc", Short: "Manage specs, docs, and memory"}

	var projectID, kind, title, brief, body, path, taskID string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an artifact (spec, doc, or memory)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireFlags(cmd, "title"); err != nil {
				return err
			}
			project := deps.resolveProject(projectID)
			if err := requireProject(cmd, project); err != nil {
				return err
			}
			content := body
			if path != "" {
				data, err := os.ReadFile(path)
				if err != nil {
					return fmt.Errorf("read %s: %w", path, err)
				}
				content = string(data)
			}
			input := app.ArtifactInput{
				ProjectID: project,
				Kind:      core.ArtifactKind(kind),
				Title:     title,
				Brief:     brief,
				Body:      content,
				Path:      path,
			}
			if taskID != "" {
				id := core.TaskID(taskID)
				input.TaskID = &id
			}
			artifact, err := deps.Artifacts.Add(cmd.Context(), input)
			if err != nil {
				return err
			}
			return deps.emit(artifactDocFrom(artifact), func() {
				deps.printFields(f("doc_id", artifact.ID), f("created", true), f("kind", artifact.Kind), f("title", artifact.Title), f("project", artifact.ProjectID))
			},
				hint{Command: fmt.Sprintf("ft doc list --project %s", artifact.ProjectID), About: "see all artifacts"},
				hint{Command: `ft doc search "<query>"`, About: "search titles, briefs, and bodies"})
		},
	}
	create.Flags().StringVarP(&projectID, "project", "p", "", "project id (required)")
	create.Flags().StringVarP(&kind, "kind", "k", string(core.ArtifactDoc), "artifact kind: spec, doc, or memory")
	create.Flags().StringVarP(&title, "title", "t", "", "artifact title (required)")
	create.Flags().StringVar(&brief, "brief", "", "one-line brief (what it is and when to load it)")
	create.Flags().StringVarP(&body, "body", "b", "", "inline content")
	create.Flags().StringVarP(&path, "file", "f", "", "read content from a file")
	create.Flags().StringVar(&taskID, "task", "", "attach to a task")

	var listProject, listKind string
	list := &cobra.Command{
		Use:   "list",
		Short: "List artifacts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			listProject = string(deps.resolveProject(listProject))
			filter := store.ArtifactFilter{ProjectID: core.ProjectID(listProject)}
			if listKind != "" {
				kind := core.ArtifactKind(listKind)
				filter.Kind = &kind
			}
			artifacts, err := deps.Artifacts.List(cmd.Context(), filter)
			if err != nil {
				return err
			}
			docs := make([]artifactDoc, 0, len(artifacts))
			for _, artifact := range artifacts {
				doc := artifactDocFrom(artifact)
				doc.Body = "" // the list stays lightweight; bodies load via ft doc get
				docs = append(docs, doc)
			}
			return deps.emit(docs, func() {
				rows := make([][]string, 0, len(artifacts))
				for _, artifact := range artifacts {
					rows = append(rows, []string{string(artifact.ID), string(artifact.Kind), artifact.Title, string(artifact.ProjectID)})
				}
				deps.printTable([]string{"ID", "KIND", "TITLE", "PROJECT"}, rows)
			}, docListHints(listProject)...)
		},
	}
	list.Flags().StringVarP(&listProject, "project", "p", "", "filter by project id")
	list.Flags().StringVarP(&listKind, "kind", "k", "", "filter by kind")

	var searchProject string
	var searchNoRerank bool
	search := &cobra.Command{
		Use:   "search <query>",
		Short: "Search artifact titles and bodies",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			searchProject = string(deps.resolveProject(searchProject))
			artifacts, err := deps.Artifacts.Search(cmd.Context(), store.ArtifactFilter{ProjectID: core.ProjectID(searchProject)}, args[0])
			if err != nil {
				return err
			}
			artifacts = deps.maybeRerank(cmd, args[0], artifacts, searchNoRerank)
			return deps.emit(artifacts, func() {
				rows := make([][]string, 0, len(artifacts))
				for _, artifact := range artifacts {
					rows = append(rows, []string{string(artifact.ID), string(artifact.Kind), artifact.Title, string(artifact.ProjectID)})
				}
				deps.printTable([]string{"ID", "KIND", "TITLE", "PROJECT"}, rows)
			}, docSearchHints(searchProject)...)
		},
	}
	search.Flags().StringVarP(&searchProject, "project", "p", "", "filter by project id")
	search.Flags().BoolVar(&searchNoRerank, "no-rerank", false, "keep lexical order instead of reranking by meaning")

	get := &cobra.Command{
		Use:   "get <artifact>",
		Short: "Get an artifact (spec, doc, or memory)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			artifact, err := deps.Artifacts.Get(cmd.Context(), core.ArtifactID(args[0]))
			if err != nil {
				return err
			}
			return deps.emit(artifactDocFrom(artifact), func() {
				deps.printf("(%s) %s: %s\n", artifact.Kind, artifact.ID, artifact.Title)
				if artifact.Brief != "" {
					deps.printf("brief: %s\n", artifact.Brief)
				}
				deps.printf("project: %s\n", artifact.ProjectID)
				if artifact.TaskID != nil {
					deps.printf("task: %s\n", *artifact.TaskID)
				}
				deps.printf("created_at: %s\n", artifact.CreatedAt.UTC().Format(time.RFC3339))
				deps.printf("updated_at: %s\n", artifact.UpdatedAt.UTC().Format(time.RFC3339))
				if artifact.Path != "" {
					deps.printf("path: %s\n", artifact.Path)
				}
				if artifact.Body != "" {
					deps.printf("\n=== Body ===\n%s\n", artifact.Body)
				}
				if len(artifact.Links) > 0 {
					deps.printf("\n=== Links ===\n")
					for _, link := range artifact.Links {
						deps.printf("[%s] %s\n", link.Kind, link.URL)
					}
				}
			}, docGetHints(artifact)...)
		},
	}

	cmd.AddCommand(create, list, search, get)
	return cmd
}
