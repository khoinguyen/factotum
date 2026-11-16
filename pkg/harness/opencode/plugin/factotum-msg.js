// Factotum message receiver for OpenCode.
//
// One default export serves both OpenCode majors from the same file:
//
//   - OpenCode 1.x (the V1 object plugin API) finds and calls `server(input)`
//     and, on unload, its returned `dispose` hook.
//   - OpenCode 2.x (the `Plugin.define({ id, setup })` API) finds `id` and
//     `setup(ctx)`; `setup` subscribes to the event stream and returns the
//     cleanup function. V2 has no `server`, and V1 has no `setup`, so each
//     major ignores the other's half.
//
// On load it registers the session with `ft`, resolves the OpenCode session to
// inject into (resuming one it persisted on a previous load, else waiting for
// OpenCode to announce one), long-polls the message queue, injects each claimed
// message as a user turn through the OpenCode session API, and acks it. It holds
// off claiming until it has a session (a claimed message it cannot inject would
// otherwise be dropped), and nacks rather than fails a message it cannot
// deliver. The poll loop runs off the agent's turn, so injection never blocks
// tool execution.
//
// Health means "claiming", not "the process is up". The hub reads a renewed run
// lease as a working peer, so a receiver that cannot claim while still
// heartbeating is an inert agent that quietly queues every handoff behind a
// live-looking TUI. This receiver therefore renews its lease only while its
// claim loop is running and it has a session to inject into; while parked it lets
// the lease lapse, so `ft msg runs --live` reports the peer as gone instead of
// healthy. Neither a failed registration nor a failed claim may end the
// receiver silently: registration is retried, the claim loop catches every
// error and keeps polling, and both transitions are logged.
//
// OpenCode reloads a plugin when its staged file changes, which restarts the
// receiver with no session. Persisting the session in the V2 context's storage
// lets the reloaded receiver resume it instead of waiting for an event: an idle
// agent gets none, so a reload would otherwise strand every queued handoff.
//
// It is a thin client of the receiver protocol. With no hub URL it shells out to
// the `ft msg agent` verbs (the stable local adapter interface); when
// `FACTOTUM_MSG_URL` points at a remote `ft serve`, it speaks the token-gated
// `/api/msg/*` HTTP transport instead, so a receiver on another host reaches the
// hub. Configuration comes from the environment `ft run` sets:
//
//	FACTOTUM_PROJECT   project the session belongs to (required)
//	FACTOTUM_ACTOR     actor the session registers as (required)
//	FACTOTUM_TASK_ID   task the session is working (optional)
//	FACTOTUM_BIN       ft binary (default "ft")
//	FACTOTUM_HARNESS   harness name (default "opencode")
//	FACTOTUM_MSG_URL       remote ft serve hub base URL (default: local ft)
//	FACTOTUM_SERVE_TOKEN   bearer token for the remote hub
//	FACTOTUM_MSG_WAIT_MS  long-poll window (default 25000)
//	FACTOTUM_MSG_TTL_MS   run lease (default 90000)
//	FACTOTUM_MSG_SESSION_WAIT_MS  how long to wait for a session (default 10000)
//	FACTOTUM_MSG_RETRY_MS  delay before retrying a failed register or claim (default 1000)
//
// With no project/actor configured the plugin is inert, so it is safe to load
// in an ordinary OpenCode session.

import { spawn } from "node:child_process";

export const DEFAULT_WAIT_MS = 25000;
export const DEFAULT_TTL_MS = 90000;
export const DEFAULT_RETRY_MS = 1000;
const SESSION_WAIT_MS = 10000;
const SESSION_POLL_MS = 50;

function num(value, fallback) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

/**
 * normalizeMsgURL returns the hub base URL: it trims whitespace and trailing
 * slashes and drops an optional trailing /api/msg, so a caller may pass either
 * the hub root or the transport root.
 */
export function normalizeMsgURL(value) {
  let url = (value || "").trim().replace(/\/+$/, "");
  if (url.endsWith("/api/msg")) url = url.slice(0, -"/api/msg".length);
  return url;
}

