import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import factotumMsg, {
  configFromEnv,
  createFactotumReceiver,
  formatMessage,
  makeInjector,
  parseJSON,
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
    run,
    inject: inject || (async (text) => { injected.push({ text }); }),
    setIntervalFn: () => 1,
    clearIntervalFn: () => { intervalCleared = true; },
  });
  return { receiver, calls, cleared: () => intervalCleared };
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
    await handlers.get("session_start")();
    await waitFor(() => existsSync(callsPath) && readFileSync(callsPath, "utf8").includes("msg agent register"), 5000);
  } finally {
    await handlers.get("session_shutdown")();
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
  const log = readFileSync(callsPath, "utf8");
  assert.match(log, /msg agent register --actor act-1 --project prj-1 --harness pi/);
  assert.match(log, /msg agent deregister --run run-1/);
});

