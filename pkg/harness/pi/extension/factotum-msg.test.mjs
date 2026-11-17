import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import factotumMsg, {
  cliCall,
  configFromEnv,
  createFactotumReceiver,
  formatMessage,
  httpCall,
  makeInjector,
  normalizeMsgURL,
  parseJSON,
  receiverEnv,
  selectCall,
} from "./factotum-msg.js";

const BASE_CONFIG = {
  project: "prj-1",
  actor: "act-1",
  task: "t-1",
  session: "s-chief",
  bin: "ft",
  harness: "pi",
  host: "",
  pid: 7,
  waitMs: 25000,
  ttlMs: 90000,
  retryMs: 5,
  msgUrl: "",
  token: "",
  store: "",
  storeOpts: "",
};

// fakeRun returns a run function whose claim call yields the queued messages,
// then parks (a never-resolving promise) so the poll loop stops spinning.
function fakeRun(claimResults) {
  const calls = [];
  let index = 0;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    const verb = argv[2];
    if (verb === "register") return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
    if (verb === "claim") {
      if (index >= claimResults.length) return new Promise(() => {});
      const result = claimResults[index];
      index += 1;
      return { code: 0, stdout: JSON.stringify(result), stderr: "" };
    }
    return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
  };
  return { run, calls };
}

async function waitFor(predicate, timeoutMs = 1000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error("timed out waiting for condition");
}

function receiverFor(claimResults, injected, { inject } = {}) {
  const { run, calls } = fakeRun(claimResults);
  let intervalCleared = false;
  const receiver = createFactotumReceiver({
    config: BASE_CONFIG,
    call: cliCall(BASE_CONFIG, run),
    inject: inject || (async (text) => { injected.push({ text }); }),
    setIntervalFn: () => 1,
    clearIntervalFn: () => { intervalCleared = true; },
  });
  return { receiver, calls, cleared: () => intervalCleared };
}

// jsonResponse is the minimal fetch Response the receiver reads: ok/status plus
// a text() body.
function jsonResponse(body, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => JSON.stringify(body) };
}

test("default export is a factory function, not an object", () => {
  assert.equal(typeof factotumMsg, "function");
  assert.equal(factotumMsg.server, undefined);
});

test("parseJSON tolerates empty and invalid input", () => {
  assert.equal(parseJSON(""), null);
  assert.equal(parseJSON("not json"), null);
  assert.deepEqual(parseJSON('{"a":1}'), { a: 1 });
});

test("configFromEnv is inert without a project or actor", () => {
  assert.equal(configFromEnv({}), null);
  assert.equal(configFromEnv({ FACTOTUM_PROJECT: "prj" }), null);
  assert.equal(configFromEnv({ FACTOTUM_ACTOR: "act" }), null);
});

test("configFromEnv reads the receiver settings with defaults", () => {
  const config = configFromEnv({ FACTOTUM_PROJECT: "prj", FACTOTUM_ACTOR: "act", FACTOTUM_TASK_ID: "t-1" });
  assert.equal(config.project, "prj");
  assert.equal(config.actor, "act");
  assert.equal(config.task, "t-1");
  assert.equal(config.bin, "ft");
  assert.equal(config.harness, "pi");
  assert.equal(config.waitMs, 25000);
  assert.equal(config.ttlMs, 90000);
  assert.equal(config.retryMs, 1000);
  assert.equal(config.store, "");
  assert.equal(config.storeOpts, "");
});

test("configFromEnv reads the retry delay and the run's store", () => {
  const config = configFromEnv({
    FACTOTUM_PROJECT: "prj",
    FACTOTUM_ACTOR: "act",
    FACTOTUM_MSG_RETRY_MS: "250",
    FACTOTUM_STORE: "jsonfile",
    FACTOTUM_STORE_OPTS: "path=/tmp/db.json",
  });
  assert.equal(config.retryMs, 250);
  assert.equal(config.store, "jsonfile");
  assert.equal(config.storeOpts, "path=/tmp/db.json");
});

test("configFromEnv defaults to the local ft transport", () => {
  const config = configFromEnv({ FACTOTUM_PROJECT: "prj", FACTOTUM_ACTOR: "act" });
  assert.equal(config.msgUrl, "");
  assert.equal(config.token, "");
});

test("configFromEnv reads the chief session a run exports", () => {
  // `ft run` and loop-agent.sh export FACTOTUM_SESSION for the whole session, so
  // the receiver registers under it: the sessions page derives a run's liveness
  // only when the run names the session it belongs to.
  const config = configFromEnv({ FACTOTUM_PROJECT: "prj", FACTOTUM_ACTOR: "act", FACTOTUM_SESSION: " s-1 " });
  assert.equal(config.session, "s-1");
  assert.equal(configFromEnv({ FACTOTUM_PROJECT: "prj", FACTOTUM_ACTOR: "act" }).session, "");
});

test("normalizeMsgURL trims trailing slashes and an /api/msg suffix", () => {
  assert.equal(normalizeMsgURL(""), "");
  assert.equal(normalizeMsgURL("  "), "");
  assert.equal(normalizeMsgURL("http://hub:8484"), "http://hub:8484");
  assert.equal(normalizeMsgURL("http://hub:8484/"), "http://hub:8484");
  assert.equal(normalizeMsgURL("http://hub:8484/api/msg/"), "http://hub:8484");
});

test("configFromEnv reads the remote hub settings", () => {
  const config = configFromEnv({
    FACTOTUM_PROJECT: "prj",
    FACTOTUM_ACTOR: "act",
    FACTOTUM_MSG_URL: "http://hub:8484/",
    FACTOTUM_SERVE_TOKEN: "s3cret",
  });
  assert.equal(config.msgUrl, "http://hub:8484");
  assert.equal(config.token, "s3cret");
});