/** configFromEnv reads the receiver configuration, or null when inert. */
export function configFromEnv(env) {
  const project = (env.FACTOTUM_PROJECT || "").trim();
  const actor = (env.FACTOTUM_ACTOR || "").trim();
  if (!project || !actor) return null;
  return {
    project,
    actor,
    task: (env.FACTOTUM_TASK_ID || "").trim(),
    bin: (env.FACTOTUM_BIN || "ft").trim() || "ft",
    harness: (env.FACTOTUM_HARNESS || "opencode").trim() || "opencode",
    host: (env.FACTOTUM_HOST || "").trim(),
    pid: num(env.FACTOTUM_PID, process.pid),
    waitMs: num(env.FACTOTUM_MSG_WAIT_MS, DEFAULT_WAIT_MS),
    ttlMs: num(env.FACTOTUM_MSG_TTL_MS, DEFAULT_TTL_MS),
    retryMs: num(env.FACTOTUM_MSG_RETRY_MS, DEFAULT_RETRY_MS),
    sessionWaitMs: num(env.FACTOTUM_MSG_SESSION_WAIT_MS, SESSION_WAIT_MS),
    msgUrl: normalizeMsgURL(env.FACTOTUM_MSG_URL),
    token: (env.FACTOTUM_SERVE_TOKEN || "").trim(),
  };
}

/** parseJSON decodes a command's stdout, returning null when it is not JSON. */
export function parseJSON(text) {
  const trimmed = (text || "").trim();
  if (!trimmed) return null;
  try {
    return JSON.parse(trimmed);
  } catch (_) {
    return null;
  }
}

/** spawnRunner runs the ft binary with argv and collects its output. */
export function spawnRunner(bin, argv, { spawnFn = spawn, env = process.env, cwd = process.cwd() } = {}) {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawnFn(bin, argv, { cwd, env, stdio: ["ignore", "pipe", "pipe"] });
    } catch (error) {
      resolve({ code: -1, stdout: "", stderr: String(error?.message || error) });
      return;
    }
    let stdout = "";
    let stderr = "";
    child.stdout?.on("data", (chunk) => { stdout += chunk; });
    child.stderr?.on("data", (chunk) => { stderr += chunk; });
    child.on("error", (error) => resolve({ code: -1, stdout, stderr: String(error?.message || error) }));
    child.on("close", (code) => resolve({ code: code ?? -1, stdout, stderr }));
  });
}

