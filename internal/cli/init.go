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
	userOnly  bool
	project   bool
	name      string
	techStack string
}

// initResult is the machine-readable shape of `ft init`.
type initResult struct {
	UserConfig    string `json:"user_config" yaml:"user_config"`
	ProjectConfig string `json:"project_config,omitempty" yaml:"project_config,omitempty"`
	Created       bool   `json:"created" yaml:"created"`
	TechStack     string `json:"tech_stack,omitempty" yaml:"tech_stack,omitempty"`
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
	var techStack string
	cmd := &cobra.Command{
		Use:   "init [project-name]",
		Short: "Set up machine and project config for first-run use",
		Long: "Initialize Factotum config, replacing hand-written files.\n\n" +
			"With no flags, create the machine-scoped config (~/.factotum/config.toml)\n" +
			"when missing, and, inside a git repository with stdin and stdout both on a\n" +
			"terminal, offer to register this directory as a project. --user-only (-u)\n" +
			"never touches project scope. --project (-p) registers this directory,\n" +
			"deriving the project id and name from the git remote or the directory name\n" +
			"unless a name is given. On a greenfield directory it asks for the tech\n" +
			"stack and records it in the project config, or takes --tech-stack when it\n" +
			"cannot prompt. Without a terminal on stdin or stdout there are no prompts:\n" +
			"ft init only creates the machine config, and ft init -p registers using\n" +
			"the derived defaults.",
		Args: cobra.MaximumNArgs(1),
		Annotations: map[string]string{
			annotationNoCallerStore: "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if userOnly && project {
				return usageError(cmd, "--user-only and --project are mutually exclusive")
			}
			if techStack != "" && !project {
				return usageError(cmd, "--tech-stack requires --project")
			}
			name := ""
			if len(args) == 1 {
				if !project {
					return usageError(cmd, "a project name requires --project")
				}
				name = args[0]
			}
			return deps.runInit(cmd, initOptions{userOnly: userOnly, project: project, name: name, techStack: techStack})
		},
	}
	cmd.Flags().BoolVarP(&userOnly, "user-only", "u", false, "initialize only the machine-scoped config")
	cmd.Flags().BoolVarP(&project, "project", "p", false, "register the current directory as a project")
	cmd.Flags().StringVar(&techStack, "tech-stack", "", "record the project's language/framework choice (non-interactive)")
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
	// The project config is written where `ft` will later read it: the caller's
	// --config, else the CWD-relative default. `ft init -p` registers the current
	// directory, so run from a subdirectory it writes .factotum/config.toml under
	// the CWD, not the git root.
	projectPath := d.ProjectConfigPath
	if projectPath == "" {
		projectPath = config.DefaultPath
	}

	interactive := d.promptsAllowed(cmd)
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
		project, techStack, created, err := d.registerProject(cmd, p, interactive, userPath, projectPath, cwd, repo, inRepo, opts)
		if err != nil {
			return err
		}
		result.ProjectConfig = projectPath
		result.TechStack = techStack
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
		if result.TechStack != "" {
			fields = append(fields, f("tech_stack", result.TechStack))
		}
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
func (d *Deps) registerProject(cmd *cobra.Command, p Prompter, interactive bool, userPath, projectPath, cwd string, repo gitRepo, inRepo bool, opts initOptions) (*core.Project, string, bool, error) {
	name, id := deriveProjectName(opts.name, cwd, repo, inRepo)
	if interactive {
		value, err := p.Input("Project id", id)
		if err != nil {
			return nil, "", false, err
		}
		id = app.Slug(value)
		value, err = p.Input("Project name", name)
		if err != nil {
			return nil, "", false, err
		}
		if strings.TrimSpace(value) != "" {
			name = strings.TrimSpace(value)
		}
	}
	if id == "" {
		return nil, "", false, usageError(cmd, "project id must contain a letter or digit")
	}
	if name == "" {
		name = id
	}

	dbFS := filepath.Join(filepath.Dir(userPath), id+".db")
	dbPath := displayPath(dbFS, d.Getenv("HOME"))
	if interactive {
		value, err := p.Input("Database path", dbPath)
		if err != nil {
			return nil, "", false, err
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
				return nil, "", false, err
			}
			if strings.TrimSpace(value) != "" {
				repoName = strings.TrimSpace(value)
			}
		}
		repos = append(repos, core.Repository{Name: repoName, URL: repo.Remote, Path: repo.Root})
	}

	techStack := strings.TrimSpace(opts.techStack)
	if techStack == "" && interactive && greenfieldDir(projectDir(cwd, repo, inRepo)) {
		value, err := p.Input("Tech stack (language/framework)", "")
		if err != nil {
			return nil, "", false, err
		}
		techStack = strings.TrimSpace(value)
	}

	// Registration is all-or-nothing: the store open is the gate (it refuses a
	// database it may not migrate), so snapshot both configs and roll them back
	// if any later step fails. A refused open then leaves no pin, no tech_stack,
	// and no [projects.<id>] entry for a project that was never created.
	snapshots := make([]fileSnapshot, 0, 2)
	for _, path := range []string{projectPath, userPath} {
		snap, err := snapshotFile(path)
		if err != nil {
			return nil, "", false, err
		}
		snapshots = append(snapshots, snap)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, snap := range snapshots {
			if err := snap.restore(); err != nil {
				_, _ = fmt.Fprintf(d.Err, "ft: restoring %s after a failed init: %v\n", snap.path, err)
			}
		}
	}()

	if _, err := config.WriteProjectConfig(projectPath, id); err != nil {
		return nil, "", false, err
	}
	if techStack != "" {
		if _, err := config.WriteProjectTechStack(projectPath, techStack); err != nil {
			return nil, "", false, err
		}
	}
	createdEntry, err := config.AddProjectEntry(userPath, id, dbPath)
	if err != nil {
		return nil, "", false, err
	}
	project, createdProject, err := d.ensureProject(cmd, id, name, repos, dbFS)
	if err != nil {
		return nil, "", false, err
	}
	committed = true
	return project, techStack, createdEntry || createdProject, nil
}