test("httpCall posts each receiver verb to /api/msg/<verb> with the bearer token", async () => {
  const requests = [];
  const fetchFn = async (url, options) => {
    requests.push({ url, options });
    return jsonResponse({ found: false });
  };
  const config = { ...BASE_CONFIG, msgUrl: "http://hub:8484", token: "s3cret" };
  const call = httpCall(config, { fetchFn });

  await call({ op: "register", actor: "act-1", session: "s-1", harness: "pi", host: "host-a", pid: 7, ttl: "90000ms", task: "t-1" });
  await call({ op: "claim", run: "run-1", actor: "act-1", wait: "25000ms", task: "t-1" });
  await call({ op: "ack", id: "m-1", run: "run-1", state: "read", error: "" });
  await call({ op: "nack", id: "m-2", run: "run-1", error: "receiver stopped before delivery" });
  await call({ op: "heartbeat", run: "run-1", ttl: "90000ms" });
  await call({ op: "deregister", run: "run-1" });

  assert.deepEqual(requests.map((r) => r.url), [
    "http://hub:8484/api/msg/register",
    "http://hub:8484/api/msg/claim",
    "http://hub:8484/api/msg/ack",
    "http://hub:8484/api/msg/nack",
    "http://hub:8484/api/msg/heartbeat",
    "http://hub:8484/api/msg/deregister",
  ]);
  for (const request of requests) {
    assert.equal(request.options.method, "POST");
    assert.equal(request.options.headers.Authorization, "Bearer s3cret");
    assert.equal(request.options.headers["Content-Type"], "application/json");
  }
  assert.deepEqual(JSON.parse(requests[0].options.body), {
    actor: "act-1", task: "t-1", session: "s-1", harness: "pi", host: "host-a", pid: 7, ttl: "90000ms",
  });
  assert.deepEqual(JSON.parse(requests[1].options.body), {
    run_id: "run-1", actor: "act-1", task: "t-1", wait: "25000ms",
  });
  assert.deepEqual(JSON.parse(requests[2].options.body), {
    id: "m-1", run_id: "run-1", state: "read", error: "",
  });
  assert.deepEqual(JSON.parse(requests[3].options.body), {
    id: "m-2", run_id: "run-1", reason: "receiver stopped before delivery",
  });
});

test("httpCall surfaces a non-2xx response as an error", async () => {
  const fetchFn = async () => ({ ok: false, status: 401, text: async () => '{"error":"msg: invalid token"}' });
  const config = { ...BASE_CONFIG, msgUrl: "http://hub:8484", token: "wrong" };
  await assert.rejects(httpCall(config, { fetchFn })({ op: "claim", run: "run-1" }), /401/);
});

test("receiver speaks the /api/msg/* transport when a hub URL is set", async () => {
  const injected = [];
  const urls = [];
  const bodies = new Map();
  let claims = 0;
  const fetchFn = async (url, options) => {
    urls.push(url);
    bodies.set(url, JSON.parse(options.body));
    if (url.endsWith("/register")) return jsonResponse({ run_id: "run-http" });
    if (url.endsWith("/claim")) {
      claims += 1;
      if (claims === 1) return jsonResponse({ found: true, message: { id: "m-9", from: "act-x", body: "remote hi" } });
      // Park the poll loop after the first claim (a pending promise with no IO
      // does not hold the event loop), mirroring fakeRun.
      return new Promise(() => {});
    }
    return jsonResponse({ ok: true });
  };
  const config = { ...BASE_CONFIG, msgUrl: "http://hub:8484", token: "s3cret" };
  const receiver = createFactotumReceiver({
    config,
    call: httpCall(config, { fetchFn }),
    inject: async (text) => { injected.push({ text }); },
    setIntervalFn: () => 1,
    clearIntervalFn: () => {},
  });
  await receiver.start();
  assert.equal(receiver.runID(), "run-http");

  await waitFor(() => injected.length === 1);
  assert.match(injected[0].text, /remote hi/);
  assert.ok(urls.includes("http://hub:8484/api/msg/register"));
  assert.equal(
    bodies.get("http://hub:8484/api/msg/register").session,
    "s-chief",
    "the receiver registers its chief session over the hub",
  );
  await receiver.dispose();
});

test("selectCall uses HTTP when a hub URL is set and local ft otherwise", async () => {
  const fetched = [];
  const remote = { ...BASE_CONFIG, msgUrl: "http://hub:8484", token: "tok" };
  const httpSelected = selectCall(remote, {
    fetchFn: async (url) => { fetched.push(url); return jsonResponse({ run_id: "run-http" }); },
  });
  const viaHTTP = await httpSelected({ op: "register", actor: "act-1", harness: "pi", pid: 7, ttl: "1ms" });
  assert.equal(viaHTTP.run_id, "run-http");
  assert.deepEqual(fetched, ["http://hub:8484/api/msg/register"]);

  const cliSelected = selectCall(BASE_CONFIG, { run: async () => ({ code: 0, stdout: '{"run_id":"run-cli"}', stderr: "" }) });
  const viaCLI = await cliSelected({ op: "register", actor: "act-1", harness: "pi", pid: 7, ttl: "1ms" });
  assert.equal(viaCLI.run_id, "run-cli");
});

test("formatMessage names the sender and task", () => {
  const text = formatMessage({ from: "act-x", task_id: "t-9", body: "hello" });
  assert.match(text, /from act-x/);
  assert.match(text, /task t-9/);
  assert.match(text, /hello/);
});

test("formatMessage renders the links a claim carries", () => {
  const text = formatMessage({
    from: "act-x",
    task_id: "t-9",
    body: "see the issue",
    links: [{ kind: "issue", url: "https://example.invalid/2" }],
  });
  assert.equal(
    text,
    "[ft msg from act-x] (task t-9)\nsee the issue\n\n=== Links ===\n[issue] https://example.invalid/2",
  );
});

test("formatMessage leaves a linkless message unchanged", () => {
  assert.equal(formatMessage({ from: "act-x", body: "hello" }), "[ft msg from act-x]\nhello");
});

test("receiver registers, claims, injects, and acks", async () => {
  const injected = [];
  const { receiver, calls, cleared } = receiverFor(
    [{ found: true, message: { id: "m-1", from: "act-x", task_id: "t-1", body: "hello" } }, { found: false }],
    injected,
  );

  await receiver.start();
  assert.equal(receiver.runID(), "run-1");
  assert.ok(calls.some((c) => c === "msg agent register --actor act-1 --project prj-1 --harness pi --pid 7 --ttl 90000ms --session s-chief --task t-1 -o json"));

  await waitFor(() => injected.length === 1);
  assert.match(injected[0].text, /hello/);
  assert.ok(calls.some((c) => c === "msg agent ack m-1 --run run-1 --project prj-1 --state read -o json"));

  await receiver.dispose();
  assert.ok(cleared(), "dispose clears the heartbeat timer");
  assert.ok(calls.some((c) => c.includes("msg agent deregister --run run-1")), "dispose deregisters");
});