/** formatMessage renders a claimed message as the text injected as a user turn. */
export function formatMessage(message) {
  const header = message.from ? `[ft msg from ${message.from}]` : "[ft msg]";
  const task = message.task_id ? ` (task ${message.task_id})` : "";
  return `${header}${task}\n${message.body}`;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** cliArgs maps a receiver operation to the hidden `ft msg agent` argv. */
function cliArgs(project, op) {
  switch (op.op) {
    case "register": {
      const args = ["msg", "agent", "register", "--actor", op.actor, "--project", project,
        "--harness", op.harness, "--pid", String(op.pid), "--ttl", op.ttl];
      if (op.task) args.push("--task", op.task);
      if (op.host) args.push("--host", op.host);
      return args;
    }
    case "claim": {
      const args = ["msg", "agent", "claim", "--run", op.run, "--actor", op.actor,
        "--project", project, "--wait", op.wait];
      if (op.task) args.push("--task", op.task);
      return args;
    }
    case "ack": {
      const args = ["msg", "agent", "ack", op.id, "--run", op.run, "--project", project, "--state", op.state];
      if (op.error) args.push("-e", op.error);
      return args;
    }
    case "nack": {
      const args = ["msg", "agent", "nack", op.id, "--run", op.run, "--project", project];
      if (op.error) args.push("-e", op.error);
      return args;
    }
    case "heartbeat":
      return ["msg", "agent", "heartbeat", "--run", op.run, "--project", project, "--ttl", op.ttl];
    case "deregister":
      return ["msg", "agent", "deregister", "--run", op.run, "--project", project];
    default:
      throw new Error(`factotum receiver: unknown operation ${op.op}`);
  }
}

/** cliCall runs the local ft binary for each receiver operation. */
export function cliCall(config, run) {
  return async (op) => {
    const args = cliArgs(config.project, op);
    const result = await run([...args, "-o", "json"]);
    if (result.code !== 0) {
      throw new Error(`ft ${args.join(" ")} failed (${result.code}): ${(result.stderr || "").trim()}`);
    }
    return parseJSON(result.stdout);
  };
}

/** httpPayload maps a receiver operation to the /api/msg/<verb> JSON body. The
 * hub scopes the project from its own configuration, so it is not sent. */
function httpPayload(op) {
  switch (op.op) {
    case "register":
      return { actor: op.actor, task: op.task, harness: op.harness, host: op.host, pid: op.pid, ttl: op.ttl };
    case "claim":
      return { run_id: op.run, actor: op.actor, task: op.task, wait: op.wait };
    case "ack":
      return { id: op.id, run_id: op.run, state: op.state, error: op.error || "" };
    case "nack":
      return { id: op.id, run_id: op.run, reason: op.error || "" };
    case "heartbeat":
      return { run_id: op.run, ttl: op.ttl };
    case "deregister":
      return { run_id: op.run };
    default:
      throw new Error(`factotum receiver: unknown operation ${op.op}`);
  }
}

/**
 * httpCall speaks a remote hub's token-gated /api/msg/* transport for each
 * receiver operation. A non-2xx response is an error, so the caller's loop
 * retries or reports it exactly as it does a failed `ft` invocation.
 */
export function httpCall(config, { fetchFn = globalThis.fetch } = {}) {
  if (typeof fetchFn !== "function") {
    throw new Error("factotum receiver: fetch is unavailable; set FACTOTUM_MSG_URL only where fetch exists");
  }
  const base = config.msgUrl;
  const headers = { "Content-Type": "application/json" };
  if (config.token) headers.Authorization = `Bearer ${config.token}`;
  return async (op) => {
    const response = await fetchFn(`${base}/api/msg/${op.op}`, {
      method: "POST",
      headers,
      body: JSON.stringify(httpPayload(op)),
    });
    const text = await response.text();
    if (!response.ok) {
      throw new Error(`ft msg ${op.op} failed (${response.status}): ${(text || "").trim()}`);
    }
    return parseJSON(text);
  };
}

/** selectCall picks the transport: a hub URL means HTTP, otherwise local ft. */
export function selectCall(config, { run = (argv) => spawnRunner(config.bin, argv), fetchFn } = {}) {
  return config.msgUrl ? httpCall(config, { fetchFn }) : cliCall(config, run);
}

/**
 * createFactotumReceiver builds the register/claim/inject/ack loop. Dependencies
 * are injectable so the protocol logic is testable without OpenCode or a store.
 */
export function createFactotumReceiver({
  config,
  call,
  inject,
  loadSession,
  saveSession,
  log = () => {},
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
} = {}) {
  if (!config) throw new Error("factotum receiver: config is required");
  if (typeof inject !== "function") throw new Error("factotum receiver: inject is required");
  if (typeof call !== "function") throw new Error("factotum receiver: call is required");

  // Both delays come from config, with the module defaults when a caller builds
  // its own config: a zero retry delay would spin on a persistent failure, and a
  // zero restore interval would hammer storage while parked.
  const retryMs = num(config.retryMs, DEFAULT_RETRY_MS);
  const restoreEveryMs = Math.max(SESSION_POLL_MS, num(config.sessionWaitMs, SESSION_WAIT_MS));

  let runID = "";
  let sessionID = "";
  let stopped = false;
  let heartbeatTimer = null;
  // loopRunning and parked describe what the receiver can do right now, not
  // whether the process is up. healthy() is the lease gate and parked drives the
  // one-shot log line; both exist so an inert receiver is visible from outside.
  let loopRunning = false;
  let parked = false;
  let lastRestoreAt = 0;

  // healthy reports whether this receiver can actually deliver a message: it is
  // not disposing, its claim loop is running, and it has a session to inject
  // into. A receiver that fails this must not renew its lease - the hub reads a
  // renewed lease as a working peer, and `ft msg runs --live` would otherwise
  // report a healthy agent while every handoff sat queued behind an inert
  // plugin. Letting the lease lapse makes the failure detectable without a
  // screen.
  function healthy() {
    return !stopped && loopRunning && !!sessionID;
  }

  async function register() {
    const out = await call({
      op: "register", actor: config.actor, harness: config.harness,
      host: config.host, pid: config.pid, ttl: `${config.ttlMs}ms`, task: config.task,
    });
    runID = out?.run_id || "";
    if (!runID) throw new Error("ft msg agent register returned no run_id");
  }

  // registerWithRetry keeps trying until the receiver is disposed. Registration
  // used to be a single attempt: a transient store or lock error rejected
  // start(), which both entrypoints swallow, leaving an agent with a live TUI,
  // no run and no claim loop for as long as it lived. Retrying turns that silent
  // death into a slow start.
  async function registerWithRetry() {
    for (let attempt = 1; !stopped; attempt += 1) {
      try {
        await register();
        log(`factotum msg: registered actor ${config.actor} as run ${runID}`);
        return true;
      } catch (error) {
        log(`factotum msg: register attempt ${attempt} failed: ${error?.message || error}; retrying in ${retryMs}ms`);
        await sleep(retryMs);
      }
    }
    return false;
  }

  async function claim() {
    const out = await call({
      op: "claim", run: runID, actor: config.actor, wait: `${config.waitMs}ms`, task: config.task,
    });
    return out?.found ? out.message : null;
  }

  async function ack(id, state, error) {
    await call({ op: "ack", id, run: runID, state, error: error || "" });
  }

  // nack releases a claimed message back to the queue (or parks it failed once
  // the attempt cap is reached). It is the safe outcome for a message this
  // receiver cannot inject: never silently lost.
  async function nack(id, reason) {
    await call({ op: "nack", id, run: runID, error: reason || "" });
  }

  async function heartbeat() {
    await call({ op: "heartbeat", run: runID, ttl: `${config.ttlMs}ms` });
  }

  async function deregister() {
    await call({ op: "deregister", run: runID });
  }

  async function waitForSession() {
    const deadline = Date.now() + (config.sessionWaitMs || SESSION_WAIT_MS);
    while (!stopped && !sessionID && Date.now() < deadline) {
      await sleep(SESSION_POLL_MS);
    }
    return sessionID;
  }

  // rememberSession records a session learned from an event so the next load
  // (a reload starts session-less) can resume it. Saving is best-effort: a
  // failed save only costs that next reload its resume.
  function rememberSession(sid) {
    if (sid === sessionID) return;
    sessionID = sid;
    parked = false;
    if (typeof saveSession === "function") {
      void Promise.resolve(saveSession(sid)).catch(() => {});
    }
  }

  // restoreSession recovers the session without waiting for an event, so a
  // receiver reloaded while its agent is idle still has somewhere to inject.
  async function restoreSession() {
    if (sessionID || typeof loadSession !== "function") return;
    try {
      const sid = await loadSession();
      if (sid) rememberSession(sid);
    } catch (error) {
      log(`factotum msg session restore failed: ${error?.message || error}`);
    }
  }

  // park waits for a session to inject into, retrying the persisted-session
  // lookup on the way. A reload's save is fire-and-forget and storage can become
  // readable after the first read, so one failed lookup must not strand the
  // receiver: an idle agent gets no event to announce a session, and without
  // this it would park for the life of the process. The transition is logged
  // once, and the lease lapses meanwhile (see healthy()).
  async function park() {
    if (!parked) {
      parked = true;
      log("factotum msg: no opencode session to inject into yet; not claiming, so this run's lease lapses until a session appears");
    }
    await sleep(SESSION_POLL_MS);
    if (Date.now() - lastRestoreAt < restoreEveryMs) return;
    lastRestoreAt = Date.now();
    await restoreSession();
  }

  async function deliver(message) {
    if (stopped) {
      // Claimed but shutting down: release rather than drop. A nack that fails
      // (the run is already gone) is best-effort; the lease sweep recovers it.
      await nack(message.id, "receiver stopped before delivery").catch(() => {});
      return;
    }
    const sid = await waitForSession();
    if (!sid) {
      // Defensive: the loop only claims once it has a session, but a message we
      // cannot inject is always released, never acked failed-and-dropped.
      await nack(message.id, "no opencode session available to inject into").catch(() => {});
      return;
    }
    try {
      await inject(sid, formatMessage(message), message);
      await ack(message.id, "read", "");
    } catch (error) {
      await ack(message.id, "failed", String(error?.message || error)).catch(() => {});
    }
  }

  // poll runs one claim step: with no session there is nowhere to inject, so it
  // parks rather than claiming (a claimed message it cannot inject would be
  // dropped or burn an attempt); otherwise it claims and delivers.
  async function poll() {
    if (!sessionID) {
      await park();
      return;
    }
    const message = await claim();
    if (!message) return;
    await deliver(message);
  }

  // The loop never ends on an error. A rejection here would otherwise stop
  // claiming for the life of the process while the heartbeat kept renewing the
  // run, which is exactly the inert receiver this receiver must not become.
  async function loop() {
    loopRunning = true;
    try {
      while (!stopped) {
        try {
          await poll();
        } catch (error) {
          log(`factotum msg: claim failed: ${error?.message || error}`);
          await sleep(retryMs);
        }
      }
    } finally {
      loopRunning = false;
    }
  }

  return {
    runID: () => runID,
    sessionID: () => sessionID,
    onEvent(event) {
      // V1 delivers hooks as { properties } and V2 events as { data }.
      const sid = event?.data?.sessionID ?? event?.properties?.sessionID;
      if (typeof sid === "string" && sid) rememberSession(sid);
    },
    async start() {
      if (!(await registerWithRetry())) return;
      if (stopped) return;
      await restoreSession();
      if (stopped) return;
      lastRestoreAt = Date.now();
      heartbeatTimer = setIntervalFn(() => {
        // Renew only while the receiver can claim (see healthy()). An inert
        // receiver lets its lease lapse so the hub sees the peer as gone.
        if (!healthy()) return;
        heartbeat().catch((error) => log(`factotum msg: heartbeat failed: ${error?.message || error}`));
      }, Math.max(1000, Math.floor(config.ttlMs / 3)));
      void loop();
    },
    async dispose() {
      stopped = true;
      if (heartbeatTimer) clearIntervalFn(heartbeatTimer);
      heartbeatTimer = null;
      if (runID) await deregister().catch(() => {});
    },
  };
}

/** makeInjector injects a text message as a new user turn in the session. */
export function makeInjector(client) {
  return async (sessionID, text) => {
    const body = { parts: [{ type: "text", text }] };
    const session = client?.session;
    if (typeof session?.promptAsync === "function") {
      await session.promptAsync({ path: { id: sessionID }, body });
      return;
    }
    if (typeof session?.prompt === "function") {
      await session.prompt({ path: { id: sessionID }, body });
      return;
    }
    throw new Error("opencode session prompt API is unavailable");
  };
}

/** makeContextInjector injects a message through the V2 context's session
 * domain, the successor to the V1 client. */
export function makeContextInjector(session) {
  return async (sessionID, text) => {
    if (typeof session?.prompt !== "function") {
      throw new Error("opencode session prompt API is unavailable");
    }
    await session.prompt({ sessionID, text });
  };
}

/**
 * msgSessionKey scopes a receiver's persisted session to one running agent: the
 * project, the actor, and the plugin host's pid. A plugin reload keeps the pid,
 * so the key survives it; a fresh agent launch has a new pid, so it starts
 * clean instead of resuming a dead session.
 *
 * Each part is length-prefixed so its content cannot shift the boundary between
 * parts: project "a.b" + actor "c" and project "a" + actor "b.c" are different
 * agents and get different keys.
 */
export function msgSessionKey(config) {
  const parts = [config.project, config.actor, config.pid].map(sessionKeyPart);
  return `session.${parts.map((part) => `${part.length}:${part}`).join(".")}`;
}

function sessionKeyPart(part) {
  return String(part ?? "");
}

/**
 * legacyMsgSessionKey is the dot-joined key receivers wrote before parts were
 * length-prefixed. msgSessionStore reads it as a fallback so a reload across the
 * upgrade still resumes its session. It is deliberately skipped when a part
 * contains '.': that is exactly the case where the old format could not tell
 * two agents apart, so the key may belong to someone else.
 */
function legacyMsgSessionKey(config) {
  const parts = [config.project, config.actor, config.pid].map(sessionKeyPart);
  if (parts.some((part) => part.includes("."))) return "";
  return `session.${parts.join(".")}`;
}

/**
 * msgSessionStore persists the receiver's session in the V2 context's durable
 * storage. OpenCode reloads a plugin when its file changes; the new receiver
 * starts session-less, and an idle agent gets no event to announce one, so
 * without this a reloaded receiver would never claim a queued handoff. load()
 * validates that the stored session still exists, so a stale id can never
 * capture a claim and inject it into a dead session.
 */
export function msgSessionStore(ctx, config) {
  const key = msgSessionKey(config);
  const legacyKey = legacyMsgSessionKey(config);
  async function liveSession(candidate) {
    if (!candidate) return "";
    let stored;
    try {
      stored = await ctx?.storage?.get(candidate);
    } catch (_) {
      return "";
    }
    const sid = typeof stored === "string" ? stored : stored?.sessionID;
    if (!sid) return "";
    try {
      if (!(await ctx?.session?.get({ sessionID: sid }))) return "";
    } catch (_) {
      return "";
    }
    return sid;
  }
  return {
    async load() {
      return (await liveSession(key)) || (await liveSession(legacyKey));
    },
    async save(sid) {
      try {
        await ctx?.storage?.set(key, sid);
      } catch (_) {
        // Best-effort: a failed save only costs the next reload its resume.
      }
    },
  };
}

/**
 * subscribeContextEvents forwards the V2 public event stream to onEvent in the
 * background, so the claim loop learns the session to inject into without
 * blocking setup. It returns a cleanup that aborts the subscription.
 */
export function subscribeContextEvents(ctx, onEvent, { controller = new AbortController() } = {}) {
  void (async () => {
    try {
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        onEvent(event);
      }
    } catch (_) {
      // An aborted subscription or a dropped stream ends the loop; the receiver
      // keeps running and re-subscribing is the next load's job.
    }
  })();
  return () => controller.abort();
}

