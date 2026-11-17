// Factotum message receiver for pi.
//
// This extension default-exports a factory `(pi) => ...` (the shape pi's loader
// requires), NOT an object. On `session_start` it registers the session with
// `ft`, long-polls the message queue, injects each claimed message as a user
// turn through pi's `sendUserMessage`, and acks it; a message it can no longer
// deliver (a claim that completes after shutdown) is nacked, never left claimed.
// The poll loop runs off the agent's turn, so injection never blocks tool
// execution, and because `sendUserMessage` triggers a turn when pi is idle it
// wakes an idle session.
//
// Health means "claiming", not "the process is up". The hub reads a renewed run
// lease as a working peer, so a receiver that cannot claim while still
// heartbeating is an inert agent that quietly queues every handoff behind a
// live-looking session. This receiver therefore renews its lease only while its
// claim loop is running and its current claim round trip keeps settling within
// two long-poll windows; while wedged it lets the lease lapse, so `ft msg runs
// --live` reports the peer as gone instead of healthy.
//
// A failed claim is not progress either: a run of consecutive failures ages the
// same clock, so a receiver that claims nothing stops renewing instead of reading
// healthy. That run is logged once in full and then summarised periodically
// rather than once per retry, so a store outage leaves a readable log instead of
// a flood that buries the stall line; when the run ends, one line says so.
// Heartbeat failures are bounded the same way, and so are the registration
// retries that precede a run at startup. The reverse direction matters too: when
// a heartbeat is answered not_found (a hub restored from a backup forgets runs
// registered after it), the receiver re-registers the run once and logs it, so a
// receiver that is in fact delivering cannot stay out of `--live` forever.
// Neither a failed registration nor a failed claim may end the receiver
// silently: registration is retried, the claim loop catches every error and
// keeps polling, and every transition is logged.
//
// It is a thin client of the receiver protocol, a structural mirror of the
// opencode receiver so the two do not drift. With no hub URL it shells out to
// the `ft msg agent` verbs (the stable local adapter interface); when
// `FACTOTUM_MSG_URL` points at a remote `ft serve`, it speaks the token-gated
// `/api/msg/*` HTTP transport instead, so a receiver on another host reaches the
// hub. Configuration comes from the environment `ft run` sets:
//
//	FACTOTUM_PROJECT   project the session belongs to (required)
//	FACTOTUM_ACTOR     actor the session registers as (required)
//	FACTOTUM_TASK_ID   task the session is working (optional)
//	FACTOTUM_SESSION   chief session the run belongs to (optional)
//	FACTOTUM_BIN       ft binary (default "ft")
//	FACTOTUM_HARNESS   harness name (default "pi")
//	FACTOTUM_STORE         store each child ft must speak to (optional)
//	FACTOTUM_STORE_OPTS    that store's options (optional)
//	FACTOTUM_MSG_URL       remote ft serve hub base URL (default: local ft)
//	FACTOTUM_SERVE_TOKEN   bearer token for the remote hub
//	FACTOTUM_MSG_WAIT_MS  long-poll window (default 25000)
//	FACTOTUM_MSG_TTL_MS   run lease (default 90000)
//	FACTOTUM_MSG_RETRY_MS  delay before retrying a failed register or claim (default 1000)
//
// With no project/actor configured the extension is inert, so it is safe to
// load in an ordinary pi session.

import { spawn } from "node:child_process";