test("receiver acks failed when injection throws", async () => {
  const { receiver, calls } = receiverFor(
    [{ found: true, message: { id: "m-2", body: "boom" } }, { found: false }],
    [],
    { inject: async () => { throw new Error("model down"); } },
  );
  await receiver.start();

  await waitFor(() => calls.some((c) => c.includes("--state failed")));
  assert.ok(calls.some((c) => c.includes("msg agent ack m-2") && c.includes("model down")));
  await receiver.dispose();
});

test("a claim that lands after dispose releases the message instead of dropping it", async () => {
  // The loop's stop check is not enough: a claim already in flight completes
  // after dispose(), and a claimed message this receiver can no longer inject
  // must go back to the queue (nack) instead of sitting `delivered` until a
  // later register's lease sweep. opencode has the same rule.
  const calls = [];
  const injected = [];
  const hung = deferred();
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        return hung.promise;
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const receiver = createFactotumReceiver({
    config: BASE_CONFIG,
    call: cliCall(BASE_CONFIG, run),
    inject: async (text) => { injected.push({ text }); },
    setIntervalFn: () => 1,
    clearIntervalFn: () => {},
  });
  await receiver.start();
  await waitFor(() => calls.some((c) => c.includes("msg agent claim")));
  await receiver.dispose();

  hung.resolve({ code: 0, stdout: JSON.stringify({ found: true, message: { id: "m-late", body: "too late" } }), stderr: "" });
  await waitFor(() => calls.some((c) => c.includes("msg agent nack m-late")));
  assert.deepEqual(injected, [], "a disposed receiver must not inject");
  assert.ok(
    calls.some((c) => c.includes("msg agent nack m-late --run run-1") && c.includes("receiver stopped before delivery")),
    `calls = ${JSON.stringify(calls)}; want the claimed message released, not dropped`,
  );
});

test("dispose waits for an in-flight register and deregisters", async () => {
  let releaseRegister;
  const calls = [];
  const run = async (argv) => {
    calls.push(argv.join(" "));
    if (argv[2] === "register") {
      await new Promise((resolve) => { releaseRegister = resolve; });
      return { code: 0, stdout: JSON.stringify({ run_id: "run-9" }), stderr: "" };
    }
    return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
  };
  const receiver = createFactotumReceiver({
    config: BASE_CONFIG,
    call: cliCall(BASE_CONFIG, run),
    inject: async () => {},
    setIntervalFn: () => 1,
    clearIntervalFn: () => {},
  });
  const started = receiver.start();
  await waitFor(() => releaseRegister !== undefined);
  const disposed = receiver.dispose();
  releaseRegister();
  await Promise.all([started.catch(() => {}), disposed]);
  assert.ok(
    calls.some((c) => c.includes("msg agent deregister --run run-9")),
    "dispose deregisters the run whose register was still in flight",
  );
});

test("makeInjector sends a user message as a follow-up", async () => {
  const calls = [];
  const pi = { sendUserMessage: async (text, options) => calls.push({ text, options }) };
  await makeInjector(pi)("hello");
  assert.deepEqual(calls, [{ text: "hello", options: { deliverAs: "followUp" } }]);

  await assert.rejects(makeInjector({})("hi"), /unavailable/);
});

