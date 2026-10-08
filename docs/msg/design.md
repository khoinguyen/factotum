# ft msg — design

Status: **design for review** (t-a4gjlxm5jj). No implementation in this change.

`ft msg` makes ft the hub for agent-to-agent messages: a first-class `Message` entity in the
store, a small register/claim/ack receiver protocol, per-agent receiver adapters (opencode plugin,
pi extension), and a token-gated transport over `ft serve` so it works across hosts. It replaces
`cmux agent message` and the `cmux-msg.sh` paste fallback for the chief loop (milestone
t-wqqsv5hiev).

This document pins the four things the task asks for, each with a section below: the Message
entity and states, addressing, the store port and conformance cases, and the adapter
register/claim/ack protocol — plus how it rides over `ft serve` and the receiver shape.

## 1. Goals and non-goals

Goals.

- Durable, ordered, at-least-once messaging between agents, and between agents and the operator.
- One addressing model that covers a person, a specific running session, and "whoever is on this
  task".
- A receiver contract any harness can implement, so ft does not depend on cmux or on owning the
  child process.
- Cross-host delivery through `ft serve` using the existing token, as the cloud prerequisite.
- Messages appear in the live dashboard alongside graph events.

Non-goals (explicitly out of scope for this milestone).

- Multi-tenant authentication: the hub trusts the serve token holder to act as any actor (single
  operator). Revisit only if ft serves more than one trust domain.
- Broadcast / pub-sub topics, presence, typing indicators, read receipts between humans.
- Message attachments or large payloads: a message carries text plus links to store artifacts.
- Exactly-once delivery. The contract is at-least-once with an explicit `read` ack; consumers must
  be idempotent by `message id` (receivers dedupe on id).

## 2. Context: what already exists

- `pkg/core` owns the domain: `Actor` (`pkg/core/actor.go`), `Ticket` (`pkg/core/task.go`),
  `Event` (`pkg/core/event.go`), and injected-id generation in `pkg/app/deps.go`. `core` is pure.
- Every mutation appends an `Event`; the dashboard polls the newest event id and pushes an SSE
  `update` (`internal/serve/serve.go`, `internal/serve/broker.go`). This is the live signal that
  messages will reuse for free.
- Storage is a port with four adapters (memory, jsonfile, jsondir, sqlite), each passing
  `pkg/store/conformance`, which is the definition of a backend (`pkg/store/store.go`).
- A run today is not an entity: `ft run` records `task.run_started` / `task.run_finished` events
  (`pkg/app/run.go`) but has no durable run identity to address.
- `ft serve` is an HTTP adapter bound to loopback by default with a read side (open) and a
  token-gated capture write side (`internal/cli/serve.go`, `internal/serve/README.md`).
- The chief loop messages peers through cmux (`cmux agent message`, with the `cmux-msg.sh` paste
  fallback), which is not durable, is host-local, and breaks when the recipient's plugin fails to
  load.

The gap `ft msg` closes: a durable mailbox ft owns, with an address that survives a restart and a
transport that reaches another host.

## 3. The Message entity

New pure types in `pkg/core` (`message.go`), following the existing entity style (`Validate`,
typed string enums, injected timestamps).

```go
type MessageID string

type MessageState string

const (
    MessageQueued    MessageState = "queued"    // accepted, not yet claimed
    MessageDelivered MessageState = "delivered" // claimed by a run, leased, awaiting ack
    MessageRead      MessageState = "read"      // acked success (terminal)
    MessageFailed    MessageState = "failed"    // terminal failure after retries, or a nack
)

type Message struct {
    ID        MessageID
    ProjectID ProjectID
    From      *ActorID   // sender; nil means a system/automated message
    To        Address    // canonical target (section 4)
    Body      string
    Links     []Link     // reuse core.Link: large content lives in an artifact
    ReplyTo   *MessageID // threading, optional

    State     MessageState
    // Delivery bookkeeping. Set by Claim/Ack, never by the sender.
    RunID      *RunID      // the run that most recently claimed it
    LeaseUntil *time.Time  // zero when not delivered
    Attempts   int         // number of claims
    Error      string      // last failure reason, for failed/nacked messages

    CreatedAt   time.Time
    UpdatedAt   time.Time
    DeliveredAt *time.Time
    ReadAt      *time.Time
}
```

