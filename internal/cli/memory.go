package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
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

// memoryContextEntry is one line of the project briefing `ft memory context`
// emits: enough to decide what to load, never the body. id, title, and brief
// are always present; task_id is set only when the memory is attached to a task.
type memoryContextEntry struct {
	ID    string `json:"id" yaml:"id"`
	Title string `json:"title" yaml:"title"`
	Brief string `json:"brief" yaml:"brief"`
	Task  string `json:"task_id,omitempty" yaml:"task_id,omitempty"`
}

func memoryEntryFrom(artifact *core.Artifact) memoryEntry {
	entry := memoryEntry{ID: string(artifact.ID), Title: artifact.Title, Brief: artifact.Brief, Project: string(artifact.ProjectID)}
	if artifact.TaskID != nil {
		entry.Task = string(*artifact.TaskID)
	}
	return entry
}

// requireMemory rejects an artifact of another kind, so the memory verbs never
// touch specs or docs.
func requireMemory(artifact *core.Artifact) error {
	if artifact.Kind != core.ArtifactMemory {
		return fmt.Errorf("%w: %s is a %s artifact, not memory", core.ErrInvalid, artifact.ID, artifact.Kind)
	}
	return nil
}

// memoryRelationShortlist bounds how many lexical neighbours are judged.
const memoryRelationShortlist = 8

// adviseMemoryRelation prints an advisory when a written memory supersedes or is
// strongly related to an existing one, so the author can reconcile them. It is
// best-effort: with no judge configured it prints nothing, and it never blocks
// or mutates the write.
func (d *Deps) adviseMemoryRelation(ctx context.Context, artifact *core.Artifact) {
	shortlist := d.memoryShortlist(ctx, artifact)
	if len(shortlist) == 0 {
		return
	}
	rel, err := app.NewMemoryRelationService(d.Judge).Check(ctx, artifact, shortlist)
	if err != nil {
		if !errors.Is(err, judge.ErrUnavailable) {
			d.warnf("memory relation check failed (%v)", err)
		}
		return
	}
	if note := memoryRelationNote(rel); note != "" {
		_, _ = fmt.Fprintln(d.Err, note)
	}
}

// memoryShortlist returns the project's other memory ranked by how many terms
// its title, brief, and body share with the written memory's title and brief.
// It is a token-overlap shortlist rather than Search, whose all-terms-AND
// semantics would miss the common case of a related-but-not-identical entry.
func (d *Deps) memoryShortlist(ctx context.Context, artifact *core.Artifact) []*core.Artifact {
	kind := core.ArtifactMemory
	all, err := d.Artifacts.List(ctx, store.ArtifactFilter{ProjectID: artifact.ProjectID, Kind: &kind})
	if err != nil {
		return nil
	}
	want := store.LexicalTerms(artifact.Title + " " + artifact.Brief)
	if len(want) == 0 {
		return nil
	}
	type scored struct {
		artifact *core.Artifact
		overlap  int
	}
	ranked := make([]scored, 0, len(all))
	for _, candidate := range all {
		if candidate.ID == artifact.ID {
			continue
		}
		have := store.LexicalTerms(candidate.Title + " " + candidate.Brief + " " + candidate.Body)
		if overlap := tokenOverlap(want, have); overlap > 0 {
			ranked = append(ranked, scored{candidate, overlap})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].overlap != ranked[j].overlap {
			return ranked[i].overlap > ranked[j].overlap
		}
		return ranked[i].artifact.ID < ranked[j].artifact.ID
	})
	if len(ranked) > memoryRelationShortlist {
		ranked = ranked[:memoryRelationShortlist]
	}
	out := make([]*core.Artifact, 0, len(ranked))
	for _, item := range ranked {
		out = append(out, item.artifact)
	}
	return out
}

func tokenOverlap(want, have []string) int {
	haveSet := make(map[string]bool, len(have))
	for _, token := range have {
		haveSet[token] = true
	}
	overlap := 0
	for _, token := range want {
		if haveSet[token] {
			overlap++
		}
	}
	return overlap
}

// memoryRelationNote renders the advisory for a related or superseding memory,
// or "" when there is nothing to advise. It names the actionable verbs so the
// author can reconcile the entry.
func memoryRelationNote(rel app.MemoryRelation) string {
	if rel.Candidate == nil || rel.Action == "unrelated" {
		return ""
	}
	verb := "is related to"
	if rel.Action == "supersedes" {
		verb = "supersedes"
	}
	id := string(rel.Candidate.ID)
	return fmt.Sprintf("ft: note: this memory %s %s (%q, %.2f)\n  reconcile: ft memory get %s | ft memory update %s | ft memory delete %s",
		verb, id, rel.Candidate.Title, rel.Confidence, id, id, id)
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
			if err := deps.emit(memoryDocFrom(artifact), func() {
				deps.printFields(f("memory_id", artifact.ID), f("created", true), f("kind", artifact.Kind), f("title", artifact.Title), f("project", artifact.ProjectID))
			},
				hint{Command: fmt.Sprintf("ft memory get %s", artifact.ID), About: "read it back"},
				hint{Command: fmt.Sprintf("ft memory list --project %s", artifact.ProjectID), About: "see all memory"}); err != nil {
				return err
			}
			deps.adviseMemoryRelation(cmd.Context(), artifact)
			return nil
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
				if artifact.Brief != "" {
					deps.printf("brief: %s\n", artifact.Brief)
				}
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
			if err := deps.emit(memoryDocFrom(updated), func() {
				deps.printFields(f("memory_id", updated.ID), f("updated", true), f("kind", updated.Kind), f("title", updated.Title), f("project", updated.ProjectID))
			}, memoryGetHints(updated)...); err != nil {
				return err
			}
			deps.adviseMemoryRelation(cmd.Context(), updated)
			return nil
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

	var ctxProject string
	contextCmd := &cobra.Command{
		Use:   "context",
		Short: "Brief the project's memory for agent triage",
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := deps.resolveProject(ctxProject)
			kind := core.ArtifactMemory
			artifacts, err := deps.Artifacts.List(cmd.Context(), store.ArtifactFilter{ProjectID: project, Kind: &kind})
			if err != nil {
				return err
			}
			entries := make([]memoryContextEntry, 0, len(artifacts))
			for _, artifact := range artifacts {
				entry := memoryContextEntry{ID: string(artifact.ID), Title: artifact.Title, Brief: artifact.Brief}
				if artifact.TaskID != nil {
					entry.Task = string(*artifact.TaskID)
				}
				entries = append(entries, entry)
			}
			return deps.emit(entries, func() {
				rows := make([][]string, 0, len(entries))
				for _, entry := range entries {
					rows = append(rows, []string{entry.ID, entry.Title, entry.Brief, entry.Task})
				}
				deps.printTable([]string{"ID", "TITLE", "BRIEF", "TASK"}, rows)
			}, memoryContextHints(entries)...)
		},
	}
	contextCmd.Flags().StringVarP(&ctxProject, "project", "p", "", "filter by project id")

	cmd.AddCommand(create, list, search, get, update, del, contextCmd)
	return cmd
}
