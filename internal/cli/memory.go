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

// memoryEntry is the lightweight list shape: titles without bodies, so listing
// stays cheap for an agent to scan.
type memoryEntry struct {
	ID      string `json:"id" yaml:"id"`
	Title   string `json:"title" yaml:"title"`
	Project string `json:"project" yaml:"project"`
	Task    string `json:"task_id,omitempty" yaml:"task_id,omitempty"`
}

// memoryDoc is the full memory document returned by `ft memory get`.
type memoryDoc struct {
	ID        string    `json:"id" yaml:"id"`
	ProjectID string    `json:"project_id" yaml:"project_id"`
	TaskID    string    `json:"task_id,omitempty" yaml:"task_id,omitempty"`
	Title     string    `json:"title" yaml:"title"`
	Body      string    `json:"body,omitempty" yaml:"body,omitempty"`
	Links     []linkDoc `json:"links,omitempty" yaml:"links,omitempty"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

func memoryEntryFrom(artifact *core.Artifact) memoryEntry {
	entry := memoryEntry{ID: string(artifact.ID), Title: artifact.Title, Project: string(artifact.ProjectID)}
	if artifact.TaskID != nil {
		entry.Task = string(*artifact.TaskID)
	}
	return entry
}

func memoryDocFrom(artifact *core.Artifact) memoryDoc {
	doc := memoryDoc{
		ID:        string(artifact.ID),
		ProjectID: string(artifact.ProjectID),
		Title:     artifact.Title,
		Body:      artifact.Body,
		CreatedAt: artifact.CreatedAt,
		UpdatedAt: artifact.UpdatedAt,
	}
	if artifact.TaskID != nil {
		doc.TaskID = string(*artifact.TaskID)
	}
	for _, link := range artifact.Links {
		doc.Links = append(doc.Links, linkDoc{Kind: string(link.Kind), URL: link.URL, Title: link.Title})
	}
	return doc
}

func newMemoryCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "memory", Short: "Manage agent memory artifacts"}

	var projectID, title, body, path, taskID string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a memory artifact",
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
				Kind:      core.ArtifactMemory,
				Title:     title,
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
			return deps.emit(memoryDocFrom(artifact), func() {
				deps.printFields(f("memory_id", artifact.ID), f("created", true), f("kind", artifact.Kind), f("title", artifact.Title), f("project", artifact.ProjectID))
			},
				hint{Command: fmt.Sprintf("ft memory get %s", artifact.ID), About: "read it back"},
				hint{Command: fmt.Sprintf("ft memory list --project %s", artifact.ProjectID), About: "see all memory"})
		},
	}
	create.Flags().StringVarP(&projectID, "project", "p", "", "project id (required)")
	create.Flags().StringVarP(&title, "title", "t", "", "memory title (required)")
	create.Flags().StringVarP(&body, "body", "b", "", "inline content")
	create.Flags().StringVarP(&path, "file", "f", "", "read content from a file")
	create.Flags().StringVar(&taskID, "task", "", "attach to a task")

	var listProject string
	list := &cobra.Command{
		Use:   "list",
		Short: "List memory artifacts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := deps.resolveProject(listProject)
			kind := core.ArtifactMemory
			artifacts, err := deps.Artifacts.List(cmd.Context(), store.ArtifactFilter{ProjectID: project, Kind: &kind})
			if err != nil {
				return err
			}
			entries := make([]memoryEntry, 0, len(artifacts))
			for _, artifact := range artifacts {
				entries = append(entries, memoryEntryFrom(artifact))
			}
			return deps.emit(entries, func() {
				rows := make([][]string, 0, len(entries))
				for _, entry := range entries {
					rows = append(rows, []string{entry.ID, entry.Title, entry.Project})
				}
				deps.printTable([]string{"ID", "TITLE", "PROJECT"}, rows)
			}, memoryListHints(string(project))...)
		},
	}
	list.Flags().StringVarP(&listProject, "project", "p", "", "filter by project id")

	get := &cobra.Command{
		Use:   "get <memory>",
		Short: "Get a memory artifact",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			artifact, err := deps.Artifacts.Get(cmd.Context(), core.ArtifactID(args[0]))
			if err != nil {
				return err
			}
			if artifact.Kind != core.ArtifactMemory {
				return fmt.Errorf("%w: %s is a %s artifact, not memory", core.ErrInvalid, artifact.ID, artifact.Kind)
			}
			return deps.emit(memoryDocFrom(artifact), func() {
				deps.printf("(memory) %s: %s\n", artifact.ID, artifact.Title)
				deps.printf("project: %s\n", artifact.ProjectID)
				if artifact.TaskID != nil {
					deps.printf("task: %s\n", *artifact.TaskID)
				}
				deps.printf("created_at: %s\n", artifact.CreatedAt.UTC().Format(time.RFC3339))
				deps.printf("updated_at: %s\n", artifact.UpdatedAt.UTC().Format(time.RFC3339))
				if artifact.Body != "" {
					deps.printf("\n=== Body ===\n%s\n", artifact.Body)
				}
				if len(artifact.Links) > 0 {
					deps.printf("\n=== Links ===\n")
					for _, link := range artifact.Links {
						deps.printf("[%s] %s\n", link.Kind, link.URL)
					}
				}
			}, memoryGetHints(artifact)...)
		},
	}

	cmd.AddCommand(create, list, get)
	return cmd
}
