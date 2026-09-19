package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

func newProjectCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Manage projects"}

	var description string
	var repoSpecs []string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a project",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repos := make([]core.Repository, 0, len(repoSpecs))
			for _, spec := range repoSpecs {
				repo, err := parseRepoSpec(spec)
				if err != nil {
					return err
				}
				repos = append(repos, repo)
			}
			project, err := deps.Projects.Create(cmd.Context(), args[0], description, repos)
			if err != nil {
				return err
			}
			hints := []hint{
				{Command: fmt.Sprintf("ft project get %s", project.ID), About: "inspect the project"},
				{Command: fmt.Sprintf("ft project repo add %s <name>", project.ID), About: "register a repository"},
				{Command: fmt.Sprintf("ft task add --project %s --title \"...\"", project.ID), About: "add the first task"},
			}
			return deps.emit(project, func() { deps.printf("%s\t%s\n", project.ID, project.Name) }, hints...)
		},
	}
	create.Flags().StringVar(&description, "description", "", "project description")
	create.Flags().StringArrayVarP(&repoSpecs, "repo", "r", nil, "repository: name | name=url | name=<k>,url=,path=,brief= (repeatable)")

	list := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		RunE: func(cmd *cobra.Command, _ []string) error {
			projects, err := deps.Projects.List(cmd.Context())
			if err != nil {
				return err
			}
			return deps.emit(projects, func() {
				rows := make([][]string, 0, len(projects))
				for _, project := range projects {
					rows = append(rows, []string{string(project.ID), project.Name, strconv.Itoa(len(project.Repos))})
				}
				deps.printTable([]string{"ID", "NAME", "REPOS"}, rows)
			}, projectListHints(projects)...)
		},
	}

	get := &cobra.Command{
		Use:   "get <project>",
		Short: "Get a project",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := deps.Projects.Get(cmd.Context(), core.ProjectID(args[0]))
			if err != nil {
				return err
			}
			return deps.emit(project, func() {
				deps.printf("%s\t%s\n", project.ID, project.Name)
				if project.Description != "" {
					deps.printf("description: %s\n", project.Description)
				}
				for _, repo := range project.Repos {
					deps.printf("repo: %s\t%s\t%s\t%s\n", repo.Name, repo.Description, repo.Path, repo.URL)
				}
			}, projectShowHints(project)...)
		},
	}

	rm := &cobra.Command{
		Use:   "rm <project>",
		Short: "Delete a project",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := deps.Projects.Delete(cmd.Context(), core.ProjectID(args[0])); err != nil {
				return err
			}
			deps.suggest(hint{Command: "ft project list", About: "review the remaining projects"})
			return nil
		},
	}

	cmd.AddCommand(create, list, get, newProjectRepoCommand(deps), rm)
	return cmd
}

// parseRepoSpec parses a repository specification. Accepted forms:
//
//	backend
//	backend=git@example.com:acme/backend.git
//	name=backend,url=git@example.com:acme/backend.git,brief=API service,path=repos/backend
func parseRepoSpec(spec string) (core.Repository, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return core.Repository{}, fmt.Errorf("empty repository specification")
	}
	if !strings.Contains(spec, "=") {
		return core.Repository{Name: spec}, nil
	}

	parts := strings.Split(spec, ",")
	repo := core.Repository{}
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok {
			if repo.Name == "" {
				repo.Name = key
				continue
			}
			return core.Repository{}, fmt.Errorf("invalid repository field %q", part)
		}
		switch key {
		case "name":
			repo.Name = value
		case "url":
			repo.URL = value
		case "path":
			repo.Path = value
		case "brief", "description":
			repo.Description = value
		default:
			if len(parts) == 1 {
				repo.Name = key
				repo.URL = value
				break
			}
			return core.Repository{}, fmt.Errorf("unknown repository field %q", key)
		}
	}
	if err := repo.Validate(); err != nil {
		return core.Repository{}, err
	}
	return repo, nil
}

func newProjectRepoCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "repo", Short: "Manage the repositories a project spans"}

	var url, path, brief string
	add := &cobra.Command{
		Use:   "add <project> <name>",
		Short: "Add a repository to a project",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := core.Repository{Name: args[1], URL: url, Path: path, Description: brief}
			project, err := deps.Projects.AddRepo(cmd.Context(), core.ProjectID(args[0]), repo)
			if err != nil {
				return err
			}
			return deps.emit(project, func() { deps.printf("added\t%s\t%s\n", repo.Name, project.ID) },
				hint{Command: fmt.Sprintf("ft task add --project %s --repo %s --title \"...\"", project.ID, repo.Name), About: "add a task in this repo"},
				hint{Command: fmt.Sprintf("ft project get %s", project.ID), About: "inspect the project"})
		},
	}
	add.Flags().StringVar(&url, "url", "", "repository URL")
	add.Flags().StringVar(&path, "path", "", "local checkout path")
	add.Flags().StringVar(&brief, "brief", "", "short description of the repository's purpose")

	list := &cobra.Command{
		Use:   "list <project>",
		Short: "List a project's repositories",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := deps.Projects.Get(cmd.Context(), core.ProjectID(args[0]))
			if err != nil {
				return err
			}
			return deps.emit(project.Repos, func() {
				rows := make([][]string, 0, len(project.Repos))
				for _, repo := range project.Repos {
					rows = append(rows, []string{repo.Name, repo.Description, repo.Path, repo.URL})
				}
				deps.printTable([]string{"NAME", "BRIEF", "PATH", "URL"}, rows)
			}, projectRepoHints(project)...)
		},
	}

	rm := &cobra.Command{
		Use:   "rm <project> <name>",
		Short: "Remove a repository from a project",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := deps.Projects.RemoveRepo(cmd.Context(), core.ProjectID(args[0]), args[1])
			if err != nil {
				return err
			}
			deps.suggest(hint{Command: fmt.Sprintf("ft project get %s", project.ID), About: "see the updated project"})
			return nil
		},
	}

	cmd.AddCommand(add, list, newProjectRepoUpdateCommand(deps), rm)
	return cmd
}

func newProjectRepoUpdateCommand(deps *Deps) *cobra.Command {
	var url, path, brief string
	cmd := &cobra.Command{
		Use:   "update <project> <name>",
		Short: "Update a repository's URL, path, or brief",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := app.RepositoryUpdate{}
			if cmd.Flags().Changed("url") {
				patch.URL = &url
			}
			if cmd.Flags().Changed("path") {
				patch.Path = &path
			}
			if cmd.Flags().Changed("brief") {
				patch.Description = &brief
			}
			project, err := deps.Projects.UpdateRepo(cmd.Context(), core.ProjectID(args[0]), args[1], patch)
			if err != nil {
				return err
			}
			return deps.emit(project, func() { deps.printf("updated\t%s\t%s\n", args[1], project.ID) },
				hint{Command: fmt.Sprintf("ft project repo list %s", project.ID), About: "see all repositories"})
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "repository URL")
	cmd.Flags().StringVar(&path, "path", "", "local checkout path")
	cmd.Flags().StringVar(&brief, "brief", "", "short description of the repository's purpose")
	return cmd
}
