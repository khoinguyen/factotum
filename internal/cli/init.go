package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// initOptions is the resolved set of `ft init` flags and arguments.
type initOptions struct {
	userOnly bool
	project  bool
	name     string
}

// initResult is the machine-readable shape of `ft init`.
type initResult struct {
	UserConfig    string `json:"user_config" yaml:"user_config"`
	ProjectConfig string `json:"project_config,omitempty" yaml:"project_config,omitempty"`
	Created       bool   `json:"created" yaml:"created"`
	Project       string `json:"project,omitempty" yaml:"project,omitempty"`
	Repo          string `json:"repo,omitempty" yaml:"repo,omitempty"`
}

// gitRepo is the repository fact `ft init` derives a project from.
type gitRepo struct {
	Root   string
	Remote string
}

// name is the repository name a project takes when none is given: the remote's
// base name when there is one, else the checkout directory.
func (g gitRepo) name() string {
	if name := repoBaseName(g.Remote); name != "" {
		return name
	}
	if g.Root != "" {
		return filepath.Base(g.Root)
	}
	return ""
}

// repoBaseName extracts the repository name from a git remote URL, handling both
// scp-like (git@host:org/repo.git) and URL (https://host/org/repo.git) forms.
func repoBaseName(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	remote = strings.TrimSuffix(remote, "/")
	remote = strings.TrimSuffix(remote, ".git")
	if i := strings.LastIndexAny(remote, "/:"); i >= 0 {
		remote = remote[i+1:]
	}
	return remote
}

// Prompter drives the interactive `ft init` session. The terminal implementation
// reads answers from stdin; tests inject a scripted one, so the flow is
// deterministic and never blocks.
type Prompter interface {
	Input(label, def string) (string, error)
	Confirm(label string, def bool) (bool, error)
}

type terminalPrompter struct {
	reader *bufio.Reader
	out    io.Writer
}

