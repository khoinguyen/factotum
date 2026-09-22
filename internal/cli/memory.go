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
	Brief   string `json:"brief,omitempty" yaml:"brief,omitempty"`
	Project string `json:"project" yaml:"project"`
	Task    string `json:"task_id,omitempty" yaml:"task_id,omitempty"`
}

// memoryDoc is the full memory document returned by `ft memory get`.
type memoryDoc struct {
	ID        string    `json:"id" yaml:"id"`
	ProjectID string    `json:"project_id" yaml:"project_id"`
	TaskID    string    `json:"task_id,omitempty" yaml:"task_id,omitempty"`
	Title     string    `json:"title" yaml:"title"`
	Brief     string    `json:"brief,omitempty" yaml:"brief,omitempty"`
	Body      string    `json:"body,omitempty" yaml:"body,omitempty"`
	Links     []linkDoc `json:"links,omitempty" yaml:"links,omitempty"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

func memoryEntryFrom(artifact *core.Artifact) memoryEntry {
	entry := memoryEntry{ID: string(artifact.ID), Title: artifact.Title, Brief: artifact.Brief, Project: string(artifact.ProjectID)}
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
		Brief:     artifact.Brief,
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

// requireMemory rejects an artifact of another kind, so the memory verbs never
// touch specs or docs.
func requireMemory(artifact *core.Artifact) error {
	if artifact.Kind != core.ArtifactMemory {
		return fmt.Errorf("%w: %s is a %s artifact, not memory", core.ErrInvalid, artifact.ID, artifact.Kind)
	}
	return nil
}

func newMemoryCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "memory", Short: "Manage agent memory artifacts"}

	var projectID, title, brief, body, path, taskID string
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
			return deps.emit(memoryDocFrom(artifact), func() {
				deps.printFields(f("memory_id", artifact.ID), f("created", true), f("kind", artifact.Kind), f("title", artifact.Title), f("project", artifact.ProjectID))
			},
				hint{Command: fmt.Sprintf("ft memory get %s", artifact.ID), About: "read it back"},
				hint{Command: fmt.Sprintf("ft memory list --project %s", artifact.ProjectID), About: "see all memory"})
		},
	}
	create.Flags().StringVarP(&projectID, "project", "p", "", "project id (required)")
	create.Flags().StringVarP(&title, "title", "t", "", "memory title (required)")
	create.Flags().StringVar(&brief, "brief", "", "one-line brief: what this memory is and when to load it")
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
					rows = append(rows, []string{entry.ID, entry.Title, entry.Brief, entry.Project})
				}
				deps.printTable([]string{"ID", "TITLE", "BRIEF", "PROJECT"}, rows)
			}, memoryListHints(string(project))...)
		},
	}
	list.Flags().StringVarP(&listProject, "project", "p", "", "filter by project id")

	var searchProject string
	var searchNoRerank bool
	search := &cobra.Command{
		Use:   "search <query>",
		Short: "Search memory titles and bodies",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := deps.resolveProject(searchProject)
			kind := core.ArtifactMemory
			artifacts, err := deps.Artifacts.Search(cmd.Context(), store.ArtifactFilter{ProjectID: project, Kind: &kind}, args[0])
			if err != nil {
				return err
			}
			artifacts = deps.maybeRerank(cmd, args[0], artifacts, searchNoRerank)
			entries := make([]memoryEntry, 0, len(artifacts))
			for _, artifact := range artifacts {
				entries = append(entries, memoryEntryFrom(artifact))
			}
			return deps.emit(entries, func() {
				rows := make([][]string, 0, len(entries))
				for _, entry := range entries {
					rows = append(rows, []string{entry.ID, entry.Title, entry.Brief, entry.Project})
				}
				deps.printTable([]string{"ID", "TITLE", "BRIEF", "PROJECT"}, rows)
			}, memorySearchHints(artifacts, string(project))...)
		},
	}
	search.Flags().StringVarP(&searchProject, "project", "p", "", "filter by project id")
	search.Flags().BoolVar(&searchNoRerank, "no-rerank", false, "keep lexical order instead of reranking by meaning")

	get := &cobra.Command{
		Use:   "get <memory>",
		Short: "Get a memory artifact",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			artifact, err := deps.Artifacts.Get(cmd.Context(), core.ArtifactID(args[0]))
			if err != nil {
				return err
			}
			if err := requireMemory(artifact); err != nil {
				return err
			}
			return deps.emit(memoryDocFrom(artifact), func() {
				deps.printf("(memory) %s: %s\n", artifact.ID, artifact.Title)
				deps.printf("project: %s\n", artifact.ProjectID)
				if artifact.Brief != "" {
					deps.printf("brief: %s\n", artifact.Brief)
				}
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

	var updTitle, updBrief, updBody, updFile, updTask string
	update := &cobra.Command{
		Use:   "update <memory>",
		Short: "Update a memory artifact",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			artifact, err := deps.Artifacts.Get(cmd.Context(), core.ArtifactID(args[0]))
			if err != nil {
				return err
			}
			if err := requireMemory(artifact); err != nil {
				return err
			}
			patch := app.ArtifactPatch{}
			if cmd.Flags().Changed("title") {
				patch.Title = &updTitle
			}
			if cmd.Flags().Changed("brief") {
				patch.Brief = &updBrief
			}
			switch {
			case cmd.Flags().Changed("file"):
				data, err := os.ReadFile(updFile)
				if err != nil {
					return fmt.Errorf("read %s: %w", updFile, err)
				}
				content := string(data)
				patch.Body = &content
			case cmd.Flags().Changed("body"):
				patch.Body = &updBody
			}
			if cmd.Flags().Changed("task") {
				if updTask == "" {
					patch.ClearTask = true
				} else {
					taskID := core.TaskID(updTask)
					patch.TaskID = &taskID
				}
			}
			if patch.Title == nil && patch.Brief == nil && patch.Body == nil && patch.TaskID == nil && !patch.ClearTask {
				return usageError(cmd, "nothing to update; pass --title, --brief, --body/--file, or --task")
			}
			updated, err := deps.Artifacts.Update(cmd.Context(), artifact.ID, patch)
			if err != nil {
				return err
			}
			return deps.emit(memoryDocFrom(updated), func() {
				deps.printFields(f("memory_id", updated.ID), f("updated", true), f("kind", updated.Kind), f("title", updated.Title), f("project", updated.ProjectID))
			}, memoryGetHints(updated)...)
		},
	}
	update.Flags().StringVarP(&updTitle, "title", "t", "", "new title")
	update.Flags().StringVar(&updBrief, "brief", "", "new one-line brief")
	update.Flags().StringVarP(&updBody, "body", "b", "", "new inline content")
	update.Flags().StringVarP(&updFile, "file", "f", "", "read new content from a file")
	update.Flags().StringVar(&updTask, "task", "", "attach to a task (empty string detaches)")

	del := &cobra.Command{
		Use:   "delete <memory>",
		Short: "Delete a memory artifact",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			artifact, err := deps.Artifacts.Get(cmd.Context(), core.ArtifactID(args[0]))
			if err != nil {
				return err
			}
			if err := requireMemory(artifact); err != nil {
				return err
			}
			if err := deps.Artifacts.Delete(cmd.Context(), artifact.ID); err != nil {
				return err
			}
			return deps.emit(memoryDocFrom(artifact), func() {
				deps.printFields(f("memory_id", artifact.ID), f("deleted", true), f("kind", artifact.Kind), f("title", artifact.Title), f("project", artifact.ProjectID))
			}, hint{Command: fmt.Sprintf("ft memory list --project %s", artifact.ProjectID), About: "see the remaining memory"})
		},
	}

	cmd.AddCommand(create, list, search, get, update, del)
	return cmd
}
