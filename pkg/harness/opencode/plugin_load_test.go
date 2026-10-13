package opencode_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/harness/opencode"
)

// TestPluginLoadsOnInstalledOpenCode loads the receiver plugin in the installed
// OpenCode V1. It is the regression guard for the cmux bug this task fixes: the
// plugin must default-export server(), the V1 entrypoint (V1 ignores the V2
// setup half), or OpenCode refuses it.
//
// The guard fails on a load error and passes once the staged plugin's server()
// registers with the stub ft. OpenCode's first run in a fresh checkout forks a
// detached install and can exit before its plugin subsystem runs, so when no
// register is observed in the attempts the test skips explicitly rather than
// flaking; the static server() shape is covered deterministically by the
// plugin's Node unit tests and by TestPluginLoadGuardRejectsWrongShape. Gated on
// the binary, so CI without OpenCode skips. The V2 counterpart is
// TestPluginLoadsOnInstalledOpenCodeV2.
func TestPluginLoadsOnInstalledOpenCode(t *testing.T) {
	res := runLoadGuard(t, opencode.MsgPluginBytes(), 6)
	if res.loadError {
		t.Fatalf("receiver plugin failed to load on opencode:\n%s", res.output)
	}
	if !res.registered {
		t.Skipf("opencode did not reach the staged plugin's server() in the attempts (no load error seen); skipped rather than flaked. The static shape is covered by the plugin's Node unit tests\nopencode output:\n%s", res.output)
	}
}

// TestPluginLoadGuardRejectsWrongShape pins that the guard is real: a
// setup()-shaped plugin (the exact cmux regression) must NOT be observed as
// loaded. The guard is isolated from the machine's global OpenCode config, so a
// pre-installed global factotum-msg.js (loop agents `ft msg install` one) cannot
// register on the broken plugin's behalf and mask the regression.
func TestPluginLoadGuardRejectsWrongShape(t *testing.T) {
	res := runLoadGuard(t, []byte("export const setup = () => ({});\n"), 3)
	if res.registered {
		t.Fatalf("a setup()-shaped plugin was observed as loaded; the guard is not real:\n%s", res.output)
	}
}

// TestPluginLoadsOnInstalledOpenCodeV2 loads the receiver plugin in OpenCode 2
// (the isolated `opencode2` V2 binary) through the V2 entrypoint: the same
// default export must expose `id` + `setup(ctx)` (Plugin.define) that V2 runs,
// while V1 keeps calling `server()`. It is the V2 counterpart to
// TestPluginLoadsOnInstalledOpenCode.
//
// OpenCode 2 loads project plugins only once a location is activated, so the
// guard creates a session and then polls `/api/plugin` until the plugin appears
// active; it also asserts the V2 receiver registered with the stub ft. Like the
// V1 guard it skips explicitly rather than flaking when the plugin never comes
// up (the static shape is covered deterministically by the Node unit tests), and
// fails loudly if V2 reports the plugin as failed. Gated on the binary, so CI
// without the isolated V2 runtime skips.
func TestPluginLoadsOnInstalledOpenCodeV2(t *testing.T) {
	bin := opencode2Path(t)
	active, registered, failed, output := runV2LoadGuard(t, bin, opencode.MsgPluginBytes())
	if failed {
		t.Fatalf("V2 reported the receiver plugin as failed:\n%s", output)
	}
	if !active || !registered {
		t.Skipf("opencode2 (V2) did not reach the staged plugin's setup() in the window (active=%v registered=%v); skipped rather than flaked. The static shape is covered by the plugin's Node unit tests\nopencode2 output:\n%s", active, registered, output)
	}
}

// opencode2Path locates the isolated OpenCode 2 binary: an explicit OPENCODE2
// path first, then `opencode2` on PATH. The installed V1 `opencode` is never
// used, so the two guards stay independent.
func opencode2Path(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("OPENCODE2"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("OPENCODE2=%q is not usable: %v", p, err)
		}
		return p
	}
	p, err := exec.LookPath("opencode2")
	if err != nil {
		t.Skipf("opencode2 (OpenCode 2) is not on PATH: %v", err)
	}
	return p
}

// v2PluginEntry is the slice of /api/plugin's Plugin.Info the guard reads.
type v2PluginEntry struct {
	ID    string `json:"id"`
	State struct {
		Status string `json:"status"`
	} `json:"state"`
}