/**
 * pluginLog writes a receiver diagnostic to OpenCode's log stream. The observed
 * inert-receiver failures had to be diagnosed off a screen; the receiver's own
 * state transitions (registered, parked, retrying, failed) must be readable in
 * the log instead.
 */
export function pluginLog(message) {
  try {
    console.error(`[factotum.msg] ${message}`);
  } catch (_) {
    // A host without console must not break the receiver.
  }
}

/**
 * setupV2 is the V2 entrypoint: it wires the shared receiver to the context's
 * session and event domains and returns the cleanup OpenCode calls on unload.
 * The `env`/`call`/`log` seams exist only so the protocol is testable without a
 * live OpenCode runtime.
 */
export function setupV2(ctx, { env = process.env, call, loadSession, saveSession, log = pluginLog } = {}) {
  const config = configFromEnv(env);
  if (!config) return undefined;
  const store = msgSessionStore(ctx, config);
  const receiver = createFactotumReceiver({
    config,
    call: call || selectCall(config),
    inject: makeContextInjector(ctx?.session),
    loadSession: loadSession || store.load,
    saveSession: saveSession || store.save,
    log,
  });
  const stop = subscribeContextEvents(ctx, (event) => receiver.onEvent(event));
  // Registration and the claim loop run off the load path so the plugin never
  // blocks OpenCode startup.
  receiver.start().catch((error) => log(`factotum msg: receiver start failed: ${error?.message || error}`));
  return () => {
    stop();
    void receiver.dispose();
  };
}

// V2 runs `setup`; V1 runs `server`. OpenCode 2 ships the `@opencode/plugin`
// package, OpenCode 1 does not, so a failing import means V1 and the plain
// `id`/`setup` fallback keeps the object valid without the define wrapper.
let v2plugin = null;
try {
  const mod = await import("@opencode/plugin");
  if (mod?.Plugin?.define) v2plugin = mod.Plugin.define({ id: "factotum.msg", setup: setupV2 });
} catch (_) {
  v2plugin = null;
}

export default {
  id: "factotum.msg",
  setup: setupV2,
  async server(input) {
    const config = configFromEnv(process.env);
    if (!config) return {};
    const receiver = createFactotumReceiver({
      config,
      call: selectCall(config),
      inject: makeInjector(input?.client),
      log: pluginLog,
    });
    const hooks = {
      event: async ({ event }) => receiver.onEvent(event),
      dispose: () => receiver.dispose(),
    };
    // Registration and the claim loop run off the load path so the plugin never
    // blocks OpenCode startup.
    receiver.start().catch((error) => pluginLog(`factotum msg: receiver start failed: ${error?.message || error}`));
    return hooks;
  },
  ...(v2plugin || {}),
};