`Validate` rejects: empty id; empty/invalid `To`; empty `ProjectID`; empty body (after trim);
unknown state; body over the configured limit (section 11); and any `To` whose kind is unknown.
The body limit is passed in at send time, not stored on the entity, so `core` stays pure.

### State machine

```
                 claim                ack(read)
  queued ─────────────────▶ delivered ─────────────▶ read      (terminal)
    ▲                         │  │
    │   lease expiry /        │  └── ack(failed) ───▶ failed    (terminal)
    └─── nack(requeue) ───────┘
                               └── lease expiry after MaxAttempts ─▶ failed (terminal)
```

- `queued`: at rest; the only state a claim may pick up.
- `delivered`: leased to exactly one run; `LeaseUntil` bounds ownership. Not terminal.
- `read`: the success terminal ack; redelivery stops.
- `failed`: terminal. Reached by an explicit `ack --failed`, or by lease expiry once `Attempts`
  reaches `MaxAttempts`. An operator can requeue with `ft msg retry`.

`read` is idempotent: a repeat `ack --read` on an already-`read` message is a no-op success, so a
receiver that crashes after injecting but before acking can safely retry.

## 4. Addressing

An address is a canonical string `kind:id`. The three kinds:

```go
type AddressKind string
const (
    AddressActor AddressKind = "actor" // any run registered for this actor (actor mailbox)
    AddressRun   AddressKind = "run"   // exactly one running session
    AddressTask  AddressKind = "task"  // sugar: resolved to run/actor at enqueue
)

type Address string // "actor:act-x", "run:run-y", "task:t-z"
```

### Semantics

- `actor:<actor-id>` — deliver to the actor's mailbox. Any run of that actor may claim it; the
  first claim wins. This is the stable address: it survives a restart and is what a human owner or
  a peer knows.