test("the factory is inert without receiver config", () => {
  const events = [];
  const pi = { on: (name) => events.push(name) };
  const previous = new Map([["FACTOTUM_PROJECT", process.env.FACTOTUM_PROJECT], ["FACTOTUM_ACTOR", process.env.FACTOTUM_ACTOR]]);
  delete process.env.FACTOTUM_PROJECT;
  delete process.env.FACTOTUM_ACTOR;
  try {
    factotumMsg(pi);
  } finally {
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
  assert.deepEqual(events, [], "no handlers registered when inert");
});

test("the factory wires session_start to register and session_shutdown to deregister", async () => {
  const dir = mkdtempSync(join(tmpdir(), "factotum-pi-"));
  const callsPath = join(dir, "calls.log");
  const stub = join(dir, "ft-stub.sh");
  writeFileSync(
    stub,
    "#!/bin/sh\n" +
      "printf '%s\\n' \"$*\" >> \"$CALLS\"\n" +
      "case \"$3\" in\n" +
      "  register) printf '{\"run_id\":\"run-1\"}';;\n" +
      "  claim) sleep 1; printf '{\"found\":false}';;\n" +
      "  *) printf '{\"ok\":true}';;\n" +
      "esac\n",
    { mode: 0o755 },
  );

  const handlers = new Map();
  const pi = { on: (name, handler) => handlers.set(name, handler), sendUserMessage: async () => {} };
  // A live loop env (an agent launched by loop-agent.sh) exports
  // FACTOTUM_HARNESS=opencode2 and FACTOTUM_TASK_ID/DEFAULT_ACTOR/SESSION; left
  // in place they leak into this non-hermetic test and the register call below
  // is made with the wrong harness. Clear them so the pi defaults apply.
  const keys = ["FACTOTUM_PROJECT", "FACTOTUM_ACTOR", "FACTOTUM_BIN", "FACTOTUM_MSG_WAIT_MS", "FACTOTUM_MSG_TTL_MS", "FACTOTUM_HARNESS", "FACTOTUM_TASK_ID", "FACTOTUM_DEFAULT_ACTOR", "FACTOTUM_SESSION", "CALLS"];
  const previous = new Map(keys.map((key) => [key, process.env[key]]));
  delete process.env.FACTOTUM_HARNESS;
  delete process.env.FACTOTUM_TASK_ID;
  delete process.env.FACTOTUM_DEFAULT_ACTOR;
  delete process.env.FACTOTUM_SESSION;
  process.env.FACTOTUM_PROJECT = "prj-1";
  process.env.FACTOTUM_ACTOR = "act-1";
  process.env.FACTOTUM_BIN = stub;
  process.env.FACTOTUM_MSG_WAIT_MS = "10";
  process.env.FACTOTUM_MSG_TTL_MS = "1000";
  process.env.CALLS = callsPath;
  try {
    factotumMsg(pi);
    // session_start returns without awaiting registration (a store outage retries
    // in the background), so the shutdown below is what must not race the run id:
    // dispose() awaits the registration promise and deregisters the run it made.
    await handlers.get("session_start")();
    await handlers.get("session_shutdown")();
  } finally {
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
  const log = readFileSync(callsPath, "utf8");
  assert.match(log, /msg agent register --actor act-1 --project prj-1 --harness pi/);
  assert.match(log, /msg agent deregister --run run-1/);
});

// --- receiver liveness (t-fbkg2d6prv) --------------------------------------
//
// The opencode receiver learned (t-vo7dpanzuu, t-rmws6mklmo, t-4fr25qsnc4) that
// health is "claiming", not "the process is up", and that a store outage must not
// flood the log. The pi extension was still the old receiver: an unconditional
// heartbeat plus a claim loop that logged every failure. These pin the same
// guarantees on the pi side, so the two harnesses cannot drift into different
// silent-inert failure modes.

// deferred is a promise whose settlement the test controls, standing in for a
// round trip that never returns (a wedged claim or injection).
function deferred() {
  let resolve;
  const promise = new Promise((settle) => { resolve = settle; });
  return { promise, resolve };
}

// STALL_WAIT_MS is the long-poll window the stall-then-resume tests hand the
// receiver, so the lease gate samples 2 * it (the stall bound). The bound is
// wall-clock and sampled, so it has to absorb the gap between the receiver
// recording a round trip and the next statement reading the clock; 300ms (a
// 600ms bound) is the derivation the opencode suite landed on under load.
const STALL_WAIT_MS = 300;
// STALL_PAST_MS clears the stall bound (2 * waitMs) by a margin. Derived, not a
// literal: raising waitMs must lengthen the sleep with it rather than silently
// leave the sleep inside the bound.
const STALL_PAST_MS = 2 * STALL_WAIT_MS + 100;

// resumeRenewal is the log sink for a stall-then-resume test: it runs one
// heartbeat tick in the same turn the receiver records the resumed round trip,
// which is the moment the lease gate must see progress.
function resumeRenewal(logs, getTick) {
  let renew;
  const renewed = new Promise((resolve) => { renew = resolve; });
  return {
    log(entry) {
      logs.push(entry);
      if (!/resumed/.test(entry)) return;
      const tick = getTick();
      renew(tick());
    },
    renewed,
  };
}

// heartbeatReceiver wires a receiver whose claim never returns unless the test
// steers it, so the only traffic a test controls is the heartbeat tick. The long
// window keeps manual ticks well inside the stall bound; a test that wants a
// stall shortens waitMs.
function heartbeatReceiver({ run, config = {}, logs = [], log } = {}) {
  let tick = null;
  const receiverConfig = { ...BASE_CONFIG, waitMs: 1000, ...config };
  const receiver = createFactotumReceiver({
    config: receiverConfig,
    call: cliCall(receiverConfig, run),
    inject: async () => {},
    setIntervalFn: (fn) => { tick = fn; return 1; },
    clearIntervalFn: () => {},
    log: log || ((entry) => logs.push(entry)),
  });
  return { receiver, tick: () => tick() };
}

test("a failed register is retried so a transient error cannot leave the receiver silent", async (t) => {
  // Registration used to be a single attempt whose failure start() rejected; the
  // factory swallows that rejection, so the session kept a live TUI, no run, no
  // claim loop and no retry - forever.
  const injected = [];
  const calls = [];
  let attempts = 0;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        attempts += 1;
        if (attempts === 1) return { code: 1, stdout: "", stderr: "store locked" };
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        if (calls.filter((c) => c.includes("msg agent claim")).length > 1) return new Promise(() => {});
        return { code: 0, stdout: JSON.stringify({ found: true, message: { id: "m-retry", body: "after retry" } }), stderr: "" };
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const config = { ...BASE_CONFIG, retryMs: 5 };
  const receiver = createFactotumReceiver({
    config,
    call: cliCall(config, run),
    inject: async (text) => { injected.push({ text }); },
    setIntervalFn: () => 1,
    clearIntervalFn: () => {},
  });
  t.after(() => receiver.dispose());

  await receiver.start();
  await waitFor(() => injected.length === 1);
  assert.ok(attempts >= 2, `register attempts = ${attempts}; want a retry after the failed first attempt`);
  assert.match(injected[0].text, /after retry/);
});

test("a claim round trip that never returns stops renewing the lease", async (t) => {
  // A wedged claim (a hung local ft, or a remote hub stalling mid-poll) keeps the
  // loop *running* without completing a round trip. Health cannot be "the loop
  // promise exists": that is the live-looking peer that quietly queues handoffs.
  const calls = [];
  const logs = [];
  const hung = deferred();
  let tick = null;
  let claimed = false;
  const resume = resumeRenewal(logs, () => tick);
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        if (!claimed) {
          claimed = true;
          return hung.promise;
        }
        return new Promise(() => {});
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const config = { ...BASE_CONFIG, waitMs: STALL_WAIT_MS };
  const receiver = createFactotumReceiver({
    config,
    call: cliCall(config, run),
    inject: async () => {},
    setIntervalFn: (fn) => { tick = fn; return 1; },
    clearIntervalFn: () => {},
    log: resume.log,
  });
  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => calls.some((c) => c.includes("msg agent claim")));

  // Past the stall bound (2 * waitMs) with no completed round trip: the lease
  // must stop being renewed, and the receiver must say why once.
  await new Promise((resolve) => setTimeout(resolve, STALL_PAST_MS));
  await tick();
  assert.equal(calls.some((c) => c.includes("msg agent heartbeat")), false, "a wedged claim must stop the heartbeat");
  const stalls = () => logs.filter((entry) => /round trip/.test(entry)).length;
  assert.equal(stalls(), 1, `stall log entries = ${JSON.stringify(logs)}; want exactly one`);

  // A second tick inside the same stall does not repeat the line.
  await tick();
  assert.equal(stalls(), 1, "the stall is logged once, not every tick");

  // When the round trip finally completes, the lease is renewed again.
  hung.resolve({ code: 0, stdout: JSON.stringify({ found: false }), stderr: "" });
  await waitFor(() => logs.some((entry) => /resumed/.test(entry)), 2000);
  await resume.renewed;
  assert.ok(
    calls.some((c) => c.includes("msg agent heartbeat")),
    `calls = ${JSON.stringify(calls.slice(-4))}; want the lease renewed once a round trip settles again`,
  );
});

test("an injection that never returns stops renewing the lease", async (t) => {
  // Same stall class from the other side: the claim succeeded, but pi's
  // sendUserMessage never settles, so nothing is delivered even though the loop
  // is alive.
  const calls = [];
  const logs = [];
  const hung = deferred();
  let tick = null;
  let claims = 0;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        claims += 1;
        if (claims > 1) return new Promise(() => {});
        return { code: 0, stdout: JSON.stringify({ found: true, message: { id: "m-hang", body: "stuck" } }), stderr: "" };
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const config = { ...BASE_CONFIG, waitMs: 10 };
  const receiver = createFactotumReceiver({
    config,
    call: cliCall(config, run),
    inject: async () => hung.promise,
    setIntervalFn: (fn) => { tick = fn; return 1; },
    clearIntervalFn: () => {},
    log: (entry) => logs.push(entry),
  });
  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => claims === 1);

  await new Promise((resolve) => setTimeout(resolve, 40));
  await tick();
  assert.equal(calls.some((c) => c.includes("msg agent heartbeat")), false, "a wedged injection must stop the heartbeat");
  assert.equal(
    logs.filter((entry) => /round trip/.test(entry)).length,
    1,
    `logs = ${JSON.stringify(logs)}; want one stall line`,
  );
});

