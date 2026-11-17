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
// claim loop is running, it has a session to inject into, and its current claim
// round trip keeps settling within two long-poll windows; while parked or
// wedged it lets the lease lapse, so `ft msg runs --live` reports the peer as
// gone instead of healthy. A failed claim is not progress either: a run of
// consecutive failures ages the same clock, so a receiver that claims nothing
// stops renewing instead of reading healthy. That run is logged once in full and
// then summarised periodically rather than once per retry, so a store outage
// leaves a readable log instead of a flood that buries the stall line; when the
// run ends, one line says so. Heartbeat failures are bounded the same way, and
// an empty queue is where they matter: an empty claim writes nothing, so a
// locked store fails no claim at all while every heartbeat fails. The reverse
// direction matters too:
// when a heartbeat is answered not_found (a hub restored from a backup forgets
// runs registered after it), the receiver re-registers the run once and logs it,
// so a receiver that is in fact delivering cannot stay out of `--live` forever.
// Neither a failed registration nor a failed claim may end the receiver
// silently: registration is retried, the claim loop catches every error and
// keeps polling, and every transition is logged.
//
// OpenCode reloads a plugin when its staged file changes, which restarts the
// receiver with no session. Persisting the session in the V2 context's storage
// lets the reloaded receiver resume it instead of waiting for an event: an idle
// agent gets none, so a reload would otherwise strand every queued handoff.
//
// A cold restore loses the environment too. OpenCode V2 resumes a surviving
// session as `opencode -s <session>` without the original launch env, so
// FACTOTUM_* never reaches the plugin and it would register nothing while the
// TUI looks healthy. Every load that has its configuration therefore records the
// identity it registered as (and the session it injects into) for its location,
// and a load with no environment recovers it from storage. A restore keeps that
// record current with the session it actually injects into, so the next restart
// resumes the live session instead of a stale id. That recovery waits while
// another receiver holds the actor live - with a backing-off liveness poll, since
// a live peer can hold it for hours: two claimers of one mailbox split a
// hand-off between them, and a human opening the checkout by hand must not take
// over a running agent's queue. The record never carries the hub token, so a
// remote receiver whose token is gone says so and stays inert rather than
// registering against the wrong store.
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
// RESTORE_POLL_MAX_MS caps the parked cold restore's liveness poll (see
// waitForActorFree): a live peer keeps it parked for as long as its agent runs,
// and the cap still notices a lapsed lease promptly enough to adopt the mailbox.
const RESTORE_POLL_MAX_MS = 30000;

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
    // The store a run's receiver must speak to. A cold restore loses the
    // environment that carried it, so the record keeps it: without this a
    // restored receiver would quietly register against whatever store its new
    // host resolves instead of the one its run was launched with.
    store: (env.FACTOTUM_STORE || "").trim(),
    storeOpts: (env.FACTOTUM_STORE_OPTS || "").trim(),
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

// FAILURE_SUMMARY_EVERY bounds outage logging. A failing call repeats on its
// own cadence (a claim every retry delay, a heartbeat every ttl/3), and a store
// outage lasts minutes: one entry per attempt buries the one-shot stall line an
// operator reads the log for. Each kind of failure logs its first attempt in
// full (the store's own error, which can run to several lines), then one summary
// per this many consecutive failures, so the count still says the store is down
// rather than the receiver wedged.
const FAILURE_SUMMARY_EVERY = 20;

/** oneLine collapses a child ft error for the periodic summary. */
function oneLine(text) {
  return String(text).replace(/\s+/g, " ").trim();
}

/**
 * isRunNotFound reports whether a receiver call failed because the hub no longer
 * knows the run. The transport decides how it can tell: the hub's HTTP transport
 * answers 404, while the local CLI exits non-zero with the store's `not found`
 * error. A hub restored from an old backup forgets runs registered after it, so
 * this is a recoverable condition, not a failure to give up on.
 */
function isRunNotFound(error) {
  return error?.notFound === true || /not found/i.test(String(error?.message || error));
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
      const error = new Error(`ft msg ${op.op} failed (${response.status}): ${(text || "").trim()}`);
      // A 404 is the hub not knowing the run, which the receiver recovers from
      // by re-registering; carrying it structurally beats matching the body.
      error.status = response.status;
      error.notFound = response.status === 404;
      throw error;
    }
    return parseJSON(text);
  };
}

