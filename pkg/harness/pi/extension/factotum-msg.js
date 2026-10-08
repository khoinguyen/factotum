// Factotum message receiver for pi.
//
// This extension default-exports a factory `(pi) => ...` (the shape pi's loader
// requires), NOT an object. On `session_start` it registers the session with
// `ft`, long-polls the message queue, injects each claimed message as a user
// turn through pi's `sendUserMessage`, and acks it. The poll loop runs off the
// agent's turn, so injection never blocks tool execution, and because
// `sendUserMessage` triggers a turn when pi is idle it wakes an idle session.
//
// It is a thin client of the `ft msg agent` verbs (the stable adapter
// interface), a structural mirror of the opencode receiver so the two do not
// drift. Configuration comes from the environment `ft run` sets:
//
//	FACTOTUM_PROJECT   project the session belongs to (required)
//	FACTOTUM_ACTOR     actor the session registers as (required)
//	FACTOTUM_TASK_ID   task the session is working (optional)
//	FACTOTUM_BIN       ft binary (default "ft")
//	FACTOTUM_HARNESS   harness name (default "pi")
//	FACTOTUM_MSG_WAIT_MS  long-poll window (default 25000)
//	FACTOTUM_MSG_TTL_MS   run lease (default 90000)
//
// With no project/actor configured the extension is inert, so it is safe to
// load in an ordinary pi session.

import { spawn } from "node:child_process";

export const DEFAULT_WAIT_MS = 25000;
export const DEFAULT_TTL_MS = 90000;

function num(value, fallback) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
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
    harness: (env.FACTOTUM_HARNESS || "pi").trim() || "pi",
    host: (env.FACTOTUM_HOST || "").trim(),
    pid: num(env.FACTOTUM_PID, process.pid),
    waitMs: num(env.FACTOTUM_MSG_WAIT_MS, DEFAULT_WAIT_MS),
    ttlMs: num(env.FACTOTUM_MSG_TTL_MS, DEFAULT_TTL_MS),
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

/**
 * createFactotumReceiver builds the register/claim/inject/ack loop. Dependencies
 * are injectable so the protocol logic is testable without pi or a store.
 */
export function createFactotumReceiver({
  config,
  run,
  inject,
  log = () => {},
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
} = {}) {
  if (!config) throw new Error("factotum receiver: config is required");
  if (typeof inject !== "function") throw new Error("factotum receiver: inject is required");
  if (typeof run !== "function") throw new Error("factotum receiver: run is required");

  let runID = "";
  let stopped = false;
  let heartbeatTimer = null;

  async function call(args) {
    // Every verb is machine-read, so always ask ft for JSON.
    const result = await run([...args, "-o", "json"]);
    if (result.code !== 0) {
      throw new Error(`ft ${args.join(" ")} failed (${result.code}): ${(result.stderr || "").trim()}`);
    }
    return parseJSON(result.stdout);
  }

  async function register() {
    const args = ["msg", "agent", "register", "--actor", config.actor, "--project", config.project,
      "--harness", config.harness, "--pid", String(config.pid), "--ttl", `${config.ttlMs}ms`];
    if (config.task) args.push("--task", config.task);
    if (config.host) args.push("--host", config.host);
    const out = await call(args);
    runID = out?.run_id || "";
    if (!runID) throw new Error("ft msg agent register returned no run_id");
  }

  async function claim() {
    const args = ["msg", "agent", "claim", "--run", runID, "--actor", config.actor,
      "--project", config.project, "--wait", `${config.waitMs}ms`];
    if (config.task) args.push("--task", config.task);
    const out = await call(args);
    return out?.found ? out.message : null;
  }

  async function ack(id, state, error) {
    const args = ["msg", "agent", "ack", id, "--run", runID, "--project", config.project, "--state", state];
    if (error) args.push("-e", error);
    await call(args);
  }

  async function heartbeat() {
    await call(["msg", "agent", "heartbeat", "--run", runID, "--project", config.project, "--ttl", `${config.ttlMs}ms`]);
  }

  async function deregister() {
    await call(["msg", "agent", "deregister", "--run", runID, "--project", config.project]);
  }

  async function deliver(message) {
    if (stopped) return;
    try {
      await inject(formatMessage(message), message);
      await ack(message.id, "read", "");
    } catch (error) {
      await ack(message.id, "failed", String(error?.message || error)).catch(() => {});
    }
  }

  async function loop() {
    while (!stopped) {
      let message;
      try {
        message = await claim();
      } catch (error) {
        log(`factotum msg claim failed: ${error?.message || error}`);
        await sleep(1000);
        continue;
      }
      if (!message) continue;
      await deliver(message);
    }
  }

  return {
    runID: () => runID,
    async start() {
      await register();
      if (stopped) return;
      heartbeatTimer = setIntervalFn(() => { heartbeat().catch(() => {}); }, Math.max(1000, Math.floor(config.ttlMs / 3)));
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
      run: (argv) => spawnRunner(config.bin, argv),
      inject: makeInjector(pi),
    });
    receiver.start().catch(() => {});
  });
  pi.on("session_shutdown", async () => {
    const current = receiver;
    receiver = null;
    if (current) await current.dispose();
  });
}