test("a run of failing claims is no progress: the stall line fires and the lease lapses", async (t) => {
  // A permanently failing claim (a broken store, a rejected actor) used to be
  // followed by a retry that looked like a live loop; the lease stayed fresh and
  // the run read healthy while every handoff sat queued. Health is a *completed*
  // round trip; a claim that always throws completes none.
  const calls = [];
  const logs = [];
  let claims = 0;
  let broken = true;
  let recovered = 0;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        claims += 1;
        if (broken) return { code: 1, stdout: "", stderr: "Error: store locked" };
        recovered += 1;
        // One completing claim proves progress; then park so the loop cannot spin
        // on an instantly empty queue (which would starve the test's timers).
        if (recovered > 1) return new Promise(() => {});
        return { code: 0, stdout: JSON.stringify({ found: false }), stderr: "" };
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const resume = resumeRenewal(logs, () => tick);
  const { receiver, tick } = heartbeatReceiver({
    run,
    config: { retryMs: 5, waitMs: STALL_WAIT_MS },
    log: resume.log,
  });
  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => claims >= 2);

  // Past the stall bound (2 * waitMs) of consecutive failures.
  await new Promise((resolve) => setTimeout(resolve, STALL_PAST_MS));
  await tick();
  assert.equal(calls.some((c) => c.includes("msg agent heartbeat")), false, "a failing claim must not keep the lease fresh");
  assert.equal(
    logs.filter((entry) => /round trip/.test(entry)).length,
    1,
    `logs = ${JSON.stringify(logs)}; want one stall line`,
  );

  // A claim that completes again is progress: the lease is renewed, once.
  broken = false;
  await waitFor(() => logs.some((entry) => /resumed/.test(entry)), 2000);
  await resume.renewed;
  assert.ok(
    calls.some((c) => c.includes("msg agent heartbeat")),
    `calls = ${JSON.stringify(calls.slice(-4))}; want the lease renewed once a claim completes again`,
  );
});

test("a single failed claim inside the stall window still renews the lease", async (t) => {
  // The rule is a *run* of failures, not one: a claim that fails once and then
  // completes proves the loop is alive, so the lease must not drop on it.
  const calls = [];
  const logs = [];
  let claims = 0;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        claims += 1;
        if (claims === 1) return { code: 1, stdout: "", stderr: "Error: store busy" };
        return new Promise(() => {});
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const { receiver, tick } = heartbeatReceiver({ run, config: { retryMs: 5 }, logs });
  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => claims >= 2);

  await tick();
  assert.ok(
    calls.some((c) => c.includes("msg agent heartbeat")),
    `one failed claim inside the stall window is not a stall; calls = ${JSON.stringify(calls)}`,
  );
});

// --- a store outage must not flood the log (t-fbkg2d6prv) ------------------
//
// During an outage every claim fails, once per retry delay, and each failure
// embeds the store's own (often multi-line) error. One entry per retry buries the
// one line an operator reads the log for - the stall line that says this receiver
// is not delivering - so the receiver logs the first failure in full and then a
// periodic summary of the run.

// outageRun is a receiver's ft whose every claim fails with the store's own
// error, as a read-only or locked store does during an outage.
function outageRun() {
  const calls = [];
  let claims = 0;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        claims += 1;
        return { code: 1, stdout: "", stderr: "Error: create temp file: /tmp/db.json: permission denied" };
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  return { run, calls, claims: () => claims };
}

test("a sustained claim outage logs the first failure and periodic summaries, not one entry per retry", async (t) => {
  const logs = [];
  const { run, calls } = outageRun();
  const { receiver } = heartbeatReceiver({ run, config: { retryMs: 1, waitMs: 1000 }, logs });
  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => calls.some((c) => c.includes("msg agent claim")) && logs.length > 0, 2000);

  // Long enough to cross several summary periods (the bound is per failures).
  await waitFor(() => logs.some((entry) => /40 consecutive claim failures/.test(entry)), 5000);

  const first = logs.filter((entry) => /claim failed:/.test(entry));
  assert.equal(
    first.length,
    1,
    `want exactly one full failure entry, got ${first.length}: ${JSON.stringify(logs.slice(0, 6))}`,
  );
  // The first entry keeps the store's own message: that is the signal that says
  // the store is down rather than the receiver wedged.
  assert.match(first[0], /create temp file: .*permission denied/);

  const summaries = logs.filter((entry) => /consecutive claim failures/.test(entry));
  assert.ok(summaries.length >= 2, `want periodic summaries: ${JSON.stringify(summaries)}`);
  for (const summary of summaries) {
    assert.ok(!summary.includes("\n"), `a summary stays on one line: ${JSON.stringify(summary)}`);
  }
  // Nothing per retry: the outage wrote one full failure entry and one entry per
  // summary, however many claims failed in between.
  assert.equal(
    logs.filter((entry) => /claim failed:|consecutive claim failures/.test(entry)).length,
    1 + summaries.length,
    `outage output is bounded: ${JSON.stringify(logs)}`,
  );
});