/**
 * receiverEnv is the environment the receiver's child ft runs with: the host's
 * own environment, with the store the receiver was configured for pinned onto
 * it. The pin matters after a cold restore, when the host no longer carries the
 * FACTOTUM_STORE* the run was launched with.
 *
 * It also names the receiver's actor as the child's default actor. Every one of
 * these children is spawned by an agent host, not by a shell that named an
 * identity, so without it each invocation falls back to the CLI's agent
 * heuristic and warns "probable agent caller ... this invocation stays
 * unattributed" - which lands inside the receiver's own log lines. The path is
 * the default actor, not FACTOTUM_CALLER: a caller is resolved only against
 * registered Actor records, so the loop's role actors (builder-<task>, chief)
 * would be dropped with a warning of their own, while the default path trusts
 * them. An inherited default is replaced, because the receiver knows the actor
 * its run belongs to.
 */
export function receiverEnv(config, env = process.env) {
  const merged = { ...env };
  if (config?.store) merged.FACTOTUM_STORE = config.store;
  if (config?.storeOpts) merged.FACTOTUM_STORE_OPTS = config.storeOpts;
  if (config?.actor) merged.FACTOTUM_DEFAULT_ACTOR = config.actor;
  return merged;
}

/** selectCall picks the transport: a hub URL means HTTP, otherwise local ft. */
export function selectCall(config, { run = (argv) => spawnRunner(config.bin, argv, { env: receiverEnv(config) }), fetchFn } = {}) {
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
  // A claim round trip - issue the poll, receive the answer, inject and ack -
  // must settle within two long-poll windows. Past that the loop is not merely
  // idle but wedged on an await that never returns (a hung local ft, a remote hub
  // stalling mid-poll with no abort, a session prompt that never resolves), and
  // renewing the lease would advertise a peer that is queueing every handoff.
  const stallMs = 2 * num(config.waitMs, DEFAULT_WAIT_MS);

  let runID = "";
  let sessionID = "";
  let stopped = false;
  let heartbeatTimer = null;
  // reRegistering coalesces the heartbeat's ticks: one registration can outlast
  // a tick, and one forgotten run is not a reason to run two registrations.
  let reRegistering = false;
  // loopRunning, parked, stalled and lastProgressAt describe what the receiver
  // can do right now, not whether its process is up. healthy() is the lease gate;
  // the flags drive the one-shot log lines. All of them exist so an inert
  // receiver is visible from outside.
  let loopRunning = false;
  let parked = false;
  let stalled = false;
  let lastProgressAt = 0;
  let lastRestoreAt = 0;

  // healthy reports whether this receiver can actually deliver a message: it is
  // not disposing, its claim loop is running, it has a session to inject into,
  // and its current round trip has not overrun. A receiver that fails this must
  // not renew its lease - the hub reads a renewed lease as a working peer, and
  // `ft msg runs --live` would otherwise report a healthy agent while every
  // handoff sat queued behind an inert plugin. Letting the lease lapse makes the
  // failure detectable without a screen.
  function healthy() {
    return !stopped && loopRunning && !!sessionID && Date.now() - lastProgressAt <= stallMs;
  }

  // startRoundTrip seeds the health clock and settleRoundTrip advances it. The
  // pair bounds a round trip rather than the loop's lifetime: a promise that
  // never settles keeps the loop "running" forever, so only a poll that actually
  // returned may count as progress. A failed poll is not progress either - the
  // loop is alive but completing nothing - so it does not settle.
  function startRoundTrip() {
    lastProgressAt = Date.now();
  }

  function settleRoundTrip() {
    lastProgressAt = Date.now();
    if (!stalled) return;
    stalled = false;
    log("factotum msg: claim round trips resumed; renewing this run's lease again");
  }

  // noteStall logs the first tick that finds the current round trip overrun, and
  // stays quiet until it settles. Parking is not a stall: it has its own line.
  function noteStall() {
    if (!loopRunning || !sessionID || stalled) return;
    stalled = true;
    log(`factotum msg: no claim round trip settled in ${stallMs}ms; not renewing this run's lease until one does (a hung claim or injection)`);
  }

  // boundedFailures bounds what repeated failures of one kind write to the log.
  // Every attempt fails with the same store error, so logging each one (often
  // multi-line, every retry delay) buries the stall line; the first failure keeps
  // its full message - that is the "the store is down" signal - and the run is
  // then summarised periodically. fail() and recovered() are the two edges, and
  // a run that ends logs once so the next outage is never silent behind this
  // one's suppression. fail() returns true for the first failure of a run, so a
  // caller can add its own once-per-outage line next to the full detail.
  function boundedFailures(kind, cadence) {
    let failures = 0;
    let firstAt = 0;
    const secsSinceFirst = () => Math.round((Date.now() - firstAt) / 1000);
    return {
      fail(error) {
        const detail = String(error?.message || error);
        failures += 1;
        if (failures === 1) {
          firstAt = Date.now();
          log(`factotum msg: ${kind} failed: ${detail}`);
          return true;
        }
        if (failures % FAILURE_SUMMARY_EVERY === 0) {
          log(`factotum msg: ${failures} consecutive ${kind} failures over ${secsSinceFirst()}s${cadence} (last: ${oneLine(detail)})`);
        }
        return false;
      },
      recovered() {
        const run = failures;
        failures = 0;
        if (run < 2) return;
        log(`factotum msg: ${kind} failures stopped after ${run} consecutive attempts over ${secsSinceFirst()}s`);
      },
    };
  }

  const claimFailures = boundedFailures("claim", `; still retrying every ${retryMs}ms`);
  const heartbeatFailures = boundedFailures("heartbeat", "");
  // Registration repeats on the same retry delay as a claim, but only until it
  // succeeds: a store outage at startup used to write one line per attempt before
  // a run existed, so it gets the same bound.
  const registerFailures = boundedFailures("register", `; still retrying every ${retryMs}ms`);

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
    while (!stopped) {
      try {
        await register();
        registerFailures.recovered();
        log(`factotum msg: registered actor ${config.actor} as run ${runID}`);
        return true;
      } catch (error) {
        if (registerFailures.fail(error)) {
          // One line names the state the flood used to imply on every attempt:
          // this receiver has no run yet, so it claims and delivers nothing.
          log(`factotum msg: not registered yet; retrying every ${retryMs}ms until the store accepts a registration`);
        }
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

  // reRegisterAfterForget restores this receiver's identity when the hub answers
  // not_found. Registration is upsert, so the run is recreated and the claim loop
  // (which reads runID) follows the new id. It tries once per answer: a failed
  // re-registration is logged and the next heartbeat tick tries again, so a hub
  // that stays unreadable costs a logged heartbeat rather than a receiver that is
  // delivering but permanently invisible to `ft msg runs --live`.
  async function reRegisterAfterForget() {
    if (reRegistering) return;
    reRegistering = true;
    const forgotten = runID;
    try {
      log(`factotum msg: heartbeat answered not found for run ${forgotten} (the hub no longer knows it); re-registering`);
      await register();
      if (stopped) {
        // Disposed mid-registration: the lease dispose tried to drop belonged to
        // the old id, so drop the new one too instead of orphaning it until TTL.
        await deregister().catch(() => {});
        return;
      }
      log(`factotum msg: re-registered actor ${config.actor} as run ${runID} after the hub forgot run ${forgotten}`);
    } catch (error) {
      log(`factotum msg: re-register after a forgotten run failed: ${error?.message || error}`);
    } finally {
      reRegistering = false;
    }
  }

  async function heartbeat() {
    try {
      await call({ op: "heartbeat", run: runID, ttl: `${config.ttlMs}ms` });
    } catch (error) {
      // A forgotten run is the one heartbeat error with a recovery: the receiver
      // is still claiming, so it must not fall out of the live list for good.
      if (!isRunNotFound(error)) throw error;
      await reRegisterAfterForget();
    }
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
  // dropped or burn an attempt); otherwise it claims and delivers. It does not
  // touch the health clock: only its return (a completed round trip) is progress.
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
  // run, which is exactly the inert receiver this receiver must not become. A
  // failed poll is retried, and deliberately does not settle a round trip: a run
  // of failures must age the health clock so the stall line fires and the lease
  // lapses, instead of reading healthy while claiming nothing. An outage logs its
  // first failure and then a periodic summary (see boundedFailures).
  async function loop() {
    loopRunning = true;
    try {
      while (!stopped) {
        try {
          await poll();
          claimFailures.recovered();
          settleRoundTrip();
        } catch (error) {
          claimFailures.fail(error);
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
      startRoundTrip();
      heartbeatTimer = setIntervalFn(() => {
        // Renew only while the receiver can claim and its round trips keep
        // settling (see healthy()). An inert or wedged receiver lets its lease
        // lapse so the hub sees the peer as gone, and says why once.
        if (!healthy()) {
          noteStall();
          return;
        }
        return heartbeat().then(
          () => heartbeatFailures.recovered(),
          (error) => heartbeatFailures.fail(error),
        );
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
 * agentRecordKey scopes a receiver's recorded identity to one OpenCode location.
 * The context's storage is shared by every location OpenCode hosts, so the
 * location itself must be part of the key; the project id (a hash of the
 * canonical directory) is stable across a restart, which is exactly what a cold
 * restore needs. The part is length-prefixed so its content cannot shift the
 * boundary of the key.
 */
export function agentRecordKey(ctx) {
  const location = ctx?.location?.project?.id || ctx?.location?.directory || "";
  return `factotum.agent.${String(location).length}:${location}`;
}

/**
 * msgAgentStore persists the identity a receiver registered as, so a load that
 * arrives with no environment (a V2 cold restore) can register again. The token
 * is deliberately not part of the record: a bearer token does not belong in
 * durable plugin state, and a remote receiver that cannot authenticate must not
 * silently fall back to the local store.
 */
export function msgAgentStore(ctx) {
  const key = agentRecordKey(ctx);
  return {
    async load() {
      let stored;
      try {
        stored = await ctx?.storage?.get(key);
      } catch (_) {
        return null;
      }
      if (!stored || typeof stored !== "object") return null;
      if (!stored.project || !stored.actor) return null;
      return {
        config: configFromRecord(stored),
        sessionID: typeof stored.sessionID === "string" ? stored.sessionID : "",
      };
    },
    async save(config, sessionID) {
      try {
        await ctx?.storage?.set(key, recordFromConfig(config, sessionID));
      } catch (_) {
        // Best-effort: a failed save only costs a later cold restore its identity.
      }
    },
  };
}

function recordFromConfig(config, sessionID) {
  return {
    project: config.project,
    actor: config.actor,
    task: config.task || "",
    harness: config.harness,
    bin: config.bin,
    host: config.host || "",
    waitMs: config.waitMs,
    ttlMs: config.ttlMs,
    retryMs: config.retryMs,
    sessionWaitMs: config.sessionWaitMs,
    msgUrl: config.msgUrl || "",
    store: config.store || "",
    storeOpts: config.storeOpts || "",
    sessionID: sessionID || "",
  };
}

function configFromRecord(record) {
  return {
    project: record.project,
    actor: record.actor,
    task: record.task || "",
    bin: record.bin || "ft",
    harness: record.harness || "opencode",
    host: record.host || "",
    // The restoring host is a new process, so the pid is its own - never the
    // dead one from the record.
    pid: process.pid,
    waitMs: num(record.waitMs, DEFAULT_WAIT_MS),
    ttlMs: num(record.ttlMs, DEFAULT_TTL_MS),
    retryMs: num(record.retryMs, DEFAULT_RETRY_MS),
    sessionWaitMs: num(record.sessionWaitMs, SESSION_WAIT_MS),
    msgUrl: normalizeMsgURL(record.msgUrl),
    token: "",
    store: record.store || "",
    storeOpts: record.storeOpts || "",
  };
}

/** sessionAlive reports whether a session still exists to inject into. */
async function sessionAlive(ctx, sid) {
  try {
    return !!(await ctx?.session?.get({ sessionID: sid }));
  } catch (_) {
    return false;
  }
}

/**
 * actorLive reports whether another live run is registered for the actor: true,
 * false, or null when the store cannot say (`ft msg runs` failed). A null never
 * blocks a restore - the register itself is retried, so an unreadable store
 * costs a slow start, not a stranded mailbox.
 */
async function actorLive(run, config) {
  let result;
  try {
    result = await run(["msg", "runs", "-p", config.project, "-o", "json"]);
  } catch (_) {
    return null;
  }
  if (result?.code !== 0) return null;
  const rows = parseJSON(result?.stdout);
  if (!Array.isArray(rows)) return null;
  return rows.some((row) => row?.actor === config.actor && row?.live === true);
}

/**
 * waitForActorFree holds a cold restore until no live receiver serves the actor,
 * so the restore can never split a running agent's mailbox. A crashed receiver's
 * lease lapses within its TTL, which is the case this waits out; a live peer
 * keeps this parked (and says so once) until it is disposed.
 *
 * The poll backs off from the retry delay to RESTORE_POLL_MAX_MS: a live peer
 * holds the actor for as long as its agent runs, so a fixed one-second poll
 * would spawn `ft msg runs` for hours.
 */
async function waitForActorFree(config, { run, log, stopped }) {
  if (config.msgUrl) return true;
  const call = run || ((argv) => spawnRunner(config.bin, argv, { env: receiverEnv(config) }));
  const retryMs = num(config.retryMs, DEFAULT_RETRY_MS);
  const maxPollMs = Math.max(RESTORE_POLL_MAX_MS, retryMs);
  let pollMs = retryMs;
  let told = false;
  while (!stopped()) {
    const live = await actorLive(call, config);
    if (live !== true) return true;
    if (!told) {
      told = true;
      log(`factotum msg: actor ${config.actor} already has a live receiver; registering again once its lease lapses`);
    }
    await sleep(pollMs);
    pollMs = Math.min(pollMs * 2, maxPollMs);
  }
  return false;
}

/**
 * setupRestoredReceiver recovers a receiver whose launch environment is gone
 * (the V2 cold restore) from the identity the previous load recorded for this
 * location. It runs entirely off the load path - V2 awaits setup, so probing the
 * store here would block OpenCode startup - and returns the cleanup the host
 * calls on unload.
 */
export function setupRestoredReceiver(ctx, { call, run, log = pluginLog } = {}) {
  let disposed = false;
  let cleanup = null;
  const stopped = () => disposed;
  void (async () => {
    const record = await msgAgentStore(ctx).load();
    if (!record) return;
    if (record.config.msgUrl && !record.config.token) {
      log(`factotum msg: recorded receiver for actor ${record.config.actor} talks to a hub, but this restore has no FACTOTUM_SERVE_TOKEN; staying inert instead of registering against the local store`);
      return;
    }
    if (!(await waitForActorFree(record.config, { run, log, stopped }))) return;
    if (record.sessionID && !(await sessionAlive(ctx, record.sessionID))) {
      log(`factotum msg: recorded session ${record.sessionID} for actor ${record.config.actor} is gone; staying inert`);
      return;
    }
    if (disposed) return;
    log(`factotum msg: no launch environment; restoring actor ${record.config.actor} from this location's recorded identity`);
    // A restored receiver records too: the session it adopts is what the next
    // restart must resume, and only this record carries it across the restart.
    cleanup = startReceiver(ctx, record.config, { call, run, log, resume: record.sessionID, record: true });
  })().catch((error) => log(`factotum msg: cold restore failed: ${error?.message || error}`));
  return () => {
    disposed = true;
    if (cleanup) void cleanup();
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
 * startReceiver wires the shared receiver to the context's session and event
 * domains and returns the cleanup OpenCode calls on unload. `record` keeps the
 * location's recorded identity and session current, so the next cold restore
 * resumes the session this receiver injects into; `resume` supplies the session
 * a restore recovered, and `run`/`call` are the test seams for the ft transport.
 */
export function startReceiver(ctx, config, { call, run, loadSession, saveSession, log = pluginLog, resume = "", record = false } = {}) {
  const sessions = msgSessionStore(ctx, config);
  const agents = msgAgentStore(ctx);
  // Every record write joins one chain. The seed below is issued before the
  // event subscription, and an event can announce the session the host is
  // actually running while that seed is still in flight: written concurrently,
  // the seed can land last and put the record back on the stale session the
  // restore resumed, which the next restart would then resume. Chaining the
  // writes keeps the record on the newest session the receiver learned.
  let writes = Promise.resolve();
  const enqueue = (write) => {
    const writeNow = writes.then(write);
    // The chain moves past a failed write; the write's own caller still handles
    // it (the seed's surfaces as "receiver start failed", a session refresh is
    // best-effort and rememberSession already swallows it).
    writes = writeNow.catch(() => {});
    return writeNow;
  };
  // Record the identity before any session is known: the service can restart
  // before the agent's first turn, and a receiver registered without a session
  // is still addressable (it injects once one appears). A restore starts from
  // the session it resumed, so seeding with that keeps the record on the
  // session this receiver injects into instead of blanking it.
  const seed = record ? enqueue(() => agents.save(config, resume)) : null;
  const receiver = createFactotumReceiver({
    config,
    call: call || selectCall(config, { run }),
    inject: makeContextInjector(ctx?.session),
    loadSession: loadSession || (resume ? async () => resume : sessions.load),
    saveSession: (sid) => enqueue(async () => {
      await sessions.save(sid);
      if (record) await agents.save(config, sid);
    }),
    log,
  });
  const stop = subscribeContextEvents(ctx, (event) => receiver.onEvent(event));
  const start = async () => {
    if (seed) await seed;
    await receiver.start();
  };
  start().catch((error) => log(`factotum msg: receiver start failed: ${error?.message || error}`));
  return () => {
    stop();
    void receiver.dispose();
  };
}

/**
 * setupV2 is the V2 entrypoint. With FACTOTUM_* it registers as usual; without
 * it (a cold restore) it recovers the identity recorded for this location. Both
 * run off the load path so the plugin never blocks OpenCode startup, and both
 * return the cleanup OpenCode calls on unload.
 */
export function setupV2(ctx, { env = process.env, call, run, loadSession, saveSession, log = pluginLog } = {}) {
  const config = configFromEnv(env);
  if (!config) return setupRestoredReceiver(ctx, { call, run, log });
  return startReceiver(ctx, config, { call, run, loadSession, saveSession, log, record: true });
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
