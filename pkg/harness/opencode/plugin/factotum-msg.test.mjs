import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import plugin, {
  agentRecordKey,
  cliCall,
  configFromEnv,
  createFactotumReceiver,
  formatMessage,
  httpCall,
  makeContextInjector,
  makeInjector,
  msgSessionKey,
  msgSessionStore,
  normalizeMsgURL,
  parseJSON,
  receiverEnv,
  selectCall,
  setupV2,
  subscribeContextEvents,
} from "./factotum-msg.js";

const BASE_CONFIG = {
  project: "prj-1",
  actor: "act-1",
  task: "t-1",
  session: "s-chief",
  bin: "ft",
  harness: "opencode",
  host: "",
  pid: 7,
  waitMs: 25000,
  ttlMs: 90000,
  retryMs: 5,
  sessionWaitMs: 30,
  msgUrl: "",
  token: "",
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

// deferred is a promise whose settlement the test controls, standing in for a
// round trip that never returns (a wedged claim or injection).
function deferred() {
  let resolve;
  const promise = new Promise((settle) => { resolve = settle; });
  return { promise, resolve };
}

// STALL_WAIT_MS is the long-poll window the stall-then-resume tests hand the
// receiver, so the lease gate samples 2 * it (the stall bound).
//
// These tests used to run with waitMs 10 - a 20ms bound chosen only to make the
// stall cheap - and lost the resume assertion under load. The bound is wall-clock
// and it is sampled, so it has to absorb the gap between the receiver recording a
// round trip and the next statement reading the clock. Under a storm of four
// concurrent race suites plus 16 CPU hogs that gap reached 145ms between two
// adjacent statements, and the 20ms bound missed 2 of 1500 samples while 100ms
// and every bound above it missed none. The derived 600ms bound is ~4x the
// measured worst gap.
const STALL_WAIT_MS = 300;
// STALL_PAST_MS clears the stall bound (2 * waitMs) by a margin. Derived, not a
// literal: raising waitMs past 350 must lengthen the sleep with it rather than
// silently leave the sleep inside the bound, which would flip what these tests
// assert. A real sleep only ever runs long, so this side of the race is safe at
// any load.
const STALL_PAST_MS = 2 * STALL_WAIT_MS + 100;

// resumeRenewal is the log sink for a stall-then-resume test: it runs one
// heartbeat tick in the same turn the receiver records the resumed round trip,
// which is the moment the lease gate must see progress.
//
// The tick is driven by the event rather than by a manual tick after
// waitFor(...) because waitFor polls every 5ms and a starved event loop then
// lands 200ms past the resume (measured p100 under the same storm) - the stall
// bound would have to absorb a hop the test does not need to take.
function resumeRenewal(logs, getTick) {
  let renew;
  const renewed = new Promise((resolve) => { renew = resolve; });
  return {
    log(entry) {
      logs.push(entry);
      if (!/resumed/.test(entry)) return;
      // A getter, not the tick: the receiver arms its interval during start(),
      // after the test has handed this sink over.
      const tick = getTick();
      renew(tick());
    },
    renewed,
  };
}

function receiverFor(claimResults, injected, { inject, loadSession, saveSession } = {}) {
  const { run, calls } = fakeRun(claimResults);
  let intervalCleared = false;
  const receiver = createFactotumReceiver({
    config: BASE_CONFIG,
    call: cliCall(BASE_CONFIG, run),
    inject: inject || (async (sid, text) => { injected.push({ sid, text }); }),
    loadSession,
    saveSession,
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

test("default export carries both the V1 server() and the V2 id + setup()", () => {
  assert.equal(typeof plugin.server, "function");
  assert.equal(typeof plugin.setup, "function");
  assert.equal(plugin.id, "factotum.msg");
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
  assert.equal(config.harness, "opencode");
  assert.equal(config.waitMs, 25000);
  assert.equal(config.ttlMs, 90000);
});

test("configFromEnv reports the harness the run selected", () => {
  // `ft run` exports the harness it drives; a V2 run must register as
  // opencode2 rather than this receiver's opencode default.
  const config = configFromEnv({ FACTOTUM_PROJECT: "prj", FACTOTUM_ACTOR: "act", FACTOTUM_HARNESS: "opencode2" });
  assert.equal(config.harness, "opencode2");
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

  await call({ op: "register", actor: "act-1", session: "s-1", harness: "opencode", host: "host-a", pid: 7, ttl: "90000ms", task: "t-1" });
  await call({ op: "claim", run: "run-1", actor: "act-1", wait: "25000ms", task: "t-1" });
  await call({ op: "ack", id: "m-1", run: "run-1", state: "read", error: "" });
  await call({ op: "nack", id: "m-2", run: "run-1", error: "no session" });
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
    actor: "act-1", task: "t-1", session: "s-1", harness: "opencode", host: "host-a", pid: 7, ttl: "90000ms",
  });
  assert.deepEqual(JSON.parse(requests[1].options.body), {
    run_id: "run-1", actor: "act-1", task: "t-1", wait: "25000ms",
  });
  assert.deepEqual(JSON.parse(requests[2].options.body), {
    id: "m-1", run_id: "run-1", state: "read", error: "",
  });
  assert.deepEqual(JSON.parse(requests[3].options.body), {
    id: "m-2", run_id: "run-1", reason: "no session",
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
    inject: async (sid, text) => { injected.push({ sid, text }); },
    setIntervalFn: () => 1,
    clearIntervalFn: () => {},
  });
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
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
  const viaHTTP = await httpSelected({ op: "register", actor: "act-1", harness: "opencode", pid: 7, ttl: "1ms" });
  assert.equal(viaHTTP.run_id, "run-http");
  assert.deepEqual(fetched, ["http://hub:8484/api/msg/register"]);

  const cliSelected = selectCall(BASE_CONFIG, { run: async () => ({ code: 0, stdout: '{"run_id":"run-cli"}', stderr: "" }) });
  const viaCLI = await cliSelected({ op: "register", actor: "act-1", harness: "opencode", pid: 7, ttl: "1ms" });
  assert.equal(viaCLI.run_id, "run-cli");
});

test("formatMessage names the sender and task", () => {
  const text = formatMessage({ from: "act-x", task_id: "t-9", body: "hello" });
  assert.match(text, /from act-x/);
  assert.match(text, /task t-9/);
  assert.match(text, /hello/);
});

test("receiver registers, claims, injects, and acks", async () => {
  const injected = [];
  const { receiver, calls, cleared } = receiverFor(
    [{ found: true, message: { id: "m-1", from: "act-x", task_id: "t-1", body: "hello" } }, { found: false }],
    injected,
  );
  receiver.onEvent({ properties: { sessionID: "ses-1" } });

  await receiver.start();
  assert.equal(receiver.runID(), "run-1");
  assert.ok(calls.some((c) => c === "msg agent register --actor act-1 --project prj-1 --harness opencode --pid 7 --ttl 90000ms --session s-chief --task t-1 -o json"));

  await waitFor(() => injected.length === 1);
  assert.equal(injected[0].sid, "ses-1");
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
  await receiver.start();

  await waitFor(() => calls.some((c) => c.includes("--state failed")));
  assert.ok(calls.some((c) => c.includes("msg agent ack m-2") && c.includes("model down")));
  await receiver.dispose();
});

test("receiver does not claim without a session, then delivers once one arrives", async () => {
  const injected = [];
  const { receiver, calls } = receiverFor(
    [{ found: true, message: { id: "m-3", body: "hi" } }, { found: false }],
    injected,
  );
  await receiver.start();
  // No session event is fed: the receiver must not claim, because a claimed
  // message it cannot inject would have to be dropped or burn an attempt.
  await new Promise((resolve) => setTimeout(resolve, 150));
  assert.equal(calls.some((c) => c.includes("msg agent claim")), false, "must not claim without a session");
  assert.equal(calls.some((c) => c.includes("--state failed")), false, "must not fail/consume the message");

  // Once a session is announced, the queued message is claimed, injected, and
  // acked read - the handoff is delivered, not lost.
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
  await waitFor(() => injected.length === 1);
  assert.equal(injected[0].sid, "ses-1");
  assert.ok(calls.some((c) => c.includes("msg agent ack m-3") && c.includes("--state read")));
  await receiver.dispose();
});

test("receiver restores its session on start and delivers without an event", async () => {
  // The reload case: OpenCode reloads the plugin when its file changes, so the
  // new receiver has no session and, if the agent is idle, no event will ever
  // announce one. A restored session must let it claim and deliver anyway.
  const injected = [];
  const { receiver, calls } = receiverFor(
    [{ found: true, message: { id: "m-4", from: "act-x", body: "resumed" } }, { found: false }],
    injected,
    { loadSession: async () => "ses-restored" },
  );
  await receiver.start();
  await waitFor(() => injected.length === 1);
  assert.equal(injected[0].sid, "ses-restored");
  assert.ok(calls.some((c) => c.includes("msg agent ack m-4") && c.includes("--state read")));
  await receiver.dispose();
});

test("receiver persists a learned session for the next load", async () => {
  const saved = [];
  const { receiver } = receiverFor([{ found: false }], [], { saveSession: async (sid) => saved.push(sid) });
  receiver.onEvent({ data: { sessionID: "ses-1" } });
  await waitFor(() => saved.length === 1);
  assert.deepEqual(saved, ["ses-1"]);

  // A repeated event for the same session is not persisted again.
  receiver.onEvent({ data: { sessionID: "ses-1" } });
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.deepEqual(saved, ["ses-1"]);
  await receiver.dispose();
});

test("msgSessionStore resumes only the current process's live session", async () => {
  const store = new Map();
  const ctx = {
    storage: {
      get: async (key) => store.get(key),
      set: async (key, value) => { store.set(key, value); },
    },
    session: {
      async get({ sessionID }) {
        if (sessionID === "ses-live") return { id: sessionID };
        throw new Error("Session.NotFoundError");
      },
    },
  };
  const glean = msgSessionStore(ctx, BASE_CONFIG);
  assert.equal(await glean.load(), "", "nothing persisted yet");

  await glean.save("ses-live");
  assert.equal(await glean.load(), "ses-live");

  // A stale id whose session is gone is never resumed: the receiver would
  // otherwise claim a message and inject it into a dead session.
  await glean.save("ses-dead");
  assert.equal(await glean.load(), "");

  // The key is scoped to the process, so a reload (same pid) resumes but a new
  // agent launch (a new pid) does not.
  assert.notEqual(msgSessionKey(BASE_CONFIG), msgSessionKey({ ...BASE_CONFIG, pid: BASE_CONFIG.pid + 1 }));
});

test("msgSessionKey never lets one part's content shift the boundary", () => {
  // A dot inside a part must not be able to stand in for the separator between
  // parts: "a.b" + "c" and "a" + "b.c" are different agents.
  assert.notEqual(
    msgSessionKey({ ...BASE_CONFIG, project: "a.b", actor: "c" }),
    msgSessionKey({ ...BASE_CONFIG, project: "a", actor: "b.c" }),
  );

  // Deriving the key twice from the same config yields the same key, so a
  // plugin reload (same pid) still finds the session it persisted.
  assert.equal(msgSessionKey(BASE_CONFIG), msgSessionKey({ ...BASE_CONFIG }));
});

test("msgSessionStore still resumes a session saved under the legacy dotted key", async () => {
  const store = new Map();
  const live = new Set(["ses-legacy", "ses-current"]);
  const ctx = {
    storage: {
      get: async (key) => store.get(key),
      set: async (key, value) => { store.set(key, value); },
    },
    session: {
      async get({ sessionID }) {
        if (live.has(sessionID)) return { id: sessionID };
        throw new Error("Session.NotFoundError");
      },
    },
  };

  // A receiver running before the length-prefixed format persisted the session
  // under the dotted key; a reload after the upgrade must still find it.
  store.set(`session.${BASE_CONFIG.project}.${BASE_CONFIG.actor}.${BASE_CONFIG.pid}`, "ses-legacy");
  const glean = msgSessionStore(ctx, BASE_CONFIG);
  assert.equal(await glean.load(), "ses-legacy");

  // Once a session is saved under the new key, it wins over the legacy one.
  await glean.save("ses-current");
  assert.equal(await glean.load(), "ses-current");
});

test("msgSessionStore does not read the legacy key when a part is dotted", async () => {
  const store = new Map();
  const ctx = {
    storage: {
      get: async (key) => store.get(key),
      set: async (key, value) => { store.set(key, value); },
    },
    session: { async get({ sessionID }) { return { id: sessionID }; } },
  };
  // The dotted config "a.b"/"c" shares its legacy key with a different agent,
  // "a"/"b.c"; falling back to it would inject into the wrong session.
  store.set("session.a.b.c.7", "ses-other");
  const dotted = { ...BASE_CONFIG, project: "a.b", actor: "c" };
  const glean = msgSessionStore(ctx, dotted);
  assert.equal(await glean.load(), "");

  // The collision case is only covered if the store can read at all: a load
  // that always returned "" would satisfy the assertion above. A save under the
  // length-prefixed key must round-trip even with a dotted part, and it must
  // beat the legacy key that belongs to the other agent.
  await glean.save("ses-mine");
  assert.equal(await glean.load(), "ses-mine");
});

test("makeInjector prefers promptAsync and falls back to prompt", async () => {
  const asyncCalls = [];
  await makeInjector({ session: { promptAsync: async (options) => asyncCalls.push(options) } })("ses-1", "hi");
  assert.deepEqual(asyncCalls, [{ path: { id: "ses-1" }, body: { parts: [{ type: "text", text: "hi" }] } }]);

  const syncCalls = [];
  await makeInjector({ session: { prompt: async (options) => syncCalls.push(options) } })("ses-2", "yo");
  assert.equal(syncCalls[0].path.id, "ses-2");

  await assert.rejects(makeInjector({})("ses-3", "hi"), /unavailable/);
});

test("onEvent captures the sessionID from the V1 and V2 event shapes", () => {
  const { receiver } = receiverFor([], []);
  receiver.onEvent({ properties: { sessionID: "ses-v1" } });
  assert.equal(receiver.sessionID(), "ses-v1");
  receiver.onEvent({ data: { sessionID: "ses-v2" } });
  assert.equal(receiver.sessionID(), "ses-v2");
  receiver.onEvent({ data: {} });
  assert.equal(receiver.sessionID(), "ses-v2");
});

test("makeContextInjector prompts the V2 context session with the text", async () => {
  const calls = [];
  await makeContextInjector({ prompt: async (options) => calls.push(options) })("ses-1", "hi");
  assert.deepEqual(calls, [{ sessionID: "ses-1", text: "hi" }]);

  await assert.rejects(makeContextInjector({})("ses-2", "hi"), /unavailable/);
});

test("subscribeContextEvents forwards events and aborts on cleanup", async () => {
  const seen = [];
  let aborted = false;
  const controller = { signal: "sig", abort: () => { aborted = true; } };
  const ctx = {
    event: {
      async *subscribe({ signal }) {
        assert.equal(signal, "sig");
        yield { data: { sessionID: "ses-v2" } };
        await new Promise(() => {});
      },
    },
  };
  const stop = subscribeContextEvents(ctx, (event) => seen.push(event), { controller });
  await waitFor(() => seen.length === 1);
  stop();
  assert.equal(aborted, true);
});

test("setupV2 is inert without a project or actor and a recorded identity", async () => {
  const calls = [];
  const run = async (argv) => {
    calls.push(argv.join(" "));
    return { code: 0, stdout: "[]", stderr: "" };
  };
  const cleanup = setupV2(restoreCtx(new Map()), { env: {}, run });
  assert.equal(typeof cleanup, "function", "the V2 entrypoint still returns a cleanup for the host to call");
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.deepEqual(calls, [], "an ordinary OpenCode session must not shell out to ft");
  cleanup();
});

test("setupV2 registers, subscribes, injects a claimed message, and disposes", async () => {
  const injected = [];
  const env = { FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1", FACTOTUM_MSG_SESSION_WAIT_MS: "50" };
  const config = configFromEnv(env);
  const { run, calls } = fakeRun([
    { found: true, message: { id: "m-1", from: "act-x", body: "hi v2" } },
    { found: false },
  ]);
  const ctx = {
    session: { prompt: async (options) => { injected.push(options); } },
    event: {
      async *subscribe() {
        yield { data: { sessionID: "ses-v2" } };
        await new Promise(() => {});
      },
    },
  };

  const cleanup = setupV2(ctx, { env, call: cliCall(config, run) });
  await waitFor(() => injected.length === 1);
  assert.deepEqual(injected[0], { sessionID: "ses-v2", text: "[ft msg from act-x]\nhi v2" });
  assert.ok(calls.some((c) => c.includes("msg agent register --actor act-1")));
  assert.ok(calls.some((c) => c.includes("msg agent ack m-1") && c.includes("--state read")));

  await cleanup();
  assert.ok(calls.some((c) => c.includes("msg agent deregister")));
});

// --- receiver liveness (t-vo7dpanzuu) -------------------------------------
//
// A loop agent can sit TUI-alive and idle with an inert receiver: no claim, no
// run, every handoff quietly queued. These pin the three silent paths that leave
// it in that state and the signal that makes it visible without a screen.

test("a failed register is retried so a transient error cannot leave the receiver silent", async (t) => {
  // Registration used to be a single attempt whose failure start() rejected;
  // the entrypoints swallow that rejection, so the agent kept a healthy TUI, no
  // run, no claim loop and no retry - forever.
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
    inject: async (sid, text) => { injected.push({ sid, text }); },
    setIntervalFn: () => 1,
    clearIntervalFn: () => {},
  });
  t.after(() => receiver.dispose());
  receiver.onEvent({ properties: { sessionID: "ses-1" } });

  await receiver.start();
  await waitFor(() => injected.length === 1);
  assert.ok(attempts >= 2, `register attempts = ${attempts}; want a retry after the failed first attempt`);
  assert.match(injected[0].text, /after retry/);
});

test("a claim round trip that never returns stops renewing the lease", async (t) => {
  // A wedged claim (a hung local ft, or a remote hub stalling mid-poll with no
  // AbortSignal) keeps the loop *running* without completing a round trip. Health
  // cannot be "the loop promise exists": that is the live-looking peer that
  // quietly queues the reviewer reproduced.
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
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
  // Same stall class from the other side: the claim succeeded, but the session
  // prompt never settles, so nothing is being delivered even though the loop is
  // alive.
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
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

test("an inert receiver stops renewing its lease so the hub sees it stale, not healthy", async (t) => {
  // `ft msg runs --live` reads liveness from the lease. A receiver with no
  // session cannot claim, so renewing the lease would advertise a peer that is
  // silently queueing every handoff - the failure this task is about.
  const calls = [];
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
        return { code: 0, stdout: JSON.stringify({ found: false }), stderr: "" };
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const receiver = createFactotumReceiver({
    config: BASE_CONFIG,
    call: cliCall(BASE_CONFIG, run),
    inject: async () => {},
    setIntervalFn: (fn) => { tick = fn; return 1; },
    clearIntervalFn: () => {},
  });
  t.after(() => receiver.dispose());
  await receiver.start();
  assert.equal(typeof tick, "function", "start must arm the heartbeat");

  // No session is ever announced: the receiver is parked, so it must not renew.
  for (let i = 0; i < 3; i += 1) await tick();
  assert.equal(
    calls.some((c) => c.includes("msg agent heartbeat")),
    false,
    "a receiver that cannot claim must let its lease lapse instead of looking live",
  );

  // Once a session arrives the loop claims, and the lease is renewed again.
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
  await waitFor(() => calls.some((c) => c.includes("msg agent claim")));
  await tick();
  await waitFor(() => calls.some((c) => c.includes("msg agent heartbeat")), 500);
});

test("a parked receiver keeps retrying its persisted session instead of waiting for an event", async (t) => {
  // A reload can find no session for a moment: the previous receiver's save is
  // fire-and-forget, and storage can become readable after the first read. An
  // idle agent gets no event, so a single failed lookup strands the receiver.
  const injected = [];
  let loads = 0;
  const { receiver, calls } = receiverFor(
    [{ found: true, message: { id: "m-late", from: "act-x", body: "late session" } }, { found: false }],
    injected,
    {
      loadSession: async () => {
        loads += 1;
        return loads >= 3 ? "ses-late" : "";
      },
    },
  );

  t.after(() => receiver.dispose());
  await receiver.start();
  await waitFor(() => injected.length === 1, 2000);
  assert.equal(injected[0].sid, "ses-late");
  assert.ok(loads >= 3, `session loads = ${loads}; want the parked receiver to retry`);
  assert.ok(calls.some((c) => c.includes("msg agent ack m-late") && c.includes("--state read")));
});

test("the V2 entrypoint logs receiver state so an inert session leaves evidence", async (t) => {
  // The observed failures were diagnosed from a screen because the receiver said
  // nothing. Registration and the session-less park must both leave a log line.
  const entries = [];
  const env = { FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1", FACTOTUM_MSG_RETRY_MS: "5" };
  const config = configFromEnv(env);
  const { run } = fakeRun([]);
  const ctx = {
    session: { prompt: async () => {} },
    event: { subscribe: async function* () { await new Promise(() => {}); } },
  };

  const cleanup = setupV2(ctx, { env, call: cliCall(config, run), log: (entry) => entries.push(entry) });
  t.after(() => cleanup());
  await waitFor(() => entries.some((entry) => /registered actor act-1 as run run-1/.test(entry)), 2000);
  await waitFor(() => entries.some((entry) => /no opencode session to inject into/.test(entry)), 2000);

  assert.ok(entries.length >= 2, `log entries = ${JSON.stringify(entries)}; want registration and the session-less park`);
});

// --- forgotten run and failing claims (t-rmws6mklmo) -----------------------
//
// The liveness work above closed the false-positive direction (an inert receiver
// that still reads live). These are the two false-negative directions: a receiver
// that IS delivering can stay out of `ft msg runs --live` forever once the hub
// forgets its run, and a receiver whose every claim fails keeps its lease fresh
// while claiming nothing.

// heartbeatReceiver wires a receiver whose single claim never returns, so the
// only traffic a test steers is the heartbeat tick. The long window keeps manual
// ticks well inside the stall bound; a test that wants a stall shortens waitMs.
// log replaces the default collect-into-logs sink (a stall/resume test passes
// resumeRenewal's).
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
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

test("a remote hub's 404 heartbeat also re-registers the run", async (t) => {
  // The transport must not change the rule: a hub answers a forgotten run with
  // 404, which is the same "answer not_found" as the local CLI's error.
  const registers = [];
  let tick = null;
  const fetchFn = async (url, options) => {
    const body = JSON.parse(options.body);
    if (url.endsWith("/register")) {
      registers.push(body.actor);
      return jsonResponse({ run_id: `run-${registers.length}` });
    }
    if (url.endsWith("/claim")) return new Promise(() => {});
    if (url.endsWith("/heartbeat")) {
      return body.run_id === "run-1" ? jsonResponse({ error: "msg: not found" }, 404) : jsonResponse({ ok: true });
    }
    return jsonResponse({ ok: true });
  };
  const config = { ...BASE_CONFIG, msgUrl: "http://hub:8484", token: "s3cret" };
  const logs = [];
  const receiver = createFactotumReceiver({
    config,
    call: httpCall(config, { fetchFn }),
    inject: async () => {},
    setIntervalFn: (fn) => { tick = fn; return 1; },
    clearIntervalFn: () => {},
    log: (entry) => logs.push(entry),
  });
  t.after(() => receiver.dispose());
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
  await receiver.start();
  await tick();
  await waitFor(() => receiver.runID() === "run-2");
  assert.deepEqual(registers, ["act-1", "act-1"]);
  assert.ok(logs.some((entry) => /re-registered actor act-1 as run run-2/i.test(entry)));
});

// claimingStore fakes the receiver's side of one claim against a store that
// enforces the real ownership rule: only the run recorded on the claim may finalize
// it. The owner is read from the claim's own --run, exactly as the store records
// it, so a receiver that acks with any other id is rejected here as it is there.
// The claim loop feeds one message and then parks; registers are numbered so a
// re-register mints a new run id like the hub does, and the hub has forgotten
// run-1 (its heartbeat answers not_found).
function claimingStore(message) {
  const calls = [];
  const owners = new Map();
  const states = new Map();
  const acks = [];
  let claimed = false;
  let registers = 0;
  const flag = (argv, name) => argv[argv.indexOf(name) + 1];
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        registers += 1;
        return { code: 0, stdout: JSON.stringify({ run_id: `run-${registers}` }), stderr: "" };
      case "heartbeat":
        if (flag(argv, "--run") === "run-1") {
          return { code: 1, stdout: "", stderr: "Error: not found: run run-1" };
        }
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
      case "claim": {
        if (claimed) return new Promise(() => {});
        claimed = true;
        owners.set(message.id, flag(argv, "--run"));
        states.set(message.id, "delivered");
        return { code: 0, stdout: JSON.stringify({ found: true, message: { ...message, state: "delivered" } }), stderr: "" };
      }
      case "ack": {
        const id = argv[3];
        const ackRun = flag(argv, "--run");
        const state = flag(argv, "--state");
        acks.push({ id, run: ackRun, state });
        if (owners.get(id) !== ackRun) {
          return { code: 1, stdout: "", stderr: `Error: invalid: message ${id} is not owned by run ${ackRun}` };
        }
        states.set(id, state);
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
      }
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  return { run, calls, states, acks };
}

test("a claim in flight across a not_found re-register is acked under the run that claimed it", async (t) => {
  // A hub restored from a backup forgets the run, so the heartbeat re-registers
  // under a new id while a claim is still being delivered. The store expects the
  // ack from the run it recorded on that claim; acking with the re-registered id
  // is rejected, and the message stays delivered under the forgotten run with a
  // lapsed lease - invisible to both ends until a later registration sweeps it.
  const store = claimingStore({ id: "msg-1", body: "after the hub forgot us" });
  const logs = [];
  const release = deferred();
  let tick = null;
  const config = { ...BASE_CONFIG, waitMs: 60000 };
  const receiver = createFactotumReceiver({
    config,
    call: cliCall(config, store.run),
    inject: async () => { await release.promise; },
    setIntervalFn: (fn) => { tick = fn; return 1; },
    clearIntervalFn: () => {},
    log: (entry) => logs.push(entry),
  });
  t.after(() => receiver.dispose());
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
  await receiver.start();
  await waitFor(() => store.states.get("msg-1") === "delivered");

  // The claim is delivered but not yet acked: the hub forgets run-1, so the
  // heartbeat re-registers this receiver under run-2 while that claim is in flight.
  await tick();
  await waitFor(() => receiver.runID() === "run-2");

  release.resolve();
  await waitFor(() => store.states.get("msg-1") === "read");

  assert.deepEqual(store.acks, [{ id: "msg-1", run: "run-1", state: "read" }], "the in-flight claim is acked under the run that claimed it");
  assert.ok(
    logs.some((entry) => /acked message msg-1 as read under run run-1/.test(entry)),
    `logs = ${JSON.stringify(logs)}; want the cross-run ack logged`,
  );
  assert.ok(
    !logs.some((entry) => /ack of message msg-1/i.test(entry)),
    `logs = ${JSON.stringify(logs)}; a successful ack must not log a rejection`,
  );
});

test("a rejected ack is logged once with the message id and reason instead of swallowed", async (t) => {
  // The turn was injected but the store rejects the ack (here: the lease lapsed
  // and a sweep or another run took the message back). The receiver used to
  // retry as failed and swallow both rejections, so the sender believed the
  // handoff landed while the message sat delivered with nobody checking it. One
  // line with the message id and reason is the evidence it did not.
  const calls = [];
  const logs = [];
  let claimed = false;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: "run-1" }), stderr: "" };
      case "claim":
        if (claimed) return new Promise(() => {});
        claimed = true;
        return { code: 0, stdout: JSON.stringify({ found: true, message: { id: "msg-1", body: "boom" } }), stderr: "" };
      case "ack":
        return { code: 1, stdout: "", stderr: "Error: invalid: message msg-1 is not owned by run run-1" };
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  const receiver = createFactotumReceiver({
    config: BASE_CONFIG,
    call: cliCall(BASE_CONFIG, run),
    inject: async () => {},
    setIntervalFn: () => 1,
    clearIntervalFn: () => {},
    log: (entry) => logs.push(entry),
  });
  t.after(() => receiver.dispose());
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
  await receiver.start();
  await waitFor(() => logs.some((entry) => /ack of message msg-1/i.test(entry)));
  // Give a would-be second (failed) ack every chance to show up before asserting.
  await waitFor(() => calls.filter((c) => c.includes("msg agent claim")).length >= 2);

  const ackLogs = logs.filter((entry) => /ack of message msg-1/i.test(entry));
  assert.equal(ackLogs.length, 1, `logs = ${JSON.stringify(logs)}; want exactly one ack-rejection line`);
  assert.match(ackLogs[0], /msg-1/);
  assert.match(ackLogs[0], /not owned by run run-1/);
  assert.equal(
    calls.filter((c) => c.includes("msg agent ack")).length,
    1,
    "a rejected read ack is not retried as a failed ack; the message stays delivered for the sweep",
  );
});

test("a run of failing claims is no progress: the stall line fires and the lease lapses", async (t) => {
  // A permanently failing claim (a broken store, a rejected actor) used to settle
  // the round trip in the catch: the loop looked like it was progressing, so the
  // lease stayed fresh and the run read healthy while every handoff sat queued.
  // Health is a *completed* round trip; a claim that always throws completes none.
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
  await receiver.start();
  await waitFor(() => claims >= 2);

  await tick();
  assert.ok(
    calls.some((c) => c.includes("msg agent heartbeat")),
    `one failed claim inside the stall window is not a stall; calls = ${JSON.stringify(calls)}`,
  );
});

// --- a store outage must not flood the log (t-4fr25qsnc4) -------------------
//
// During an outage every claim fails, once per retry delay, and each failure
// embeds the store's own (often multi-line) error. One entry per retry buries
// the one line an operator reads the log for - the stall line that says this
// receiver is not delivering - so the receiver logs the first failure in full
// and then a periodic summary of the run.

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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
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
  // - while every heartbeat, once per ttl/3, fails. That is the other half of the
  // QA's log flood, and it gets the same bound: the first failure in full, one
  // summary per 20 consecutive failures, one closing line when renewal works
  // again, and the next outage logs its first failure afresh.
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
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

// Registration is the third repeating failure path: the store being down at
// startup makes every attempt fail once per retry delay, before a run exists.
// start() owns that loop (registerWithRetry), so the outage bound is pinned
// there: one full failure, one line naming the state, then periodic summaries.

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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });
  // start() cannot resolve while registration keeps failing; the loop is what
  // is under test, and dispose() ends it.
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
  receiver.onEvent({ properties: { sessionID: "ses-1" } });

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