test("an outage that ends closes its summary once, and a later outage logs its first failure again", async (t) => {
  const logs = [];
  let claims = 0;
  const run = async (argv) => {
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        claims += 1;
        // Two failures, one completed claim, then failures again: the counter
        // must close the first run and start the second, so a second outage is
        // never silent behind the first one's suppression.
        if (claims <= 2 || claims > 3) return { code: 1, stdout: "", stderr: "Error: store locked" };
        return { code: 0, stdout: JSON.stringify({ found: false }), stderr: "" };
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const { receiver } = heartbeatReceiver({ run, config: { retryMs: 1, waitMs: 1000 }, logs });
  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => logs.filter((entry) => /claim failed:/.test(entry)).length === 2, 5000);

  assert.equal(
    logs.filter((entry) => /claim failures stopped after 2 consecutive attempts/.test(entry)).length,
    1,
    `want one closing line for the run that ended: ${JSON.stringify(logs)}`,
  );
});

test("a heartbeat outage while the queue is empty is bounded the same way", async (t) => {
  // The steady state of a receiver is an empty queue: an empty claim writes
  // nothing, so a read-only store fails no claim and the claim path says nothing
  // - while every heartbeat, once per ttl/3, fails. That gets the same bound: the
  // first failure in full, one summary per 20 consecutive failures, one closing
  // line when renewal works again, and the next outage logs its first failure.
  const logs = [];
  let claims = 0;
  let heartbeats = 0;
  let broken = true;
  const run = async (argv) => {
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        claims += 1;
        // One completed (empty) claim, then a long poll that never returns: the
        // real shape of an idle queue, and it keeps the loop off the CPU.
        if (claims > 1) return new Promise(() => {});
        return { code: 0, stdout: JSON.stringify({ found: false }), stderr: "" };
      case "heartbeat":
        heartbeats += 1;
        if (broken) return { code: 1, stdout: "", stderr: "Error: create temp file: /tmp/db.json: permission denied" };
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  // A long poll window keeps every manual tick inside the stall bound, so the
  // heartbeat ticks below are all sent (an unhealthy receiver sends none).
  const { receiver, tick } = heartbeatReceiver({ run, config: { retryMs: 5, waitMs: 60000 }, logs });
  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => claims >= 2, 2000);

  for (let ticked = 0; ticked < 25; ticked += 1) await tick();
  assert.equal(heartbeats, 25, "every tick inside the stall bound renewed");
  const first = logs.filter((entry) => /heartbeat failed:/.test(entry));
  assert.equal(first.length, 1, `want one full heartbeat-failure entry, got ${first.length}: ${JSON.stringify(logs)}`);
  assert.match(first[0], /create temp file: .*permission denied/);
  const summaries = logs.filter((entry) => /consecutive heartbeat failures/.test(entry));
  assert.deepEqual(
    summaries.map((entry) => /^factotum msg: (\d+) consecutive heartbeat failures/.exec(entry)?.[1]),
    ["20"],
    `want exactly the 20-failure summary, got ${JSON.stringify(summaries)}`,
  );

  // Renewal works again: one closing line names the run that ended.
  broken = false;
  await tick();
  assert.equal(
    logs.filter((entry) => /heartbeat failures stopped after 25 consecutive attempts/.test(entry)).length,
    1,
    `want one closing line: ${JSON.stringify(logs)}`,
  );

  // And a later outage is not silent behind the first one's suppression.
  broken = true;
  await tick();
  assert.equal(
    logs.filter((entry) => /heartbeat failed:/.test(entry)).length,
    2,
    `want a fresh first-failure line for the second outage: ${JSON.stringify(logs)}`,
  );
});