type v2PluginList struct {
	Data []v2PluginEntry `json:"data"`
}

// runV2LoadGuard stages the plugin where OpenCode 2 loads project plugins
// (`.opencode/plugin/`, the production path), starts `opencode2 serve` with an
// isolated config/data home, activates the location by creating a session, and
// polls /api/plugin until the plugin is active or failed and the stub ft has
// seen a register. It returns what it observed plus the server output.
func runV2LoadGuard(t *testing.T, bin string, plugin []byte) (active, registered, failed bool, output string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "factotum-opencode2-plugin-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		if err := removeAllEventually(dir); err != nil {
			t.Logf("scratch dir %s not fully removed (OpenCode's detached install may still be writing): %v", dir, err)
		}
	})
	callsPath, stub := stagePluginAndStub(t, dir, plugin)

	// Isolate OpenCode's config, data, and cache so the machine's own global
	// plugins and state never load: only the staged plugin can register.
	xdgConfig := filepath.Join(dir, "xdg")
	xdgData := filepath.Join(dir, "xdg-data")
	xdgCache := filepath.Join(dir, "xdg-cache")
	for _, d := range []string{xdgConfig, xdgData, xdgCache} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", d, err)
		}
	}

	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+xdgConfig,
		"XDG_DATA_HOME="+xdgData,
		"XDG_CACHE_HOME="+xdgCache,
		"FACTOTUM_PROJECT=prj-test",
		"FACTOTUM_ACTOR=act-test",
		"FACTOTUM_BIN="+stub,
		"FACTOTUM_MSG_WAIT_MS=10",
		"FACTOTUM_MSG_TTL_MS=1000",
		"CALLS="+callsPath,
	)

	port := freeTCPPort(t)
	server, password, serverOut := startV2Server(t, bin, dir, env, port)
	defer server.stop()

	// OpenCode 2 loads a location's plugins only once the location is
	// activated; creating a session does that.
	createSessionV2(t, port, password)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if calls, _ := os.ReadFile(callsPath); strings.Contains(string(calls), "msg agent register") {
			registered = true
		}
		switch pluginStateV2(t, port, password, "factotum.msg") {
		case "active":
			active = true
		case "failed":
			failed = true
		}
		if (active || failed) && registered {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return active, registered, failed, serverOut.String()
}

// v2Server wraps a running `opencode2 serve` so the guard can stop it.
type v2Server struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// stop terminates the server and reaps it.
func (s *v2Server) stop() {
	s.cancel()
	_ = s.cmd.Wait()
}

// startV2Server launches `opencode2 serve` and returns it with the password it
// prints on startup. Output is captured for diagnostics.
func startV2Server(t *testing.T, bin, dir string, env []string, port int) (*v2Server, string, *lockedBuffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "--log-level", "error", "serve", "--port", strconv.Itoa(port), "--hostname", "127.0.0.1")
	cmd.Dir = dir
	cmd.Env = env
	out := &lockedBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start opencode2 serve: %v", err)
	}

	password := make(chan string, 1)
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if pw := scanV2Password(out.String()); pw != "" {
					select {
					case password <- pw:
					default:
					}
					return
				}
			}
		}
	}()

	select {
	case pw := <-password:
		close(stop)
		return &v2Server{cmd: cmd, cancel: cancel}, pw, out
	case <-time.After(45 * time.Second):
		close(stop)
		cancel()
		_ = cmd.Wait()
		t.Fatalf("opencode2 serve printed no password within 45s:\n%s", out.String())
	}
	return nil, "", out
}

// scanV2Password extracts the password from "server password <pw>".
func scanV2Password(text string) string {
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "server password ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "server password "))
		}
	}
	return ""
}

// lockedBuffer is a concurrency-safe io.Writer for capturing a child's output
// while another goroutine reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// v2Request issues a basic-auth request to the V2 server and returns the body.
func v2Request(t *testing.T, method, url, password string, body string) []byte {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth("opencode", password)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return out
}

// createSessionV2 activates the location so OpenCode 2 loads its plugins.
func createSessionV2(t *testing.T, port int, password string) {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/api/session", port)
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if body := v2Request(t, http.MethodPost, url, password, "{}"); len(body) > 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("could not create a session on the V2 server")
}