// fileSnapshot captures a config file's on-disk state so a failed init can
// restore it. A file that did not exist is captured as absent and restores to
// absent, so a refused init leaves no new config behind.
type fileSnapshot struct {
	path    string
	existed bool
	data    []byte
	mode    os.FileMode
}

// snapshotFile reads path's current state. A missing file is captured as absent
// rather than an error.
func snapshotFile(path string) (fileSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{path: path}, nil
	}
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("stat config %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("read config %s: %w", path, err)
	}
	return fileSnapshot{path: path, existed: true, data: data, mode: info.Mode().Perm()}, nil
}

// restore returns path to its captured state.
func (s fileSnapshot) restore() error {
	if !s.existed {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(s.path, s.data, s.mode)
}

// projectDir is the directory `ft init` registers and inspects for greenfield:
// the git root when there is one, else the current directory.
func projectDir(cwd string, repo gitRepo, inRepo bool) string {
	if inRepo && repo.Root != "" {
		return repo.Root
	}
	return cwd
}

// greenfieldDir reports whether dir looks like a fresh project with no source
// files yet. The test is a positive source signal, not a list of names to
// ignore: a directory is greenfield unless it holds a file with a known source
// or build-manifest extension, or a directory other than docs/ (src/ or cmd/
// may hold source). Documentation of any name and extension, dotfiles and
// docs/ are scaffolding a first-run checkout can plausibly start with:
// `gh repo create --add-readme` plus GitHub's "add a code of conduct / security
// policy" yields README, CODE_OF_CONDUCT.md, SECURITY.md, SUPPORT.md and a
// licence.
//
// A file with no extension carries no signal to match, so it is scaffolding
// too: a bare Makefile or Dockerfile is greenfield and asked for its stack
// (a fresh repo often adds a Makefile before any code), as is a bare CMake
// build, because .txt is documentation. An unrecognized documentation name is
// greenfield by default rather than a false "project already exists" answer,
// and a missing or unreadable directory is treated as greenfield, so a git root
// that does not exist locally still offers the setup question.
//
// The rule is a table, not an allowlist: a language or manifest this
// classification misses is one row in sourceExtensions.
func greenfieldDir(dir string) bool {
	if dir == "" {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if sourceEntry(entry) {
			return false
		}
	}
	return true
}

// sourceEntry reports whether entry is a real project signal: source code, a
// build manifest, or a directory that may hold either. Dotfiles such as
// .gitignore and .github are scaffolding whichever side of that line they would
// otherwise fall on.
func sourceEntry(entry os.DirEntry) bool {
	name := entry.Name()
	if strings.HasPrefix(name, ".") {
		return false
	}
	if entry.IsDir() {
		return !strings.EqualFold(name, "docs")
	}
	return sourceExtensions[filepath.Ext(strings.ToLower(name))]
}

// sourceExtensions lists the extensions that mark a checkout as holding real
// source or a build manifest. A new language or manifest is a row here; the
// documentation side needs none, because an unknown extension is scaffolding.
var sourceExtensions = map[string]bool{
	// Source languages.
	".go": true, ".py": true, ".pyx": true, ".rb": true, ".rs": true, ".java": true,
	".kt": true, ".kts": true, ".scala": true, ".groovy": true, ".clj": true,
	".cljs": true, ".cljc": true, ".edn": true, ".c": true, ".h": true, ".cc": true,
	".cpp": true, ".cxx": true, ".hpp": true, ".hh": true, ".hxx": true, ".m": true,
	".mm": true, ".swift": true, ".cs": true, ".fs": true, ".fsx": true, ".vb": true,
	".php": true, ".pl": true, ".pm": true, ".lua": true, ".r": true, ".jl": true,
	".ex": true, ".exs": true, ".erl": true, ".hrl": true, ".hs": true, ".lhs": true,
	".elm": true, ".dart": true, ".zig": true, ".nim": true, ".v": true, ".sv": true,
	".vhd": true, ".vhdl": true, ".sol": true, ".pas": true, ".ml": true, ".mli": true,
	".rkt": true, ".scm": true, ".lisp": true, ".el": true, ".raku": true, ".purs": true,
	".re": true, ".rei": true, ".d": true, ".cr": true, ".tcl": true, ".awk": true,
	".coffee": true, ".asm": true, ".s": true, ".ipynb": true, ".nix": true,
	// Web source.
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true,
	".vue": true, ".svelte": true, ".html": true, ".htm": true, ".css": true,
	".scss": true, ".sass": true, ".less": true,
	// Scripts.
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true, ".bat": true,
	// Build manifests and their lock files.
	".mod": true, ".sum": true, ".work": true, ".toml": true, ".json": true,
	".jsonc": true, ".json5": true, ".yaml": true, ".yml": true, ".xml": true,
	".lock": true, ".gradle": true, ".sbt": true, ".csproj": true, ".fsproj": true,
	".vbproj": true, ".sln": true, ".cabal": true, ".gemspec": true, ".podspec": true,
	".nimble": true, ".rockspec": true, ".bzl": true, ".bazel": true, ".mk": true,
	".cmake": true, ".build": true, ".props": true, ".targets": true, ".nuspec": true,
	// Schema and generated source definitions.
	".proto": true, ".thrift": true, ".graphql": true, ".gql": true, ".tf": true,
	".hcl": true, ".sql": true,
}

// ensureProject opens the project's store and creates the project entity when it
// is absent, so a freshly initialized project can take tasks immediately. It
// carries the caller's store options (for example `migrate=yes`) into the open,
// so the consent the guard asks for is the consent init honors; path is forced
// to the project database init just derived.
func (d *Deps) ensureProject(cmd *cobra.Command, id, name string, repos []core.Repository, dbPath string) (*core.Project, bool, error) {
	factory, err := d.StoreFactories.MustLookup("sqlite")
	if err != nil {
		return nil, false, err
	}
	options := map[string]string{}
	for key, value := range d.Config.Store.Options {
		options[key] = value
	}
	options["path"] = dbPath
	backend, err := factory(cmd.Context(), store.Config{
		Backend: "sqlite",
		Options: options,
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