test("setupV2 resumes the persisted session after a reload and delivers without an event", async () => {
  const injected = [];
  const store = new Map();
  const env = { FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1" };
  const config = configFromEnv(env);
  const { run, calls } = fakeRun([
    { found: true, message: { id: "m-1", from: "act-x", body: "after reload" } },
    { found: false },
  ]);
  const ctx = {
    session: {
      prompt: async (options) => { injected.push(options); },
      get: async ({ sessionID }) => ({ id: sessionID }),
    },
    storage: {
      get: async (key) => store.get(key),
      set: async (key, value) => { store.set(key, value); },
    },
    // A reloaded idle agent gets no session event at all.
    event: { subscribe: async function* () { await new Promise(() => {}); } },
  };
  // A previous load persisted the session before the plugin was reloaded.
  await ctx.storage.set(msgSessionKey(config), "ses-prev");

  const cleanup = setupV2(ctx, { env, call: cliCall(config, run) });
  await waitFor(() => injected.length === 1);
  assert.deepEqual(injected[0], { sessionID: "ses-prev", text: "[ft msg from act-x]\nafter reload" });
  assert.ok(calls.some((c) => c.includes("msg agent ack m-1") && c.includes("--state read")));

  await cleanup();
});

// --- cold restore (t-fbeydkulit) ------------------------------------------
//
// OpenCode V2 cold-restores a surviving session as `opencode -s <session>` with
// the original launch environment gone, so FACTOTUM_* never reaches the plugin:
// it registers nothing and every hand-off queues behind a TUI that looks
// healthy. The receiver therefore records the identity it registered as (and the
// session it injects into) for its location, and a load with no environment
// recovers it - but only while no live receiver holds that actor, so opening the
// same checkout by hand can never split a running agent's mailbox.

const LOCATION = { directory: "/work/prj-1", project: { id: "loc-1" } };

// restoreCtx is one OpenCode instance's plugin context. A cold restore is a new
// context over the same durable storage: a new process, the same location.
function restoreCtx(store, { injected = [], sessionID = "", get } = {}) {
  return {
    location: LOCATION,
    session: {
      prompt: async (options) => { injected.push(options); },
      get: get || (async ({ sessionID: id }) => ({ id })),
    },
    storage: {
      get: async (key) => store.get(key),
      set: async (key, value) => { store.set(key, value); },
    },
    event: {
      async *subscribe() {
        if (sessionID) yield { data: { sessionID } };
        await new Promise(() => {});
      },
    },
  };
}

// restoreRun is the stub ft a restore test shells out to. `runs` is what
// `ft msg runs` reports (the liveness probe); a claim of runID yields `message`
// once, then parks.
function restoreRun({ runID = "run-1", runs = [], message } = {}) {
  const calls = [];
  let claimed = false;
  const run = async (argv) => {
    calls.push(argv.join(" "));
    if (argv[1] === "runs") return { code: 0, stdout: JSON.stringify(runs), stderr: "" };
    switch (argv[2]) {
      case "register":
        return { code: 0, stdout: JSON.stringify({ run_id: runID }), stderr: "" };
      case "claim":
        if (message && !claimed) {
          claimed = true;
          return { code: 0, stdout: JSON.stringify({ found: true, message }), stderr: "" };
        }
        return new Promise(() => {});
      default:
        return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    }
  };
  return { run, calls };
}

function recorded(store) {
  return store.get(agentRecordKey({ location: LOCATION }));
}

test("a receiver records the identity and session a cold-restored host must resume", async (t) => {
  const store = new Map();
  const env = {
    FACTOTUM_PROJECT: "prj-1",
    FACTOTUM_ACTOR: "act-1",
    FACTOTUM_TASK_ID: "t-1",
    FACTOTUM_BIN: "/opt/ft",
    FACTOTUM_STORE: "jsonfile",
    FACTOTUM_STORE_OPTS: "path=/tmp/db.json",
    FACTOTUM_SESSION: "s-chief",
    FACTOTUM_SERVE_TOKEN: "secret",
  };
  const { run } = restoreRun();

  const cleanup = setupV2(restoreCtx(store, { sessionID: "ses-1" }), { env, run });
  t.after(cleanup);
  await waitFor(() => recorded(store)?.sessionID === "ses-1", 2000);

  const record = recorded(store);
  assert.equal(record.project, "prj-1");
  assert.equal(record.actor, "act-1");
  assert.equal(record.task, "t-1");
  assert.equal(record.bin, "/opt/ft");
  assert.equal(record.store, "jsonfile", "the store the run was launched with survives the restore");
  assert.equal(record.storeOpts, "path=/tmp/db.json");
  assert.equal(record.session, "s-chief", "the chief session the run belongs to survives the restore");
  assert.equal(record.token, undefined, "a bearer token is never written to durable storage");
  assert.equal(record.version, undefined, "the record carries only what a restore reads");
  assert.equal(record.savedAt, undefined, "the record carries only what a restore reads");
});

test("a cold-restored host re-registers from the recorded identity and delivers", async (t) => {
  const store = new Map();
  const env = { FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1", FACTOTUM_SESSION: "s-chief" };
  const first = restoreRun();
  const cleanupFirst = setupV2(restoreCtx(store, { sessionID: "ses-restored" }), { env, run: first.run });
  t.after(cleanupFirst);
  await waitFor(() => recorded(store)?.sessionID === "ses-restored", 2000);

  // The server restarts and resumes the session as `opencode -s <session>`: same
  // location and OpenCode storage, but no FACTOTUM_* in the environment.
  const injected = [];
  const second = restoreRun({ runID: "run-2", message: { id: "m-restore", from: "act-x", body: "after the restart" } });
  const cleanupRestored = setupV2(restoreCtx(store, { injected }), { env: {}, run: second.run });
  t.after(cleanupRestored);

  await waitFor(() => injected.length === 1, 2000);
  assert.deepEqual(injected[0], { sessionID: "ses-restored", text: "[ft msg from act-x]\nafter the restart" });
  assert.ok(second.calls.some((c) => c.includes("msg agent register") && c.includes("--actor act-1")));
  assert.ok(
    second.calls.some((c) => c.includes("msg agent register") && c.includes("--session s-chief")),
    "the restore registers the recorded chief session, so the run still keeps its session alive",
  );
  assert.ok(second.calls.some((c) => c.includes("msg agent ack m-restore") && c.includes("--state read")));
});

test("receiverEnv pins the configured store onto the child ft environment", () => {
  // Without the pin a restored receiver would register against the store its
  // new host resolves (the machine's default) instead of the run's throwaway one.
  const env = receiverEnv({ store: "jsonfile", storeOpts: "path=/tmp/db.json" }, { HOME: "/home/u", FACTOTUM_STORE: "sqlite" });
  assert.deepEqual(env, { HOME: "/home/u", FACTOTUM_STORE: "jsonfile", FACTOTUM_STORE_OPTS: "path=/tmp/db.json" });
  assert.deepEqual(receiverEnv({}, { HOME: "/home/u" }), { HOME: "/home/u" });
});

test("receiverEnv names the receiver's actor as each child ft's default actor", () => {
  // Every `ft` the receiver spawns is a child of the agent host, not of a shell
  // that named an identity: with none in its environment the CLI falls back to
  // its agent heuristic and warns "probable agent caller ... this invocation
  // stays unattributed" on every register/claim/ack, and that warning lands
  // inside the receiver's own log lines. The receiver knows its actor, so it
  // names it as the child's default actor. It is deliberately the default-actor
  // path and not FACTOTUM_CALLER: a caller resolves only against registered
  // Actor records, so the loop's role actors (builder-<task>, chief) would be
  // dropped with a warning of their own, while the default path trusts them.
  const env = receiverEnv({ actor: "builder-t-1" }, { HOME: "/home/u", FACTOTUM_DEFAULT_ACTOR: "stale" });
  assert.deepEqual(env, { HOME: "/home/u", FACTOTUM_DEFAULT_ACTOR: "builder-t-1" });
});

test("a plugin-spawned ft child reads the receiver's actor from its environment", async () => {
  // The pin above, over a real child process: this is the transport the receiver
  // actually uses (selectCall -> spawnRunner -> receiverEnv), so the assertion is
  // over the environment an ft binary would read, not a helper's return value.
  const dir = mkdtempSync(join(tmpdir(), "ft-receiver-env-"));
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

test("a cold restore waits while another receiver holds the actor live", async (t) => {
  // Two claimers of one mailbox split a hand-off between them, so a load with no
  // environment must not take over an actor a live receiver already serves. It
  // adopts as soon as that receiver's lease lapses (a crashed host), which is the
  // restore case this exists for.
  const store = new Map();
  const seed = restoreRun();
  const cleanupSeed = setupV2(restoreCtx(store, { sessionID: "ses-live" }), {
    env: { FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1" },
    run: seed.run,
  });
  t.after(cleanupSeed);
  await waitFor(() => recorded(store)?.sessionID === "ses-live", 2000);

  let live = true;
  const resumed = restoreRun({ runID: "run-2" });
  const run = async (argv) => {
    if (argv[1] !== "runs") return resumed.run(argv);
    resumed.calls.push(argv.join(" "));
    return { code: 0, stdout: JSON.stringify([{ actor: "act-1", live }]), stderr: "" };
  };
  const cleanup = setupV2(restoreCtx(store), { env: {}, run });
  t.after(cleanup);

  await new Promise((resolve) => setTimeout(resolve, 60));
  assert.equal(resumed.calls.some((c) => c.includes("msg agent register")), false, "a live receiver's actor is not adopted");

  live = false;
  await waitFor(() => resumed.calls.some((c) => c.includes("msg agent register")), 2000);
});

test("a cold restore whose recorded session is gone stays inert", async (t) => {
  const store = new Map();
  const seed = restoreRun();
  const cleanupSeed = setupV2(restoreCtx(store, { sessionID: "ses-deleted" }), {
    env: { FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1" },
    run: seed.run,
  });
  t.after(cleanupSeed);
  await waitFor(() => recorded(store)?.sessionID === "ses-deleted", 2000);

  const resumed = restoreRun({ runID: "run-2" });
  const cleanup = setupV2(restoreCtx(store, { get: async () => { throw new Error("Session.NotFoundError"); } }), { env: {}, run: resumed.run });
  t.after(cleanup);
  await new Promise((resolve) => setTimeout(resolve, 60));
  assert.equal(resumed.calls.some((c) => c.includes("msg agent register")), false);
});

test("a cold restore of a remote receiver without its token stays inert instead of registering locally", async (t) => {
  // The token is not persisted with the record, so a restored receiver cannot
  // reach its hub. Registering against the local store instead would be a silent
  // misroute: it must say so and leave the hand-offs queued.
  const store = new Map();
  const seed = restoreRun();
  const cleanupSeed = setupV2(restoreCtx(store, { sessionID: "ses-hub" }), {
    env: { FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1", FACTOTUM_MSG_URL: "http://hub:8484", FACTOTUM_SERVE_TOKEN: "secret" },
    call: cliCall(configFromEnv({ FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1" }), seed.run),
  });
  t.after(cleanupSeed);
  await waitFor(() => recorded(store)?.sessionID === "ses-hub", 2000);

  const logs = [];
  const resumed = restoreRun({ runID: "run-2" });
  const cleanup = setupV2(restoreCtx(store), { env: {}, run: resumed.run, log: (entry) => logs.push(entry) });
  t.after(cleanup);
  await new Promise((resolve) => setTimeout(resolve, 60));
  assert.equal(resumed.calls.some((c) => c.includes("msg agent register")), false);
  assert.ok(logs.some((entry) => /token/i.test(entry)), `logs = ${JSON.stringify(logs)}; want the missing token named`);
});

test("a second cold restore delivers into the session the first restore switched to", async () => {
  // The record's sessionID is what a restore resumes. A restore that adopts a
  // different session (the restarted host runs another one) but never writes it
  // back leaves the record naming a session nothing uses any more - and the
  // next restart would inject every hand-off there: the exact misroute the
  // restore path exists to prevent.
  const store = new Map();
  const env = { FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1" };

  // The launch: records ses-1.
  const launch = restoreRun();
  const cleanupLaunch = setupV2(restoreCtx(store, { sessionID: "ses-1" }), { env, run: launch.run });
  try {
    await waitFor(() => recorded(store)?.sessionID === "ses-1", 2000);
  } finally {
    cleanupLaunch();
  }

  // The first restart: resumes ses-1, then the host announces the session it is
  // actually running (ses-2). The record must follow the receiver.
  const resumed = restoreRun({ runID: "run-2" });
  const cleanupResumed = setupV2(restoreCtx(store, { sessionID: "ses-2" }), { env: {}, run: resumed.run });
  try {
    await waitFor(() => recorded(store)?.sessionID === "ses-2", 2000);
  } finally {
    cleanupResumed();
  }

  // The second restart: the queued hand-off must land in ses-2, never the stale
  // ses-1 the first launch recorded.
  const injected = [];
  const again = restoreRun({ runID: "run-3", message: { id: "m-second", from: "act-x", body: "after the second restart" } });
  const cleanupAgain = setupV2(restoreCtx(store, { injected }), { env: {}, run: again.run });
  try {
    await waitFor(() => injected.length === 1, 2000);
    assert.equal(injected[0].sessionID, "ses-2", "the second restore injected into the session the record still named");
  } finally {
    cleanupAgain();
  }
});

test("an event's session cannot be overwritten by the seed it arrived alongside", async (t) => {
  // A restore seeds the record with the session it resumed, and the event stream
  // announces the session the restarted host is actually running. The two writes
  // target one key, so if they race the seed can land last and leave the record
  // naming the stale session - which the next restart resumes: the misroute the
  // two-restore guard pins. The writes are serialised, so they land in the order
  // the receiver learned the sessions, whatever the storage round trips do.
  const store = new Map();
  store.set(agentRecordKey({ location: LOCATION }), {
    project: "prj-1",
    actor: "act-1",
    harness: "opencode",
    bin: "ft",
    sessionID: "ses-seed",
  });

  const slowSeed = deferred();
  let seedIssued = false;
  let settled = 0;
  const ctx = {
    location: LOCATION,
    session: {
      prompt: async () => {},
      get: async ({ sessionID: id }) => ({ id }),
    },
    storage: {
      get: async (key) => store.get(key),
      set: async (key, value) => {
        if (key !== agentRecordKey({ location: LOCATION })) {
          store.set(key, value);
          return;
        }
        // Hold the seed's write, standing in for a storage round trip that
        // outlasts the event's.
        if (value?.sessionID === "ses-seed") {
          seedIssued = true;
          await slowSeed.promise;
        }
        store.set(key, value);
        settled += 1;
      },
    },
    event: {
      async *subscribe() {
        yield { data: { sessionID: "ses-event" } };
        await new Promise(() => {});
      },
    },
  };

  const { run } = restoreRun({ runID: "run-1" });
  const cleanup = setupV2(ctx, { env: {}, run });
  t.after(cleanup);

  await waitFor(() => seedIssued, 2000);
  // Let an unordered event write land while the seed is still in flight.
  await new Promise((resolve) => setTimeout(resolve, 10));
  slowSeed.resolve();
  await waitFor(() => settled >= 2, 2000);

  assert.equal(recorded(store)?.sessionID, "ses-event", "the record follows the session the host announced, not the seed the restore resumed");
});

test("a parked cold restore backs off its liveness poll", async (t) => {
  // A live peer can hold the actor for hours, so a parked restore polling
  // `ft msg runs` every retry interval is a stream of subprocesses for as long
  // as the agent runs. The gap grows instead, and the first poll after the lease
  // lapses still adopts the mailbox.
  const store = new Map();
  const seed = restoreRun();
  const cleanupSeed = setupV2(restoreCtx(store, { sessionID: "ses-live" }), {
    env: { FACTOTUM_PROJECT: "prj-1", FACTOTUM_ACTOR: "act-1", FACTOTUM_MSG_RETRY_MS: "10" },
    run: seed.run,
  });
  t.after(cleanupSeed);
  await waitFor(() => recorded(store)?.sessionID === "ses-live", 2000);

  let live = true;
  let registered = false;
  let polls = 0;
  const cleanup = setupV2(restoreCtx(store), {
    env: {},
    run: async (argv) => {
      if (argv[1] === "runs") {
        polls += 1;
        return { code: 0, stdout: JSON.stringify([{ actor: "act-1", live }]), stderr: "" };
      }
      if (argv[2] === "register") {
        registered = true;
        return { code: 0, stdout: JSON.stringify({ run_id: "run-2" }), stderr: "" };
      }
      // Park a claim so the adopted receiver does not spin after the assertion.
      if (argv[2] === "claim") return new Promise(() => {});
      return { code: 0, stdout: JSON.stringify({ ok: true }), stderr: "" };
    },
  });
  t.after(cleanup);

  await new Promise((resolve) => setTimeout(resolve, 300));
  // Without backoff this window is 30 polls at a 10ms interval.
  assert.ok(polls <= 10, `polls = ${polls} in 300ms; want the parked poll backed off`);

  live = false;
  await waitFor(() => registered, 2000);
});