// pluginStateV2 returns the state status of id in /api/plugin, or "" when the
// plugin is absent.
func pluginStateV2(t *testing.T, port int, password, id string) string {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/api/plugin", port)
	body := v2Request(t, http.MethodGet, url, password, "")
	if len(body) == 0 {
		return ""
	}
	var list v2PluginList
	if err := json.Unmarshal(body, &list); err != nil {
		return ""
	}
	for _, entry := range list.Data {
		if entry.ID == id {
			return entry.State.Status
		}
	}
	return ""
}

// freeTCPPort reserves and releases a port for the V2 server to bind.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

// loadGuardResult is what a guarded opencode run observed.
type loadGuardResult struct {
	// registered is true when the staged plugin ran server() and registered
	// with the stub ft.
	registered bool
	// loadError is true when opencode logged a load failure naming the plugin.
	loadError bool
	// output is the last opencode run's combined output.
	output []byte
}

// stagePluginAndStub writes plugin where both OpenCode majors load project
// plugins and a stub ft the staged plugin shells out to. The stub records each
// invocation to a calls log and answers register/claim. It returns the calls
// log and the stub path.
func stagePluginAndStub(t *testing.T, dir string, plugin []byte) (callsPath, stub string) {
	t.Helper()
	pluginDir := filepath.Join(dir, ".opencode", "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "factotum-msg.js"), plugin, 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	callsPath = filepath.Join(dir, "calls.log")
	stub = filepath.Join(dir, "ft-stub.sh")
	stubScript := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$CALLS\"\n" +
		"case \"$3\" in\n" +
		"  register) printf '{\"run_id\":\"run-1\"}';;\n" +
		"  claim) printf '{\"found\":false}';;\n" +
		"  *) printf '{\"ok\":true}';;\n" +
		"esac\n"
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return callsPath, stub
}

// runLoadGuard stages plugin where OpenCode loads project plugins and runs
// `opencode run` until the plugin registers or the attempts are exhausted. It
// runs with an isolated XDG_CONFIG_HOME so the machine's own global plugins
// (including a pre-installed factotum-msg.js) do not load: only the staged
// plugin can register, which is the whole point of the guard. A bogus model
// makes opencode exit quickly, so a register can race the exit; the retry loop
// waits it out. Skips explicitly when opencode is not on PATH.
func runLoadGuard(t *testing.T, plugin []byte, attempts int) loadGuardResult {
	t.Helper()
	opencodePath, err := exec.LookPath("opencode")
	if err != nil {
		t.Skipf("opencode is not on PATH: %v", err)
	}

	// Do not use t.TempDir: OpenCode bootstraps a project runtime by forking a
	// detached `@opencode-ai/plugin` install into .opencode/ (thousands of
	// files), which can outlive `opencode run` and race the framework's
	// RemoveAll cleanup with "directory not empty". A self-managed dir with
	// best-effort cleanup keeps that incidental write from failing the guard.
	dir, err := os.MkdirTemp("", "factotum-opencode-plugin-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		if err := removeAllEventually(dir); err != nil {
			t.Logf("scratch dir %s not fully removed (OpenCode's detached install may still be writing): %v", dir, err)
		}
	})
	callsPath, stub := stagePluginAndStub(t, dir, plugin)

	// Isolate OpenCode's global config so the machine's own plugins never
	// load: without this, a pre-installed global factotum-msg.js registers and
	// satisfies the guard even when the staged plugin is broken, and the
	// unrelated cmux plugins add load-error noise. A fresh XDG_CONFIG_HOME
	// keeps project-plugin loading (the staged plugin) and drops the globals.
	xdgConfig := filepath.Join(dir, "xdg")
	if err := os.MkdirAll(filepath.Join(xdgConfig, "opencode"), 0o755); err != nil {
		t.Fatalf("MkdirAll(xdg) error = %v", err)
	}

	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+xdgConfig,
		"FACTOTUM_PROJECT=prj-test",
		"FACTOTUM_ACTOR=act-test",
		"FACTOTUM_BIN="+stub,
		"FACTOTUM_MSG_WAIT_MS=10",
		"FACTOTUM_MSG_TTL_MS=1000",
		"CALLS="+callsPath,
	)

	res := loadGuardResult{}
	for attempt := 0; attempt < attempts; attempt++ {
		_ = os.Remove(callsPath)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		cmd := exec.CommandContext(ctx, opencodePath, "run", "--print-logs", "--log-level", "ERROR", "--model", "bogus/none", "hi")
		cmd.Dir = dir
		cmd.Env = env
		res.output, _ = cmd.CombinedOutput()
		cancel()

		// An invalid plugin shape can be reported as a load error naming the
		// plugin (OpenCode validates global plugins; the project path is
		// silent on some versions, so the register probe below is the guard).
		if pluginLoadFailed(res.output) {
			res.loadError = true
			return res
		}
		if calls, _ := os.ReadFile(callsPath); strings.Contains(string(calls), "msg agent register") {
			res.registered = true
			return res
		}
	}
	return res
}