// registerOutageRun fails register `failures` times (forever when it is
// Infinity) with the store's own error, then succeeds.
function registerOutageRun(failures) {
  let registers = 0;
  const run = async (argv) => {
    switch (argv[2]) {
      case "register":
        registers += 1;
        if (registers <= failures) {
          return { code: 1, stdout: "", stderr: "Error: create temp file: /tmp/db.json: permission denied" };
        }
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        return new Promise(() => {});
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  return { run, registers: () => registers };
}

test("a registration outage logs the first failure and periodic summaries, not one entry per retry", async (t) => {
  const logs = [];
  const { run } = registerOutageRun(Infinity);
  const { receiver } = heartbeatReceiver({ run, config: { retryMs: 1 }, logs });
  t.after(() => receiver.dispose());
  // start() cannot resolve while registration keeps failing; the loop is what is
  // under test, and dispose() ends it.
  void receiver.start();
  await waitFor(() => logs.some((entry) => /40 consecutive register failures/.test(entry)), 5000);

  const first = logs.filter((entry) => /register failed:/.test(entry));
  assert.equal(
    first.length,
    1,
    `want exactly one full registration-failure entry, got ${first.length}: ${JSON.stringify(logs.slice(0, 6))}`,
  );
  // The full entry keeps the store's own error: that is the signal that says the
  // store is down rather than the receiver wedged.
  assert.match(first[0], /create temp file: .*permission denied/);

  // The state an operator needs once, not once per attempt.
  assert.equal(
    logs.filter((entry) => /not registered yet/.test(entry)).length,
    1,
    `want exactly one "not registered yet" line: ${JSON.stringify(logs)}`,
  );

  const summaries = logs.filter((entry) => /consecutive register failures/.test(entry));
  assert.ok(summaries.length >= 2, `want periodic summaries: ${JSON.stringify(summaries)}`);
  for (const summary of summaries) {
    assert.ok(!summary.includes("\n"), `a summary stays on one line: ${JSON.stringify(summary)}`);
  }

  // Nothing per retry: the outage wrote the full failure, the state line and one
  // entry per summary, however many attempts failed in between.
  assert.equal(
    logs.filter((entry) => /register failed:|consecutive register failures|not registered yet/.test(entry)).length,
    2 + summaries.length,
    `registration outage output is bounded: ${JSON.stringify(logs)}`,
  );
});

test("a registration outage that ends logs once and registers the run", async (t) => {
  const logs = [];
  const { run, registers } = registerOutageRun(2);
  const { receiver } = heartbeatReceiver({ run, config: { retryMs: 1 }, logs });
  t.after(() => receiver.dispose());

  await receiver.start();
  assert.equal(receiver.runID(), "run-1", "the retry loop registers the run after the store recovers");
  assert.equal(registers(), 3, "two failures then a success");
  assert.equal(
    logs.filter((entry) => /register failures stopped after 2 consecutive attempts/.test(entry)).length,
    1,
    `want one closing line for the outage that ended: ${JSON.stringify(logs)}`,
  );
  assert.equal(
    logs.filter((entry) => /not registered yet/.test(entry)).length,
    1,
    `want the "not registered yet" line once for the outage: ${JSON.stringify(logs)}`,
  );
  assert.ok(
    logs.some((entry) => /registered actor act-1 as run run-1/.test(entry)),
    `want the registration line after recovery: ${JSON.stringify(logs)}`,
  );
});

test("a heartbeat answered not found re-registers the run once and logs it", async (t) => {
  // A hub restored from a backup forgets runs registered after it. The receiver
  // keeps claiming and delivering, but every heartbeat answers not_found; without
  // re-registering it can never reappear in `ft msg runs --live`. Registration is
  // upsert, so one re-register recreates the run under a new id and the claim loop
  // follows it.
  const calls = [];
  const logs = [];
  let registers = 0;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        registers += 1;
        return { code: 0, stdout: JSON.stringify({ run_id: `run-${registers}` }), stderr: "" };
      case "claim":
        return new Promise(() => {});
      case "heartbeat": {
        const id = argv[argv.indexOf("--run") + 1];
        if (id === "run-1") return { code: 1, stdout: "", stderr: "Error: not found: run run-1" };
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
      }
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const { receiver, tick } = heartbeatReceiver({ run, logs });
  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => calls.some((c) => c.includes("msg agent claim")));

  await tick();
  await waitFor(() => registers === 2);
  assert.equal(receiver.runID(), "run-2", "the receiver follows the re-registered run");
  assert.ok(
    logs.some((entry) => /re-registered actor act-1 as run run-2/i.test(entry)),
    `logs = ${JSON.stringify(logs)}; want one re-registration line`,
  );

  // The next renewal goes to the new run and does not re-register again: one
  // re-registration per forgotten run, not one per heartbeat.
  await tick();
  assert.ok(calls.some((c) => c.includes("msg agent heartbeat --run run-2")));
  assert.equal(registers, 2, "a forgotten run is re-registered once, not on every tick");
});

test("a heartbeat that fails for another reason does not re-register", async (t) => {
  // Only a forgotten run (not_found) is re-registered. A transient store or
  // transport error must not churn registrations - against a healthy hub that
  // would create a second run for the same agent.
  const calls = [];
  const logs = [];
  let registers = 0;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        registers += 1;
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        return new Promise(() => {});
      case "heartbeat":
        return { code: 1, stdout: "", stderr: "Error: store locked" };
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const { receiver, tick } = heartbeatReceiver({ run, logs });
  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => calls.some((c) => c.includes("msg agent claim")));

  await tick();
  await waitFor(() => logs.some((entry) => /heartbeat failed/i.test(entry)));
  assert.equal(registers, 1, "a non-not_found heartbeat error must not re-register");
});

test("httpCall marks a 404 as not found so the receiver can re-register", async () => {
  const fetchFn = async () => ({ ok: false, status: 404, text: async () => '{"error":"msg: not found: run run-1"}' });
  const call = httpCall({ ...BASE_CONFIG, msgUrl: "http://hub:8484", token: "s3cret" }, { fetchFn });
  await assert.rejects(call({ op: "heartbeat", run: "run-1" }), (error) => {
    assert.equal(error.notFound, true);
    return true;
  });
});

