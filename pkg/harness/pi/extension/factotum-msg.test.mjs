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
  selectCall,
} from "./factotum-msg.js";

const BASE_CONFIG = {
  project: "prj-1",
  actor: "act-1",
  task: "t-1",
  bin: "ft",
  harness: "pi",
  host: "",
  pid: 7,
  waitMs: 25000,
  ttlMs: 90000,
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
});

test("configFromEnv defaults to the local ft transport", () => {
  const config = configFromEnv({ FACTOTUM_PROJECT: "prj", FACTOTUM_ACTOR: "act" });
  assert.equal(config.msgUrl, "");
  assert.equal(config.token, "");
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

  await call({ op: "register", actor: "act-1", harness: "pi", host: "host-a", pid: 7, ttl: "90000ms", task: "t-1" });
  await call({ op: "claim", run: "run-1", actor: "act-1", wait: "25000ms", task: "t-1" });
  await call({ op: "ack", id: "m-1", run: "run-1", state: "read", error: "" });
  await call({ op: "heartbeat", run: "run-1", ttl: "90000ms" });
  await call({ op: "deregister", run: "run-1" });

  assert.deepEqual(requests.map((r) => r.url), [
    "http://hub:8484/api/msg/register",
    "http://hub:8484/api/msg/claim",
    "http://hub:8484/api/msg/ack",
    "http://hub:8484/api/msg/heartbeat",
    "http://hub:8484/api/msg/deregister",
  ]);
  for (const request of requests) {
    assert.equal(request.options.method, "POST");
    assert.equal(request.options.headers.Authorization, "Bearer s3cret");
    assert.equal(request.options.headers["Content-Type"], "application/json");
  }
  assert.deepEqual(JSON.parse(requests[0].options.body), {
    actor: "act-1", task: "t-1", harness: "pi", host: "host-a", pid: 7, ttl: "90000ms",
  });
  assert.deepEqual(JSON.parse(requests[1].options.body), {
    run_id: "run-1", actor: "act-1", task: "t-1", wait: "25000ms",
  });
  assert.deepEqual(JSON.parse(requests[2].options.body), {
    id: "m-1", run_id: "run-1", state: "read", error: "",
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
  let claims = 0;
  const fetchFn = async (url) => {
    urls.push(url);
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

test("receiver registers, claims, injects, and acks", async () => {
  const injected = [];
  const { receiver, calls, cleared } = receiverFor(
    [{ found: true, message: { id: "m-1", from: "act-x", task_id: "t-1", body: "hello" } }, { found: false }],
    injected,
  );

  await receiver.start();
  assert.equal(receiver.runID(), "run-1");
  assert.ok(calls.some((c) => c === "msg agent register --actor act-1 --project prj-1 --harness pi --pid 7 --ttl 90000ms --task t-1 -o json"));

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
  const keys = ["FACTOTUM_PROJECT", "FACTOTUM_ACTOR", "FACTOTUM_BIN", "FACTOTUM_MSG_WAIT_MS", "FACTOTUM_MSG_TTL_MS", "CALLS"];
  const previous = new Map(keys.map((key) => [key, process.env[key]]));
  process.env.FACTOTUM_PROJECT = "prj-1";
  process.env.FACTOTUM_ACTOR = "act-1";
  process.env.FACTOTUM_BIN = stub;
  process.env.FACTOTUM_MSG_WAIT_MS = "10";
  process.env.FACTOTUM_MSG_TTL_MS = "1000";
  process.env.CALLS = callsPath;
  try {
    factotumMsg(pi);
    // session_start resolves once registration completes, so the shutdown below
    // cannot race the run id.
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