func (p *terminalPrompter) Input(label, def string) (string, error) {
	if def != "" {
		_, _ = fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		_, _ = fmt.Fprintf(p.out, "%s: ", label)
	}
	line, err := p.reader.ReadString('\n')
	if err != nil && line == "" {
		return def, nil
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

func (p *terminalPrompter) Confirm(label string, def bool) (bool, error) {
	suffix := "[y/N]"
	if def {
		suffix = "[Y/n]"
	}
	_, _ = fmt.Fprintf(p.out, "%s %s ", label, suffix)
	line, err := p.reader.ReadString('\n')
	if err != nil && line == "" {
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func newInitCommand(deps *Deps) *cobra.Command {
	var userOnly, project bool
	cmd := &cobra.Command{
		Use:   "init [project-name]",
		Short: "Set up machine and project config for first-run use",
		Long: "Initialize Factotum config, replacing hand-written files.\n\n" +
			"With no flags, create the machine-scoped config (~/.factotum/config.toml)\n" +
			"when missing, and, inside a git repository on a terminal, offer to register\n" +
			"this directory as a project. --user-only (-u) never touches project scope.\n" +
			"--project (-p) registers this directory, deriving the project id and name from\n" +
			"the git remote or the directory name unless a name is given. Without a terminal\n" +
			"there are no prompts: ft init only creates the machine config, and ft init -p\n" +
			"registers using the derived defaults.",
		Args: cobra.MaximumNArgs(1),
		Annotations: map[string]string{
			annotationNoCallerStore: "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if userOnly && project {
				return usageError(cmd, "--user-only and --project are mutually exclusive")
			}
			name := ""
			if len(args) == 1 {
				if !project {
					return usageError(cmd, "a project name requires --project")
				}
				name = args[0]
			}
			return deps.runInit(cmd, initOptions{userOnly: userOnly, project: project, name: name})
		},
	}
	cmd.Flags().BoolVarP(&userOnly, "user-only", "u", false, "initialize only the machine-scoped config")
	cmd.Flags().BoolVarP(&project, "project", "p", false, "register the current directory as a project")
	return cmd
}

// runInit creates the machine config, optionally registers the current directory
// as a project, and reports what it did. It never clobbers an existing config.
func (d *Deps) runInit(cmd *cobra.Command, opts initOptions) error {
	userPath := d.UserConfigPath
	if userPath == "" {
		userPath = config.UserPath(d.Getenv)
	}
	if userPath == "" {
		return fmt.Errorf("cannot locate the machine config: set $HOME or pass --user-config")
	}
	projectPath := d.ProjectConfigPath
	if projectPath == "" {
		projectPath = config.DefaultPath
	}

	interactive := d.interactive()
	p := d.prompter(cmd)

	if opts.project && !fileExists(userPath) && interactive {
		ok, err := p.Confirm(fmt.Sprintf("No machine config at %s. Create it?", userPath), true)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("init aborted: no machine config")
		}
	}
	createdUser, err := config.EnsureMachineConfig(userPath)
	if err != nil {
		return err
	}
	result := initResult{UserConfig: userPath, Created: createdUser}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repo, inRepo := d.gitDetect(cwd)

	register := opts.project
	if !opts.userOnly && !opts.project && inRepo && interactive {
		ok, err := p.Confirm("Register this repository as a Factotum project?", true)
		if err != nil {
			return err
		}
		register = ok
	}

	var hints []hint
	switch {
	case register:
		project, created, err := d.registerProject(cmd, p, interactive, userPath, projectPath, cwd, repo, inRepo, opts.name)
		if err != nil {
			return err
		}
		result.ProjectConfig = projectPath
		result.Project = string(project.ID)
		if len(project.Repos) > 0 {
			result.Repo = project.Repos[0].Name
		}
		result.Created = result.Created || created
		hints = []hint{
			{Command: fmt.Sprintf("ft project get %s", project.ID), About: "inspect the project"},
			{Command: fmt.Sprintf("ft task create -p %s -t \"...\"", project.ID), About: "add the first task"},
		}
	case !opts.userOnly && inRepo:
		hints = []hint{{Command: "ft init -p", About: "register this repository as a project"}}
	}

	text := func() {
		fields := []field{f("user_config", result.UserConfig)}
		if result.ProjectConfig != "" {
			fields = append(fields, f("project_config", result.ProjectConfig))
		}
		fields = append(fields, f("created", result.Created))
		if result.Project != "" {
			fields = append(fields, f("project", result.Project))
		}
		if result.Repo != "" {
			fields = append(fields, f("repo", result.Repo))
		}
		d.printFields(fields...)
	}
	return d.emit(result, text, hints...)
}

// registerProject writes the machine and project config for a project and makes
// sure the project entity exists in its store. It prompts only when interactive.
func (d *Deps) registerProject(cmd *cobra.Command, p Prompter, interactive bool, userPath, projectPath, cwd string, repo gitRepo, inRepo bool, given string) (*core.Project, bool, error) {
	name, id := deriveProjectName(given, cwd, repo, inRepo)
	if interactive {
		value, err := p.Input("Project id", id)
		if err != nil {
			return nil, false, err
		}
		id = app.Slug(value)
		value, err = p.Input("Project name", name)
		if err != nil {
			return nil, false, err
		}
		if strings.TrimSpace(value) != "" {
			name = strings.TrimSpace(value)
		}
	}
	if id == "" {
		return nil, false, usageError(cmd, "project id must contain a letter or digit")
	}
	if name == "" {
		name = id
	}

	dbFS := filepath.Join(filepath.Dir(userPath), id+".db")
	dbPath := displayPath(dbFS, d.Getenv("HOME"))
	if interactive {
		value, err := p.Input("Database path", dbPath)
		if err != nil {
			return nil, false, err
		}
		if strings.TrimSpace(value) != "" {
			dbPath = strings.TrimSpace(value)
			dbFS = config.ExpandPath(dbPath)
		}
	}

	var repos []core.Repository
	if inRepo {
		repoName := repo.name()
		if interactive {
			value, err := p.Input("Repository name", repoName)
			if err != nil {
				return nil, false, err
			}
			if strings.TrimSpace(value) != "" {
				repoName = strings.TrimSpace(value)
			}
		}
		repos = append(repos, core.Repository{Name: repoName, URL: repo.Remote, Path: repo.Root})
	}

	if _, err := config.WriteProjectConfig(projectPath, id); err != nil {
		return nil, false, err
	}
	createdEntry, err := config.AddProjectEntry(userPath, id, dbPath)
	if err != nil {
		return nil, false, err
	}
	project, createdProject, err := d.ensureProject(cmd, id, name, repos, dbFS)
	if err != nil {
		return nil, false, err
	}
	return project, createdEntry || createdProject, nil
}

// ensureProject opens the project's store and creates the project entity when it
// is absent, so a freshly initialized project can take tasks immediately.
func (d *Deps) ensureProject(cmd *cobra.Command, id, name string, repos []core.Repository, dbPath string) (*core.Project, bool, error) {
	factory, err := d.StoreFactories.MustLookup("sqlite")
	if err != nil {
		return nil, false, err
	}
	backend, err := factory(cmd.Context(), store.Config{
		Backend: "sqlite",
		Options: map[string]string{"path": dbPath},
		Noticef: func(format string, args ...any) {
			_, _ = fmt.Fprintf(d.Err, "ft: "+format+"\n", args...)
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("open project store %s: %w", dbPath, err)
	}
	defer func() { _ = backend.Close() }()

	service := app.NewProjectService(backend, d.Clock, d.IDs)
	project, err := service.Get(cmd.Context(), core.ProjectID(id))
	if err == nil {
		return project, false, nil
	}
	if !errors.Is(err, core.ErrNotFound) {
		return nil, false, err
	}
	project, err = service.CreateWithID(cmd.Context(), core.ProjectID(id), name, "", repos)
	if err != nil {
		return nil, false, err
	}
	return project, true, nil
}

// deriveProjectName returns the default project name and id: an explicit name
// wins, then the git repository, then the current directory.
func deriveProjectName(given, cwd string, repo gitRepo, inRepo bool) (name, id string) {
	switch {
	case strings.TrimSpace(given) != "":
		name = strings.TrimSpace(given)
	case inRepo:
		name = repo.name()
	default:
		name = filepath.Base(cwd)
	}
	return name, app.Slug(name)
}

// displayPath renders an absolute path with ~ when it is under home, so a
// machine-local database path stays portable across machines.
func displayPath(path, home string) string {
	if home == "" {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return path
	}
	return filepath.ToSlash(filepath.Join("~", rel))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// interactive reports whether `ft init` may prompt: a terminal on stdout, or an
// injected prompter in tests.
func (d *Deps) interactive() bool {
	if d.Prompt != nil {
		return true
	}
	return d.IsTerminal != nil && d.IsTerminal(d.Out)
}

func (d *Deps) prompter(cmd *cobra.Command) Prompter {
	if d.Prompt != nil {
		return d.Prompt
	}
	return &terminalPrompter{reader: bufio.NewReader(cmd.InOrStdin()), out: d.Err}
}

func (d *Deps) gitDetect(dir string) (gitRepo, bool) {
	if d.GitDetect != nil {
		return d.GitDetect(dir)
	}
	return detectGitRepo(dir)
}

// detectGitRepo probes a directory with the git CLI. A missing git binary or a
// directory that is not a repository reports ok=false.
func detectGitRepo(dir string) (gitRepo, bool) {
	root, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(root) == "" {
		return gitRepo{}, false
	}
	remote, _ := gitOutput(dir, "config", "--get", "remote.origin.url")
	return gitRepo{Root: strings.TrimSpace(root), Remote: strings.TrimSpace(remote)}, true
}

func gitOutput(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}