test("the extension logs receiver state so an inert session leaves evidence", async () => {
  // The observed failures were diagnosed off a screen because the receiver said
  // nothing. Registration and a failing claim must both leave a line in pi's log.
  const entries = [];
  const dir = mkdtempSync(join(tmpdir(), "factotum-pi-log-"));
  const stub = join(dir, "ft-stub.sh");
  writeFileSync(
    stub,
    "#!/bin/sh\n" +
      "case \"$3\" in\n" +
      "  register) printf '{\"run_id\":\"run-1\"}';;\n" +
      "  claim) printf 'Error: store locked\\n' >&2; exit 1;;\n" +
      "  *) printf '{\"ok\":true}';;\n" +
      "esac\n",
    { mode: 0o755 },
  );

  const handlers = new Map();
  const pi = { on: (name, handler) => handlers.set(name, handler), sendUserMessage: async () => {} };
  const keys = ["FACTOTUM_PROJECT", "FACTOTUM_ACTOR", "FACTOTUM_BIN", "FACTOTUM_MSG_RETRY_MS", "FACTOTUM_MSG_WAIT_MS", "FACTOTUM_HARNESS", "FACTOTUM_TASK_ID"];
  const previous = new Map(keys.map((key) => [key, process.env[key]]));
  const originalError = console.error;
  delete process.env.FACTOTUM_HARNESS;
  delete process.env.FACTOTUM_TASK_ID;
  process.env.FACTOTUM_PROJECT = "prj-1";
  process.env.FACTOTUM_ACTOR = "act-1";
  process.env.FACTOTUM_BIN = stub;
  process.env.FACTOTUM_MSG_RETRY_MS = "5";
  process.env.FACTOTUM_MSG_WAIT_MS = "10";
  console.error = (line) => entries.push(String(line));
  try {
    factotumMsg(pi);
    await handlers.get("session_start")();
    await waitFor(() => entries.some((entry) => /registered actor act-1 as run run-1/.test(entry)), 2000);
    await waitFor(() => entries.some((entry) => /claim failed: .*store locked/.test(entry)), 2000);
  } finally {
    // A receiver left claiming keeps the test process alive, so shut it down even
    // when an assertion above failed.
    await handlers.get("session_shutdown")();
    console.error = originalError;
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
  assert.ok(
    entries.some((entry) => /^\[factotum\.msg\] /.test(entry)),
    `log entries = ${JSON.stringify(entries)}; want the extension to name itself on every line`,
  );
});

test("session_start does not hold pi open while registration keeps failing", async () => {
  // Registration retries in the background for as long as the store is down.
  // Awaiting that loop inside session_start would hold pi's handler open for the
  // whole outage, so the session must get past the start path while the retries
  // continue behind it.
  const dir = mkdtempSync(join(tmpdir(), "factotum-pi-retry-"));
  const callsPath = join(dir, "calls.log");
  const stub = join(dir, "ft-stub.sh");
  writeFileSync(
    stub,
    "#!/bin/sh\n" +
      "printf '%s\\n' \"$*\" >> \"$CALLS\"\n" +
      "case \"$3\" in\n" +
      "  register) printf 'Error: store locked\\n' >&2; exit 1;;\n" +
      "  *) printf '{\"ok\":true}';;\n" +
      "esac\n",
    { mode: 0o755 },
  );

  const handlers = new Map();
  const pi = { on: (name, handler) => handlers.set(name, handler), sendUserMessage: async () => {} };
  const keys = ["FACTOTUM_PROJECT", "FACTOTUM_ACTOR", "FACTOTUM_BIN", "FACTOTUM_MSG_RETRY_MS", "FACTOTUM_MSG_WAIT_MS", "FACTOTUM_HARNESS", "FACTOTUM_TASK_ID", "CALLS"];
  const previous = new Map(keys.map((key) => [key, process.env[key]]));
  delete process.env.FACTOTUM_HARNESS;
  delete process.env.FACTOTUM_TASK_ID;
  process.env.FACTOTUM_PROJECT = "prj-1";
  process.env.FACTOTUM_ACTOR = "act-1";
  process.env.FACTOTUM_BIN = stub;
  process.env.FACTOTUM_MSG_RETRY_MS = "5";
  process.env.FACTOTUM_MSG_WAIT_MS = "10";
  process.env.CALLS = callsPath;
  writeFileSync(callsPath, "");
  const attempts = () => readFileSync(callsPath, "utf8").split("\n").filter((line) => line.includes("msg agent register")).length;
  try {
    factotumMsg(pi);
    // Race the start path so a regression that awaits the retry loop fails the
    // assertion instead of hanging the suite.
    const started = handlers.get("session_start")();
    const outcome = await Promise.race([
      started.then(() => "returned"),
      new Promise((resolve) => setTimeout(() => resolve("held open"), 500)),
    ]);
    assert.equal(outcome, "returned", "session_start must resolve while registration keeps failing");
    await waitFor(() => attempts() >= 2, 2000);
    assert.ok(
      attempts() >= 2,
      `register attempts = ${attempts()}; the failed registration must keep retrying after session_start returns`,
    );
  } finally {
    // A receiver left retrying keeps the test process alive, so shut it down even
    // when an assertion above failed.
    await handlers.get("session_shutdown")();
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
});

test("receiverEnv pins the configured store onto the child ft environment", () => {
  // Without the pin a receiver whose host environment was replaced registers
  // against the store its host resolves (the machine's default) instead of the
  // run's throwaway one.
  const env = receiverEnv({ store: "jsonfile", storeOpts: "path=/tmp/db.json" }, { HOME: "/home/u", FACTOTUM_STORE: "sqlite" });
  assert.deepEqual(env, { HOME: "/home/u", FACTOTUM_STORE: "jsonfile", FACTOTUM_STORE_OPTS: "path=/tmp/db.json" });
  assert.deepEqual(receiverEnv({}, { HOME: "/home/u" }), { HOME: "/home/u" });
});

test("receiverEnv names the receiver's actor as each child ft's default actor", () => {
  // Every `ft` the receiver spawns is a child of pi, not of a shell that named an
  // identity: with none in its environment the CLI falls back to its agent
  // heuristic and warns "probable agent caller ... this invocation stays
  // unattributed" on every register/claim/ack, and that warning lands inside the
  // receiver's own log lines. The receiver knows its actor, so it names it as the
  // child's default actor - the default-actor path, not FACTOTUM_CALLER, which
  // resolves only against registered Actor records and would drop the loop's role
  // actors with a warning of their own.
  const env = receiverEnv({ actor: "builder-t-1" }, { HOME: "/home/u", FACTOTUM_DEFAULT_ACTOR: "stale" });
  assert.deepEqual(env, { HOME: "/home/u", FACTOTUM_DEFAULT_ACTOR: "builder-t-1" });
});

test("a plugin-spawned ft child reads the receiver's actor from its environment", async () => {
  // The pin above, over a real child process: this is the transport the receiver
  // actually uses (selectCall -> spawnRunner -> receiverEnv), so the assertion is
  // over the environment an ft binary would read, not a helper's return value.
  const dir = mkdtempSync(join(tmpdir(), "ft-pi-receiver-env-"));
  const stub = join(dir, "ft-stub.js");
  writeFileSync(
    stub,
    "#!/usr/bin/env node\n" +
      "process.stdout.write(JSON.stringify({ run_id: \"run-1\", child_actor: process.env.FACTOTUM_DEFAULT_ACTOR || \"\" }));\n",
    { mode: 0o755 },
  );
  const config = { ...BASE_CONFIG, actor: "builder-t-1", bin: stub };
  const out = await selectCall(config)({
    op: "register", actor: config.actor, harness: config.harness, pid: 1, ttl: "1000ms",
  });
  assert.equal(out?.child_actor, "builder-t-1");
});