- `run:<run-id>` — deliver to exactly one session. Used for replies (`--reply-to` implies `run:`
  the original sender's run) and for the chief wiring a specific builder/reviewer session. A claim
  for `run:` targets only the run's own messages. If the run is gone, the message stays `queued`
  (never silently dropped) and falls back to the actor mailbox at the operator's discretion via
  `ft msg retry --to actor:`.
- `task:<ticket-id>` — sugar, resolved **at enqueue time** by `ft msg send`:
  1. if a live run is registered with `TaskID == t`, address `run:<that run>`;
  2. else if the task's `AssigneeID` is an agent actor, address `actor:<assignee>`;
  3. else keep `task:<t>`; whoever later starts the task (registers a run with that `TaskID`)
     becomes the recipient. A `task:` message is claimable by exactly the run whose `TaskID`
     matches.

### Why run + actor, with task as sugar

- **actor-only** loses session identity: replies cannot find the exact peer, two concurrent runs
  of the same actor race for the same mailbox, and the hub cannot wake the *idle* session that
  should handle it. cmux has this weakness, which is why the chief script has to disambiguate
  surfaces by hand.
- **run-only** is precise but unusable as the common address: senders know a person or a task, not
  an ephemeral run id.
- **task-only** is work-scoped, not conversation-scoped: it cannot address a human, the chief, or
  a peer outside a task.
- Recommendation: **run and actor are the two real scopes; task is a resolver over them.** This is
  the recommendation the task asked us to weigh, and it composes all three use cases (a person, a
  session, a task) with one stored `To`.

## 5. Runs and the registry

The delivery protocol needs a durable identity for "a live session". Add `core.Run` and a
`RunRepo` alongside messages. A run is created by **register** and is live while its lease is
renewed.

```go
type RunID string

type Run struct {
    ID        RunID
    ProjectID ProjectID
    ActorID   ActorID
    TaskID    *TicketID // the task this session is working, when any
    Harness   string    // "opencode", "pi", ... (free-form, for display)
    Host      string    // machine identity, for cross-host diagnostics
    PID       int
    // Wake describes how the hub may reach this session out-of-band; empty
    // means "poll only" (section 8).
    CanInject bool
    CreatedAt  time.Time
    SeenAt     time.Time // last heartbeat
    LeaseUntil time.Time // SeenAt + TTL; stale once past
}
```

- Registration is upsert-idempotent on `(ProjectID, ActorID, Host, PID)` so a plugin reconnect does
  not create a duplicate run; the returned `RunID` is stable for the session.
- A run is **live** while `LeaseUntil > now` and **stale** otherwise. `ft msg inbox --runs` and the
  dashboard show liveness; a claim addressed to a stale `run:` does not match (the message stays
  queued for actor fallback).
- Deregister on clean shutdown; staleness is the crash path.

## 6. Store port and conformance

`pkg/store` grows two repos on `Backend` (`Messages()`, `Runs()`); every adapter implements them
and the shared suite is extended, so "a backend supports messages" has the same meaning as for
tasks. The exact interface:

```go
type MessageFilter struct {
    ProjectID core.ProjectID
    To        *core.Address   // exact stored To match
    Actor     *core.ActorID   // messages addressable by this actor (To == actor:<id>)
    Run       *core.RunID     // messages addressable by this run (To == run:<id>)
    Task      *core.TicketID  // messages addressable by this task (To == task:<id>)
    States    []core.MessageState
    Since     *time.Time
    Limit     int
}

type MessageRepo interface {
    Create(ctx context.Context, m *core.Message) error
    Get(ctx context.Context, id core.MessageID) (*core.Message, error)
    List(ctx context.Context, f MessageFilter) ([]*core.Message, error)
    Update(ctx context.Context, m *core.Message) error

    // Claim atomically selects the oldest queued message claimable by
    // (runID, actor, taskID) and transitions it to delivered, setting RunID,
    // LeaseUntil, and Attempts. It returns ErrNotFound when nothing is
    // claimable. Two concurrent claims never return the same message.
    Claim(ctx context.Context, req ClaimRequest) (*core.Message, error)

    // Ack finalizes a delivered message owned by runID: read is terminal
    // success and idempotent; failed records Error and is terminal.
    Ack(ctx context.Context, req AckRequest) error

    // Nack requeues a delivered message (attempts already counted) or, when
    // attempts >= MaxAttempts, parks it as failed. It is the explicit
    // "I could not handle this" path.
    Nack(ctx context.Context, id core.MessageID, runID core.RunID, reason string) error

    // RequeueExpired reclaims every delivered message whose lease lapsed:
    // back to queued, or to failed once attempts >= maxAttempts. It returns
    // how many moved. It is the crash-recovery sweep.
    RequeueExpired(ctx context.Context, now time.Time, maxAttempts int) (int, error)

    // Prune removes terminal (read/failed) messages older than cutoff and
    // returns how many were removed.
    Prune(ctx context.Context, before time.Time, states []core.MessageState) (int, error)
}

type RunRepo interface {
    Register(ctx context.Context, run *core.Run) (*core.Run, error) // upsert, returns stable ID
    Heartbeat(ctx context.Context, id core.RunID, now time.Time, lease time.Duration) error
    List(ctx context.Context, f RunFilter) ([]*core.Run, error)
    Delete(ctx context.Context, id core.RunID) error
}
```

Design notes:

- **`Claim` is the only concurrency-sensitive primitive.** It is a compare-and-swap on state:
  memory uses its mutex, sqlite a transaction, jsonfile its write lock, jsondir its lock file.
  Claimability is the union of the run address, the run's actor address, and the run's task
  address, matching the resolution rules in section 4.
- **Ordering is FIFO** by `CreatedAt` then `ID`, so delivery is deterministic and testable.
- **`RequeueExpired`** is called by receivers/serve on a timer and on startup, so a crashed
  receiver's in-flight message is not stuck in `delivered` forever. It is also how at-least-once
  redelivery happens.
- **`UpdatedAt`** is the compare-and-swap token the other repos already use; a backend may reuse
  the pattern from `TicketRepo.UpdateExpected`.

### Conformance cases (`pkg/store/conformance`)

Add `t.Run("Message", ...)` and `t.Run("Run", ...)`, each backed by the same in-memory factory
contract. The suite must cover, named for what it asserts:

1. `Create`/`Get`/`List` round-trip; duplicate `Create` → `ErrAlreadyExists`; `Get` missing →
   `ErrNotFound`.
2. `Validate`: empty id, empty `To`, unknown state, oversize body, empty project are rejected.
3. `List` filters: by project, exact `To`, state set, `Since`, and `Limit`; ordering is oldest
   first; no cross-project leakage.
4. `Claim`: returns the oldest claimable queued message; skips non-claimable addresses; returns
   `ErrNotFound` on empty; sets `State=delivered`, `RunID`, `LeaseUntil`, and increments
   `Attempts`.
5. `Claim` concurrency: N goroutines claiming one queued message — exactly one wins, the rest get
   `ErrNotFound`, and no message is delivered twice.
6. `Claim` addressing: `actor:` messages are claimable by any run of that actor; `run:` only by
   that run; `task:` only by a run registered with that `TaskID`.
7. `Ack(read)`: `delivered → read`; repeat is idempotent; ack on a non-delivered message errors.
8. `Ack(failed)` / `Nack`: records `Error`, ends terminal `failed`; `Nack` requeues when under
   `MaxAttempts`.
9. `RequeueExpired`: a `delivered` message past its lease returns to `queued` with `Attempts`
   retained; past `MaxAttempts` it becomes `failed`; a `read` message is untouched.
10. `Prune`: removes only the requested terminal states before the cutoff; leaves `queued`/
    `delivered` and younger terminal messages.
11. `Run.Register` upsert: same `(project, actor, host, pid)` returns the same `RunID`; heartbeat
    moves `SeenAt`/`LeaseUntil`; `List` separates live from stale; `Delete` removes.

These are the contract the four adapters (memory, jsonfile, jsondir, sqlite) must pass before
t-q7iwpouhr5 is done.

## 7. Delivery protocol: register / claim / ack

One small vocabulary, used identically by the CLI, the `ft serve` transport, and both plugins.
The store is the hub; a receiver is any client of these verbs.

- **register(run)** — announce a session. Payload: `project`, `actor`, `task?`, `harness`, `host`,
  `pid`, `can_inject`, `ttl`. Returns `run_id` and `lease_until`. Idempotent per session (section
  5). The adapter then heartbeats at `ttl/3`.
- **claim(run_id, wait?)** — atomically take the oldest message claimable by the run (section 4)
  and mark it `delivered` with `lease_until = now + lease`. With `wait`, it long-polls up to
  `wait` before returning empty. This is the poll loop.
- **ack(message_id, run_id, state=read|failed, error?)** — finalize a delivered message. `read`
  stops redelivery; `failed` is terminal and records the reason.
- **nack(message_id, run_id, reason)** — the receiver could not handle it now; requeue (or park
  as failed past `MaxAttempts`).
- **heartbeat(run_id)** — renew the run lease.
- **deregister(run_id)** — clean shutdown.

Lease and retry defaults (configurable, machine-scoped like `serve.token`):

- `lease = 30s`: injection is fast, so the receiver acks well within it; a crash past the lease
  redelivers.
- `max_attempts = 5`: after that a message is parked `failed` for a human, never retried forever.
- `claim wait = 25s`: long-poll window under the lease, so the receiver re-registers/refreshes
  cleanly between windows.

Idempotency: the receiver dedupes on `message id` before injecting, because at-least-once
redelivery is possible. The plugin keeps a tiny in-memory set of recently acked ids.

## 8. Wake: poll is the contract, push is an optimization

- **Poll (default, always correct).** The receiver runs a background loop: long-poll `claim`, and
  on a message inject it as a new user turn in the agent session, then `ack(read)`. This works for
  any adapter regardless of who launched the session, wakes an idle agent (the loop is independent
  of the agent's turn), and crosses hosts. It is the only mechanism the contract requires.
- **Push / direct inject (when `ft run` owns the session).** When `ft run` launched an interactive
  session, it holds the session's terminal/control channel (`isolation.Command.TTY`). The hub may
  write a claimed message straight into that channel instead of waiting for a poll. A headless
  one-shot `opencode run` exits after its turn, so push only applies to a persistent/interactive
  session; a one-shot run's *next* invocation is a fresh session that claims on start.
- A run advertises `can_inject` at register. The hub tries push only when the run is live with a
  control channel; otherwise the poll loop delivers. **Correctness never depends on push** — push
  is just lower latency.

This settles open question (3) from the task: timer-poll is the baseline, direct injection is an
optional accelerator for `ft run`-owned sessions.

## 9. Cross-host transport over `ft serve`

Local `ft msg` writes to the project backend directly (same DB on the same host). To reach another
host (or the cloud), `ft serve` exposes the same protocol over HTTP. The receiver is an adapter;
the transport is a detail.

Endpoints (JSON, mirroring the CLI verbs):

```
POST /api/msg/send       {from?, to, body, reply_to?, links?}      -> {id, state}
GET  /api/msg/inbox      ?address=&state=&limit=                   -> {messages:[...]}
GET  /api/msg/read/<id>                                            -> {message}
POST /api/msg/register   {actor, task?, harness, host, pid, ...}   -> {run_id, lease_until}
POST /api/msg/claim      {run_id, wait?}                           -> {message?}
POST /api/msg/ack        {id, run_id, state, error?}               -> {ok}
POST /api/msg/heartbeat  {run_id}                                  -> {ok}
POST /api/msg/deregister {run_id}                                  -> {ok}
```

Auth and trust:

- All `/api/msg/*` endpoints require the serve token in the `Authorization: Bearer` header,
  constant-time compared, exactly like capture. This **diverges from the open graph read side on
  purpose**: private agent comms should not be world-readable to anyone who can reach the
  dashboard. (Open question for Khoi, section 12.)
- The token authenticates the *host* to the hub, not the actor; a token holder may send as any
  actor (`from` in the payload). Acceptable for a single-operator hub; documented as the trust
  boundary.
- The hub is the same backend store, so a message sent locally is visible to a remote claimer and
  vice versa — cross-host is purely the transport, not a second store. This is the
  "cloud prerequisite" t-773lhmbml2 builds.
- Multi-project: the transport scopes to the serve target project (or requires `?project=` when
  serving all); crash/lease sweeps run on a `ft serve` timer so a remote receiver that dies is
  recovered by the hub.

## 10. Receiver adapters

Both adapters are thin clients over section 7. Each implements the same four steps: register,
long-poll claim, inject as a user turn, ack.

### opencode plugin (t-fn2gndufxa)

- A local plugin that **default-exports an object with `server()`** — the shape opencode 1.18.35
  requires and the one cmux's plugin got wrong. `server()` starts the claim loop.
- Reads `FACTOTUM_PROJECT`, `FACTOTUM_ACTOR`, `FACTOTUM_RUN_ID`, `FACTOTUM_TASK_ID` (set by
  `ft run`), and `FACTOTUM_MSG_URL`/`FACTOTUM_SERVE_TOKEN` for a remote hub; with no URL it talks
  to the local `ft` store.
- On load: register. Loop: `claim({wait})`; on a message, inject it as a new user turn through the
  opencode server API (a mid-turn agent queues it for the next turn; an idle agent starts a new
  turn), then `ack(read)`. A failure `ack(failed)` with the error. On unload: deregister.
- The loop is off the agent's turn, so injection never blocks tool execution.
- `ft run`'s opencode harness injects at launch when the run itself is the reason the session
  exists (it passes the initial message, and the plugin claims the rest).

### pi extension (t-eedgwt6g6g)

- The same protocol through pi's extension registration and message-injection hook. A structural
  mirror of the opencode plugin so the two do not drift; the shared code is the protocol client.

### cmux migration

- `ft msg send` becomes the primitive. `cmux-msg.sh` becomes a shim that calls `ft msg send` and
  only falls back to `cmux paste` when ft is unavailable. The chief loop addresses peers as
  `run:<id>` (precise) or `actor:<name>` (stable), replacing surface-title disambiguation.

## 11. CLI surface

```
ft msg send <address|ticket-id> [-b body | -] [--from <actor>] [--reply-to <id>] [--link kind=url]
ft msg inbox [--state queued|delivered|read|failed] [--address X] [--for <actor>] [--run <id>] [--limit N]
ft msg get <id>            # show without changing state
ft msg read <id>           # show and ack read (marks terminal)
ft msg ack <id> [--failed -e <reason>]     # explicit state ack for adapters/humans
ft msg retry <id> [--to <address>]         # requeue a failed message, optionally re-address
ft msg runs [--live]       # list registered runs (adapter liveness)
ft msg agent <register|claim|ack|nack|heartbeat|deregister>   # hidden, stable adapter verbs
```

- Output follows the repo shapes: single-result commands print `id/action/.../project/repo` yaml-
  like lines (with `sent: true`/`read: true`); list commands print a table; `get` prints a human
  block. `-o json|yaml` are the machine interfaces.
- `--from` defaults to the configured actor (`FACTOTUM_ACTOR`, else the machine default).
- The hidden `ft msg agent *` verbs are the stable interface the plugins shell out to when they
  cannot speak HTTP; they are the same call the transport implements.

## 12. Dashboard, events, retention, security

### Events and the dashboard

- New event kinds: `message.sent`, `message.claimed`, `message.read`, `message.failed`. Each
  transition appends one, so the existing broker/SSE path lights up with no new mechanism.
- `Event.TicketID` is set when the message is task-addressed, so it surfaces on the task detail.
- The dashboard snapshot gains a `messages` list (project-scoped, like `updates`), so the
  right-pane shows a live message stream; because the app refetches on every `/events` update, a
  send appears immediately. This answers open question (5): messages compose with events by being
  an event source, not a parallel channel.

### Retention and limits

- **Body limit:** default 16 KiB, machine-configurable. Enforced at send (`ErrInvalid`), never
  silently truncated — a sender must know.
- **Retention:** terminal `read` messages pruned after 30 days; terminal `failed` after 90 days;
  `queued`/`delivered` never auto-pruned. `ft msg prune` runs the sweep manually; `ft serve` runs
  it on a slow timer. Reuses the `Prune` port method, so it is backend-uniform.
- **No blobs:** large content is linked (a doc/artifact), not inlined.

### Security / trust

- Messages are stored as plaintext in the project DB and are readable by anything that can read
  that DB. For a single-operator hub that is the current posture for tasks and events too. If
  message bodies carry secrets, that is a product decision (open question 1).

## 13. Migration, rollout, and task sequencing

- **Additive schema only.** New tables/keys for messages and runs; no change to existing entities.
  sqlite gets a forward migration; jsondir a new file/dir; jsonfile new keys; memory new maps.
- **The `Backend` interface grows**, so every adapter must implement both repos and pass the new
  conformance cases. That growth is the intended forcing function.
- Rollout follows the current DAG: this spec → **t-q7iwpouhr5** (entity + store port + CLI +
  conformance) → **t-773lhmbml2** (serve transport) and **t-eedgwt6g6g / t-fn2gndufxa** (pi /
  opencode receivers). The chief loop can switch to `ft msg` once q7iwpouhr5 + one receiver land;
  cmux fallback stays until both.

## 14. Open questions for Khoi (product calls)

1. **Sensitive bodies.** Messages persist as plaintext in the shared DB. Acceptable for a
   single-operator hub, or do we need a shorter default retention / opt-in encryption?
2. **Token-gated message reads.** I recommend gating `/api/msg/*` reads as well as writes
   (private comms), diverging from the open graph read side. Confirm this is the intended posture.
3. **Human delivery.** A message to a human `actor:` has no adapter. Does it simply sit in the
   inbox and surface on the dashboard, or should it also notify (e.g. the existing ntfy path)?
4. **Defaults.** Body 16 KiB, read retention 30d, failed 90d, lease 30s, max attempts 5 — accept,
   or set different numbers?

## 15. Alternatives considered and rejected

- **cmux/`cmux agent message` as the transport.** Rejected: not durable, host-local, and the
  plugin-load bug shows it owns too much of the delivery path. `ft msg` supersedes it.
- **A single `actor:` mailbox only.** Rejected (section 4): no precise session replies, races
  between concurrent runs, no reliable idle wake.
- **HTTP-only (no local store path).** Rejected: a local `ft msg` should not require a running
  `ft serve`; the store is the hub, HTTP is one transport to it.
- **Reuse `Event` as the message log.** Rejected: events are append-only summaries for the
  dashboard; messages need mutable delivery state (claim/lease/ack/retry) and a typed recipient.
- **Push-only wake.** Rejected: `ft run` does not always own the session, and a one-shot run is
  gone before a push arrives; poll must be the contract.
