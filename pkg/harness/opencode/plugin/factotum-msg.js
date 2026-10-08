// Factotum message receiver for OpenCode.
//
// This plugin default-exports an object with `server()` (the shape OpenCode
// 1.18.35's loader requires), NOT a `setup()` factory. On load it registers the
// session with `ft`, long-polls the message queue, injects each claimed message
// as a user turn through the OpenCode session API, and acks it. The poll loop
// runs off the agent's turn, so injection never blocks tool execution.
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
//
// With no project/actor configured the plugin is inert, so it is safe to load
// in an ordinary OpenCode session.

import { spawn } from "node:child_process";

export const DEFAULT_WAIT_MS = 25000;
export const DEFAULT_TTL_MS = 90000;
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
  log = () => {},
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
} = {}) {
  if (!config) throw new Error("factotum receiver: config is required");
  if (typeof inject !== "function") throw new Error("factotum receiver: inject is required");
  if (typeof call !== "function") throw new Error("factotum receiver: call is required");

  let runID = "";
  let sessionID = "";
  let stopped = false;
  let heartbeatTimer = null;

  async function register() {
    const out = await call({
      op: "register", actor: config.actor, harness: config.harness,
      host: config.host, pid: config.pid, ttl: `${config.ttlMs}ms`, task: config.task,
    });
    runID = out?.run_id || "";
    if (!runID) throw new Error("ft msg agent register returned no run_id");
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

  async function deliver(message) {
    const sid = await waitForSession();
    if (stopped) return;
    if (!sid) {
      await ack(message.id, "failed", "no opencode session available to inject into").catch(() => {});
      return;
    }
    try {
      await inject(sid, formatMessage(message), message);
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
    sessionID: () => sessionID,
    onEvent(event) {
      const sid = event?.properties?.sessionID;
      if (typeof sid === "string" && sid) sessionID = sid;
    },
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

export default {
  id: "factotum.msg",
  async server(input) {
    const config = configFromEnv(process.env);
    if (!config) return {};
    const receiver = createFactotumReceiver({
      config,
      call: selectCall(config),
      inject: makeInjector(input?.client),
    });
    const hooks = {
      event: async ({ event }) => receiver.onEvent(event),
      dispose: () => receiver.dispose(),
    };
    // Registration and the claim loop run off the load path so the plugin never
    // blocks OpenCode startup.
    receiver.start().catch(() => {});
    return hooks;
  },
};