export const DEFAULT_WAIT_MS = 25000;
export const DEFAULT_TTL_MS = 90000;
export const DEFAULT_RETRY_MS = 1000;

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
    // The chief session the run belongs to. It rides the register so the hub
    // can attribute this run's lease to its session; without it a remote role
    // that is quiet past the sessions page's idle window reads as stale while
    // it works.
    session: (env.FACTOTUM_SESSION || "").trim(),
    bin: (env.FACTOTUM_BIN || "ft").trim() || "ft",
    harness: (env.FACTOTUM_HARNESS || "pi").trim() || "pi",
    host: (env.FACTOTUM_HOST || "").trim(),
    pid: num(env.FACTOTUM_PID, process.pid),
    waitMs: num(env.FACTOTUM_MSG_WAIT_MS, DEFAULT_WAIT_MS),
    ttlMs: num(env.FACTOTUM_MSG_TTL_MS, DEFAULT_TTL_MS),
    retryMs: num(env.FACTOTUM_MSG_RETRY_MS, DEFAULT_RETRY_MS),
    msgUrl: normalizeMsgURL(env.FACTOTUM_MSG_URL),
    token: (env.FACTOTUM_SERVE_TOKEN || "").trim(),
    // The store every child ft must speak to, so a receiver whose host
    // environment was replaced still reaches its run's store instead of whatever
    // store its new host resolves.
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
  const links = Array.isArray(message.links) ? message.links : [];
  const attachments = links.length
    ? `\n\n=== Links ===\n${links.map((link) => `[${link.kind}] ${link.url}`).join("\n")}`
    : "";
  return `${header}${task}\n${message.body}${attachments}`;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// FAILURE_SUMMARY_EVERY bounds outage logging. A failing call repeats on its own
// cadence (a claim every retry delay, a heartbeat every ttl/3), and a store
// outage lasts minutes: one entry per attempt buries the one-shot stall line an
// operator reads the log for. Each kind of failure logs its first attempt in full
// (the store's own error, which can run to several lines), then one summary per
// this many consecutive failures, so the count still says the store is down
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
      if (op.session) args.push("--session", op.session);
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
      return { actor: op.actor, task: op.task, session: op.session, harness: op.harness, host: op.host, pid: op.pid, ttl: op.ttl };
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
 * it. The pin matters when the host no longer carries the FACTOTUM_STORE* the
 * run was launched with.
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
 * are injectable so the protocol logic is testable without pi or a store.
 */
export function createFactotumReceiver({
  config,
  call,
  inject,
  log = () => {},
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
} = {}) {
  if (!config) throw new Error("factotum receiver: config is required");
  if (typeof inject !== "function") throw new Error("factotum receiver: inject is required");
  if (typeof call !== "function") throw new Error("factotum receiver: call is required");

  // Both delays come from config, with the module defaults when a caller builds
  // its own config: a zero retry delay would spin on a persistent failure.
  const retryMs = num(config.retryMs, DEFAULT_RETRY_MS);
  // A claim round trip - issue the poll, receive the answer, inject and ack -
  // must settle within two long-poll windows. Past that the loop is not merely
  // idle but wedged on an await that never returns (a hung local ft, a remote hub
  // stalling mid-poll with no abort, a session prompt that never resolves), and
  // renewing the lease would advertise a peer that is queueing every handoff.
  const stallMs = 2 * num(config.waitMs, DEFAULT_WAIT_MS);

  let runID = "";
  let stopped = false;
  let heartbeatTimer = null;
  let registerPromise = null;
  // reRegistering coalesces the heartbeat's ticks: one registration can outlast
  // a tick, and one forgotten run is not a reason to run two registrations.
  let reRegistering = false;
  // loopRunning, stalled and lastProgressAt describe what the receiver can do
  // right now, not whether its process is up. healthy() is the lease gate; the
  // flags drive the one-shot log lines. All of them exist so an inert receiver is
  // visible from outside.
  let loopRunning = false;
  let stalled = false;
  let lastProgressAt = 0;

  // healthy reports whether this receiver can actually deliver a message: it is
  // not disposing, its claim loop is running, and its current round trip has not
  // overrun. A receiver that fails this must not renew its lease - the hub reads
  // a renewed lease as a working peer, and `ft msg runs --live` would otherwise
  // report a healthy agent while every handoff sat queued behind an inert
  // extension. Letting the lease lapse makes the failure detectable without a
  // screen.
  function healthy() {
    return !stopped && loopRunning && Date.now() - lastProgressAt <= stallMs;
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
  // stays quiet until it settles.
  function noteStall() {
    if (!loopRunning || stalled) return;
    stalled = true;
    log(`factotum msg: no claim round trip settled in ${stallMs}ms; not renewing this run's lease until one does (a hung claim or injection)`);
  }

  // boundedFailures bounds what repeated failures of one kind write to the log.
  // Every attempt fails with the same store error, so logging each one (often
  // multi-line, every retry delay) buries the stall line; the first failure keeps
  // its full message - that is the "the store is down" signal - and the run is
  // then summarised periodically. fail() and recovered() are the two edges, and a
  // run that ends logs once so the next outage is never silent behind this one's
  // suppression. fail() returns true for the first failure of a run, so a caller
  // can add its own once-per-outage line next to the full detail.
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
      op: "register", actor: config.actor, session: config.session, harness: config.harness,
      host: config.host, pid: config.pid, ttl: `${config.ttlMs}ms`, task: config.task,
    });
    runID = out?.run_id || "";
    if (!runID) throw new Error("ft msg agent register returned no run_id");
  }

  // registerWithRetry keeps trying until the receiver is disposed. Registration
  // used to be a single attempt: a transient store or lock error rejected
  // start(), which both entrypoints swallow, leaving an agent with a live TUI, no
  // run and no claim loop for as long as it lived. Retrying turns that silent
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
  // receiver cannot inject: never silently left `delivered` with no owner.
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

  async function deliver(message) {
    if (stopped) {
      // Claimed but shutting down: the claim was already in flight when dispose()
      // ran, and this receiver can no longer inject, so release it rather than
      // leave it `delivered` with no owner until a lease sweep. A nack that fails
      // (the run is already gone) is best-effort.
      await nack(message.id, "receiver stopped before delivery").catch(() => {});
      return;
    }
    try {
      await inject(formatMessage(message), message);
      await ack(message.id, "read", "");
    } catch (error) {
      await ack(message.id, "failed", String(error?.message || error)).catch(() => {});
    }
  }

  // poll runs one claim step: claim and deliver. It does not touch the health
  // clock: only its return (a completed round trip) is progress.
  async function poll() {
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
    async start() {
      if (stopped) return;
      // Retain the registration promise so dispose() can await it and never
      // deregister a run whose id has not arrived yet (the shutdown-during-
      // register race that would otherwise orphan the run until its TTL).
      registerPromise = registerWithRetry();
      if (!(await registerPromise)) return;
      if (stopped) return;
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
      if (registerPromise) await registerPromise.catch(() => {});
      if (runID) await deregister().catch(() => {});
    },
  };
}