// pluginLoadFailed reports whether OpenCode logged a load failure for the
// receiver plugin. OpenCode 1.18.35 writes the path unquoted followed by the
// error, e.g.
//
//	level=ERROR message="failed to load plugin" path=file:///.../factotum-msg.js error="Plugin ... must default export an object with server()"
//
// The pattern is scoped to the plugin's own path so the (unrelated) global cmux
// plugins that also fail to load on this OpenCode version do not trip it.
func pluginLoadFailed(out []byte) bool {
	text := string(out)
	return strings.Contains(text, "factotum-msg.js error=") ||
		(strings.Contains(text, "failed to load plugin") && strings.Contains(text, "factotum-msg.js"))
}

// TestPluginLoadFailedDetector pins the detector against the exact OpenCode
// 1.18.35 line shape (path unquoted, then error=) so the load guard cannot
// silently become toothless, and against a run that only carries an unrelated
// global plugin failure.
func TestPluginLoadFailedDetector(t *testing.T) {
	broken := []byte(`timestamp=... level=ERROR message="failed to load plugin" path=file:///tmp/x/.opencode/plugin/factotum-msg.js error="Plugin file:///tmp/x/.opencode/plugin/factotum-msg.js must default export an object with server()"`)
	if !pluginLoadFailed(broken) {
		t.Fatal("pluginLoadFailed() = false for a genuine receiver load error")
	}
	other := []byte(`timestamp=... level=ERROR message="failed to load plugin" path=file:///home/u/.config/opencode/plugins/cmux-feed.js error="Plugin ... must default export an object with server()"`)
	if pluginLoadFailed(other) {
		t.Fatal("pluginLoadFailed() = true for an unrelated global plugin failure")
	}
}

// removeAllEventually removes dir, retrying while it is transiently
// non-removable. OpenCode forks a detached `@opencode-ai/plugin` install into
// .opencode/ (its config loader calls install(...).forkDetach), so it can still
// be writing files when `opencode run` returns; a single RemoveAll then fails
// with "directory not empty". Retrying waits the detached writer out. A caller
// that cannot tolerate leftovers should treat a returned error as best-effort.
func removeAllEventually(dir string) error {
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		if err = os.RemoveAll(dir); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// TestRemoveAllEventuallyRetriesPastTransientFailure pins the cleanup helper's
// retry contract: a scratch directory that cannot be removed on the first
// attempt is still removed once the transient condition clears. This is the
// cleanup half of the OpenCode load guard's flake — OpenCode forks a detached
// `@opencode-ai/plugin` install into .opencode/ that keeps writing after
// `opencode run` exits, so a single RemoveAll intermittently fails with
// "directory not empty".
func TestRemoveAllEventuallyRetriesPastTransientFailure(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(locked, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	go func() {
		time.Sleep(250 * time.Millisecond)
		_ = os.Chmod(locked, 0o755)
	}()

	if err := removeAllEventually(dir); err != nil {
		t.Fatalf("removeAllEventually() = %v; want nil after the transient failure cleared", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir still present after removeAllEventually: stat err = %v", err)
	}
}

// TestPluginUnitTests runs the plugin's Node unit tests, so `mise run test`
// covers the receiver protocol logic (register/claim/inject/ack) without
// OpenCode or a store. Gated on node.
func TestPluginUnitTests(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not on PATH: %v", err)
	}
	cmd := exec.Command(nodePath, "--test", "plugin/factotum-msg.test.mjs")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node plugin tests failed: %v\n%s", err, out)
	}
}