/**
 * makeInjector injects a text message as a user turn through pi. Follow-up
 * delivery is used so injection works both mid-turn (queued after the current
 * turn) and while idle, where pi starts a fresh turn and wakes the session.
 */
export function makeInjector(pi) {
  return async (text) => {
    if (!pi || typeof pi.sendUserMessage !== "function") {
      throw new Error("pi sendUserMessage API is unavailable");
    }
    await pi.sendUserMessage(text, { deliverAs: "followUp" });
  };
}

/**
 * pluginLog writes a receiver diagnostic to the harness's log stream. The
 * observed inert-receiver failures had to be diagnosed off a screen; the
 * receiver's own state transitions (registered, retrying, stalled, failed) must
 * be readable in the log instead.
 */
export function pluginLog(message) {
  try {
    console.error(`[factotum.msg] ${message}`);
  } catch (_) {
    // A host without console must not break the receiver.
  }
}

export default function factotumMsg(pi) {
  const config = configFromEnv(process.env);
  if (!config) return;
  let receiver = null;
  // Registration and the claim loop run on session_start, never from the
  // factory: pi may load an extension in an invocation that starts no session.
  pi.on("session_start", async () => {
    if (receiver) await receiver.dispose();
    receiver = createFactotumReceiver({
      config,
      call: selectCall(config),
      inject: makeInjector(pi),
      log: pluginLog,
    });
    // Registration and the claim loop stay off the start path: a store outage
    // retries in the background instead of holding pi's session_start handler
    // open. dispose() awaits the registration promise, so a shutdown that races
    // registration still deregisters the run it created.
    receiver.start().catch((error) => pluginLog(`factotum msg: receiver start failed: ${error?.message || error}`));
  });
  pi.on("session_shutdown", async () => {
    const current = receiver;
    receiver = null;
    if (current) await current.dispose();
  });
}
