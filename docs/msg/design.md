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
- The chief loop messaged peers through cmux (`cmux agent message`, with the `cmux-msg.sh` paste
  fallback), which was not durable, was host-local, and broke when the recipient's plugin failed to
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
    From      *ActorID     // sender; nil means a system/automated message
    To        Address      // canonical target (section 4)
    // TaskID is the originating task this message concerns, when any. It is
    // set when the target was task:<t> (section 4) even though To may be
    // rewritten to run:/actor:, so the task link is never lost. A task-less
    // message leaves it nil.
    TaskID    *TicketID
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
unknown state; body over the configured limit (section 11); any `To` whose kind is unknown; and a
`To = task:<t>` whose `TaskID` disagrees with `t`. The body limit is passed in at send time, not
stored on the entity, so `core` stays pure.

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
- `failed`: terminal. Reached by an explicit `ft msg agent ack <message> --run <run> --state failed
  -e <reason>`, or by lease expiry once `Attempts` reaches `MaxAttempts`. An operator can requeue it
  with the retry verb (proposed, not built — section 11).

`read` is idempotent: a repeat `ft msg agent ack <message> --run <run> --state read` on an
already-`read` message is a no-op success, so a receiver that crashes after injecting but before
acking can safely retry.

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
- `run:<run-id>` — deliver to exactly one session: the chief wiring a specific builder/reviewer
  session, or an operator re-addressing a message after a node restart. A claim for `run:` targets
  only the run's own messages. A reply is not one of those uses: `--reply-to` records only the
  threading id (`Message.ReplyTo`) and never rewrites the target, and a message stores the sender's
  actor, not the sender's run (the receiver injects `[ft msg from <actor>]`), so a reply addresses
  `actor:<from>` — the stable mailbox, never an inferred run. If the run is gone, the message stays
  `queued` (never silently dropped) and falls back to the actor mailbox at the operator's
  discretion (the proposed retry verb's re-address, section 11).
- `task:<ticket-id>` — sugar, resolved **at enqueue time** by `ft msg send`. In every case the
  message records `Message.TaskID = t`, so the task link survives the rewrite. Resolution never
  guesses between peers, because a live run is a session, not a peer:
  1. exactly one live run is registered with `Run.TaskID == t` → address `run:<that run>`;
  2. several live runs, all of them the **same actor** → address `actor:<that actor>`: that is the
     stable mailbox every one of those sessions can claim, while picking one run by id order would
     be arbitrary;
  3. several live runs with **different actors** → refuse with `ErrInvalid`, naming every candidate
     `actor:<role>` address and an example send. This is the normal chief-loop state — a builder
     and a reviewer are both live on one task — so a ticket-addressed hand-off has to say which
     peer it means (`ft msg send actor:reviewer-t-x`); a run-id tie-break would silently hand the
     message to the wrong peer;
  4. no live run, and the task's `AssigneeID` is an agent actor → address `actor:<assignee>`;
  5. else keep `To = task:<t>`; whoever later starts the task (registers a run with that `TaskID`)
     becomes the recipient. A stored `task:` message is claimable by exactly the run whose
     `Run.TaskID` matches.

  Cases 4 and 5 address a mailbox, not a session, so `ft msg send` warns on stderr when no live run
  can claim it: the message is `queued`, a receiver may not exist yet, so `to:` must not be read as
  a working peer.

  Because `TaskID` is carried on the message, a message resolved to `run:`/`actor:` still filters
  by task and still sets `Event.TicketID` (sections 6 and 12); the `task:` stored form is only the
  unresolved fallback, not the only way the task link is expressed.

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
- A run is **live** while `LeaseUntil > now` and **stale** otherwise. `ft msg runs --live` and the
  dashboard show liveness. Liveness gates **send resolution**, not claimability: `ft msg send`
  resolves a task to its one live run, else the actor when every live run shares it, else the
  assignee actor, else `task:` — refusing when live runs of different actors make the ticket
  ambiguous (section 4; `resolveTask` in `pkg/app/message.go`) — while `Claim` is a store-local
  compare-and-swap on the stored address with no liveness check. So a run always drains its own
  mailbox, even with a lease that lapsed a moment ago, and a claim for a stale `run:` does still
  match. Gating claim on liveness instead would need
  the store to read the registry inside the claim, and would strand a live receiver whose lease
  merely lapsed.
- The cost is the other side: a message addressed to a run that dies before claiming stays `queued`
  under `run:<id>`, and nothing sweeps queued messages. It is recoverable only while the run row
  exists: registration upserts on `(project, actor, host, pid)`, so the same process re-registering
  after a stale lease keeps its id and drains the mailbox. A clean `deregister` deletes the row, and
  a restarted process has a new pid, so either way the next registration mints a new run id and the
  old `run:` message has no claimant; nothing refuses a deregister with queued mail either. An
  operator then re-addresses it with the proposed `retry` verb (section 11).
- **Liveness means claiming, not being up.** The lease is renewed only while a receiver can actually
  deliver: its claim loop is running, it has a session to inject into, and its current claim round trip
  settles within two long-poll windows. A receiver that is parked (no session after a reload, which an
  idle agent will never announce), whose claim loop stopped, or that is wedged on a round trip that
  never returns (a hung local `ft`, a remote hub stalling mid-poll, a session prompt that never
  resolves) must not renew, so its lease lapses and `ft msg runs --live` reports the peer as gone
  instead of healthy. That is the point of the lease: a live-looking TUI with no claim child is the
  failure this rule makes observable (t-vo7dpanzuu). A failed claim is the same absence of progress as
  a hung one: a run of consecutive failures ages the clock just as a wedged round trip does, so a
  receiver whose every claim fails cannot read healthy while claiming nothing. Claimability still does
  not depend on liveness, so a lapsed receiver that wakes drains its mailbox; and registration is
  retried rather than attempted once, so a transient store error at load cannot leave a session
  registered-in-name-only.
- **The inverse:** a heartbeat answered `not_found` (the hub forgot the run - a hub restored from a
  backup does not know runs registered after it) re-registers the run once and logs it. The receiver
  is still claiming, so without this it would deliver every handoff while staying out of
  `ft msg runs --live` forever.
- Deregister on clean shutdown; staleness is the crash path.

## 6. Store port and conformance

`pkg/store` grows two repos on `Backend` (`Messages()`, `Runs()`); every adapter implements them
and the shared suite is extended, so "a backend supports messages" has the same meaning as for
tasks. The exact interface:

```go
type MessageFilter struct {
    ProjectID core.ProjectID
    To        *core.Address   // exact stored To match
    Actor     *core.ActorID   // exact stored To == actor:<id>
    Run       *core.RunID     // exact stored To == run:<id>; never task:<t> (that is the Task filter)
    Task      *core.TicketID  // Message.TaskID == <id> (the originating task, whatever To became)
    States    []core.MessageState
    Since     *time.Time
    Limit     int
}

// ClaimRequest selects one message for a run. Recipient is the union of the
// run's own address, its actor's address, and its task address (section 4).
type ClaimRequest struct {
    RunID       core.RunID
    Recipient   ClaimRecipient // {ActorID, TaskID} resolved from the run
    Lease       time.Duration  // delivered lease; 0 uses the hub default (30s)
    Wait        time.Duration  // long-poll window; 0 returns immediately
}

// AckRequest finalizes one delivered message owned by a run.
type AckRequest struct {
    ID      core.MessageID
    RunID   core.RunID
    State   core.MessageState // read | failed
    Error   string            // reason when State == failed
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

    // Prune removes terminal (read/failed) messages whose UpdatedAt — the
    // time the message reached its terminal state, not its creation — is
    // before cutoff, and returns how many were removed. A non-terminal state
    // in states matches nothing.
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
- **The `Run` filter matches only the run's own address** (`To == run:<id>`), never
  `To == task:<Run.TaskID>`: a `RunID` alone gives the store no `TaskID` to compare against, and
  inferring one from a live run would make the filter's meaning depend on registry timing.
  Task-scoped listing is the `Task` filter, which matches `Message.TaskID` whatever `To` became. A
  run whose `TaskID` is nil therefore has no task address at all: it matches no stored `task:`
  message in a list, and because `Claim` takes that address from `ClaimRequest.Recipient` (nil for
  such a run), it claims no `task:` message either.
- **Ordering is FIFO** by `CreatedAt` then `ID`, so delivery is deterministic and testable.
- **`RequeueExpired`** is the crash-recovery sweep. `ft serve` (and the `ft up` daemon) runs it
  once at startup and then every minute, and a receiver's `register` also runs it, so a crashed
  receiver's in-flight message returns to `queued` within a minute of its lease lapsing even if
  nobody registers. It is also how at-least-once redelivery happens.
- **`UpdatedAt`** is the compare-and-swap token the other repos already use; a backend may reuse
  the pattern from `TicketRepo.UpdateExpected`.

### Conformance cases (`pkg/store/conformance`)

Add `t.Run("Message", ...)` and `t.Run("Run", ...)`, each backed by the same in-memory factory
contract. The suite must cover, named for what it asserts:

1. `Create`/`Get`/`List` round-trip; duplicate `Create` → `ErrAlreadyExists`; `Get` missing →
   `ErrNotFound`.
2. `Validate`: empty id, empty `To`, unknown state, oversize body, empty project are rejected.
3. `List` filters: by project, exact `To`, state set, `Since`, and `Limit`; `Actor` and `Run` are
   exact stored addresses (`actor:<id>`, `run:<id>`), while `Task` matches `Message.TaskID`
   whatever `To` became, so a message resolved to `run:`/`actor:` is still returned; ordering is
   oldest first; no cross-project leakage.
4. `Claim`: returns the oldest claimable queued message; skips non-claimable addresses; returns
   `ErrNotFound` on empty; sets `State=delivered`, `RunID`, `LeaseUntil`, and increments
   `Attempts`.
5. `Claim` concurrency: N goroutines claiming one queued message — exactly one wins, the rest get
   `ErrNotFound`, and no message is delivered twice.
6. `Claim` addressing: `actor:` messages are claimable by any run of that actor; `run:` only by
   that run; a stored `task:` message only by a run whose `Run.TaskID` matches (and a run whose
   `TaskID` is nil matches none); and a message resolved to `run:`/`actor:` whose `Message.TaskID`
   is set is claimable by that run and still matches the `Task` list filter.
7. `Ack(read)`: `delivered → read`; repeat is idempotent; ack on a non-delivered message errors.
8. `Ack(failed)` / `Nack`: records `Error`, ends terminal `failed`; `Nack` requeues when under
   `MaxAttempts`.
9. `RequeueExpired`: a `delivered` message past its lease returns to `queued` with `Attempts`
   retained; past `MaxAttempts` it becomes `failed`; a `read` message is untouched.
10. `Prune`: removes only the requested terminal states whose `UpdatedAt` (the terminal
    transition, not `CreatedAt`) is before the cutoff; leaves `queued`/`delivered`, younger
    terminal messages, and terminal messages whose `CreatedAt` is old but whose ack is recent.
    A non-terminal state in the request removes nothing.
11. `Run.Register` upsert: same `(project, actor, host, pid)` returns the same `RunID`; heartbeat
    moves `SeenAt`/`LeaseUntil`; `List` separates live from stale; `Delete` removes.

These are the contract the four adapters (memory, jsonfile, jsondir, sqlite) must pass before
t-q7iwpouhr5 is done.

## 7. Delivery protocol: register / claim / ack

One small vocabulary, used identically by the CLI, the `ft serve` transport, and both plugins.
The store is the hub; a receiver is any client of these verbs.

- **register(run)** — announce a session. Payload: `project`, `actor`, `task?`, `session?`,
  `harness`, `host`, `pid`, `can_inject`, `ttl`. Returns `run_id` and `lease_until`. `session` is
  the chief session the receiver belongs to (`FACTOTUM_SESSION`); the sessions page lists that
  session while the run leases and counts the lease as its liveness evidence, so a role that has
  recorded no event yet, or is quiet past the idle window, still reads as working. Idempotent per
  session (section 5). The adapter then heartbeats at `ttl/3`.
- **claim(run_id, wait?)** — atomically take the oldest message claimable by the run (section 4)
  and mark it `delivered` with `lease_until = now + lease`. With `wait`, it long-polls up to
  `wait` before returning empty. This is the poll loop.
- **ack(message_id, run_id, state=read|failed, error?)** — finalize a delivered message. `read`
  stops redelivery; `failed` is terminal and records the reason. The `run_id` is the run that took
  the claim, captured when the claim was issued, not necessarily the receiver's current run: a
  heartbeat answered `not_found` can re-register it while a claim is in flight, and the store
  accepts an ack only from the claiming run. Acking with the fresh id is rejected and leaves the
  message `delivered` under the forgotten run, so a receiver that still gets a rejection logs it
  with the message id and reason and leaves the message to the lease sweep.
- **nack(message_id, run_id, reason)** — the receiver could not handle it now; requeue (or park
  as failed past `MaxAttempts`).
- **heartbeat(run_id)** — renew the run lease. `not_found` (a hub that forgot the run) makes the
  receiver re-register once; it is not a receiver failure.
- **deregister(run_id)** — clean shutdown.

Lease and retry defaults (configurable, machine-scoped like `serve.token`):

- `lease = 30s`: injection is fast, so the receiver acks well within it; a crash past the lease
  redelivers.
- `max_attempts = 5`: after that a message is parked `failed` for a human, never retried forever.
- `claim wait = 25s`: long-poll window under the lease, so the receiver re-registers/refreshes
  cleanly between windows.

Idempotency: delivery is **at-least-once**, and duplicate injection is an accepted cost, not a
solved problem. The durable ack (`read`) is what stops *further* redelivery; the residual window is
a receiver that injects a turn and then crashes before acking, whose message is redelivered after
the lease and injected a second time. The receiver dedupes within its own process on
`message id` (a small in-memory set of recently acked ids), which covers a redelivery while it is
still running, but **not** a duplicate across a restart. A cross-restart ledger would need a
durable processed-id set and is deliberately out of scope here; because an injected turn is visible
in the transcript, the duplicate is low-harm and recoverable. If it proves noisy, add a per-run
processed-id set to the `RunRepo` and check it before injecting.

## 8. Wake: poll is the contract, push is an optimization

- **Poll (default, always correct).** The receiver runs a background loop: long-poll `claim`, and
  on a message inject it as a new user turn in the agent session, then `ack(read)`. This works for
  any adapter regardless of who launched the session, wakes an idle agent (the loop is independent
  of the agent's turn), and crosses hosts. It is the only mechanism the contract requires.
- **Push / direct inject (optional, deferred).** The isolation port today exposes no writable
  channel: `isolation.Command.TTY` is only a boolean request for a terminal, `Stdin` is set once
  at `Exec`, and an execution exposes only `Events`/`Wait` (`pkg/isolation`). So the hub cannot
  write a message into a running child through the existing port. Push therefore means handing the
  message to the *receiver adapter itself* through a side channel the adapter owns (e.g. the
  opencode plugin's own injection API), not through `isolation.Command`. Enabling `ft run` to
  inject into a child it launched would need a new isolation port method and is deliberately out of
  scope for this milestone. A headless one-shot `opencode run` exits after its turn, so a one-shot
  run's *next* invocation is a fresh session that claims on start.
- A run advertises `can_inject` at register, meaning "a receiver adapter is running that can
  accept an out-of-band injection". The hub uses push only when it has such a channel; otherwise
  the poll loop delivers. **Correctness never depends on push** — push is just lower latency, and
  the entire milestone works with poll alone.

This settles open question (3) from the task: timer-poll is the baseline. Direct injection can only
accelerate a session whose receiver adapter already owns an injection channel; making it serve an
`ft run`-owned session is exactly the case that needs the new isolation port method above, so that
acceleration is out of scope for this milestone.

## 9. Cross-host transport over `ft serve`

Local `ft msg` writes to the project backend directly (same DB on the same host). To reach another
host (or the cloud), `ft serve` exposes the same protocol over HTTP. The receiver is an adapter;
the transport is a detail.

Endpoints (JSON, mirroring the CLI verbs; the CLI column names the command that shares the verb):

```
POST /api/msg/send       {from?, to, body, reply_to?, links?}      -> {id, state}   ft msg send
GET  /api/msg/inbox      ?address=&state=&limit=                   -> {messages:[..]} ft msg inbox
GET  /api/msg/get/<id>                                             -> {message}      ft msg get
POST /api/msg/ack        {id, run_id, state, error?}               -> {ok}           ft msg agent ack / ft msg read
POST /api/msg/nack       {id, run_id, reason}                      -> {ok}           ft msg agent nack
POST /api/msg/retry      {id, to?}                                 -> {ok}           (proposed, section 11)
POST /api/msg/prune      {before, states?}                         -> {removed}      (proposed, section 11)
POST /api/msg/register   {actor, task?, session?, harness, host, pid, ...}   -> {run_id, lease_until}
POST /api/msg/claim      {run_id, wait?}                           -> {message?}
POST /api/msg/heartbeat  {run_id}                                  -> {ok}
POST /api/msg/deregister {run_id}                                  -> {ok}
```

The transport is a thin wrapper over the store port: each endpoint maps to one `MessageRepo`/`RunRepo`
call (`send`→`Create`, `get`/`inbox`→`Get`/`List`, `ack`/`nack`/`retry`→`Ack`/`Nack`/`Update`+event,
`claim`→`Claim`, `register`/`heartbeat`/`deregister`→`RunRepo`). The CLI verbs are the same calls, so
there is one behavior with two transports; `ft msg get` is the non-mutating fetch and `ft msg read`
is the mutating fetch-and-mark-read (the transport's `ack` is `ft msg agent ack` for a receiver).

Every item (`inbox`, `get`, and `claim`) carries the sender's `links` (`[{kind, url, title?}]`,
reusing `core.Link`) beside the body, so a remote reader or receiver sees the attachment without a
second fetch. A message without links omits the key. The HTTP `links` array and the CLI
`--link 'kind=url title=text'` fill the same `core.Link` fields, so an attachment looks the same
whichever sender wrote it (section 11).

Auth and trust:

- All `/api/msg/*` endpoints require the serve token in the `Authorization: Bearer` header,
  constant-time compared, exactly like capture. This **diverges from the open graph read side on
  purpose**: private agent comms should not be world-readable to anyone who can reach the
  dashboard. (Open question for Khoi, section 12.) `ft serve` resolves that token from `--token`,
  `FACTOTUM_SERVE_TOKEN`, or `serve.token`, and, when none is set on a scoped serve, mints a random
  one per start and prints it; a remote hub a launched receiver talks to (`serve.url` +
  `serve.token`) therefore needs an explicit token, since a rotated one is not known to `ft run`.
- The token authenticates the *host* to the hub, not the actor; a token holder may send as any
  actor (`from` in the payload). Acceptable for a single-operator hub; documented as the trust
  boundary.
- The hub is the same backend store, so a message sent locally is visible to a remote claimer and
  vice versa — cross-host is purely the transport, not a second store. This is the
  "cloud prerequisite" t-773lhmbml2 builds.
- Multi-project: the transport scopes to the serve target project (or requires `?project=` when
  serving all). The sweeps are global — the store's lease and retention are not project-scoped —
  so a `ft serve` hub recovers a dead remote receiver and trims old terminal history whichever
  project it serves.

## 10. Receiver adapters

Both adapters are thin clients over section 7. Each implements the same four steps: register,
long-poll claim, inject as a user turn, ack.

### opencode plugin (t-fn2gndufxa)

- One local plugin, `factotum-msg.js`, **default-exports a single object that serves both OpenCode
  plugin majors** from the one file; neither half is a fallback for the other. OpenCode 1.x
  (1.18.35 included) finds and calls `server(input)` and, on unload, the `dispose` hook it
  returns — the shape cmux's plugin got wrong. OpenCode 2 finds `id` and `setup(ctx)` and has no
  `server`, so each major ignores the other's half.
- V2 is feature-detected, not configured. At load the module tries
  `await import("@opencode/plugin")` and, when that package exposes `Plugin.define`, spreads
  `Plugin.define({ id: "factotum.msg", setup })` into the default export. OpenCode 1 ships no such
  package, so the import rejects and the plain `id`/`setup` keys already on the export keep it valid
  without the wrapper; a V2 host where the package cannot be resolved still loads a working
  receiver.
- The entrypoints reach the host by different APIs. V1 gets `input.client.session` and injects with
  `promptAsync` (or `prompt`), returning `{ event, dispose }` hooks. V2 gets the context's domains:
  it reads `ctx.event.subscribe` on a background loop, so an awaited `setup` never blocks OpenCode
  startup, and injects with `ctx.session.prompt({ sessionID, text })`. Both feed the same `onEvent`,
  which takes the session id from V2's `event.data` or V1's `event.properties`. Durable state
  (`ctx.storage`, `ctx.session.get`) is a V2 context domain only, so the persisted session and the
  cold-restore record below are V2-only; V1 learns its session from an event.
- Staging is the same file for either major: `ft msg install --harness opencode2` writes
  `factotum-msg.js` into `<config>/opencode/plugins` (`$XDG_CONFIG_HOME`, else `~/.config`), the
  directory `--harness opencode` writes to as well, because V1 and V2 share the config root and both
  load global plugins from there. `ft run`'s harness stages the same bytes at
  `.opencode/plugin/factotum-msg.js` in the run's workspace instead.
- Reads `FACTOTUM_PROJECT`, `FACTOTUM_ACTOR`, `FACTOTUM_TASK_ID` (set by `ft run`),
  `FACTOTUM_SESSION` (the chief session it registers, which the sessions page lists and counts live
  while the run leases — even before the session records an event), and
  `FACTOTUM_MSG_URL`/`FACTOTUM_SERVE_TOKEN` for a remote hub; with no URL it talks to the local `ft`
  store.
- On load: register. Loop: `claim({wait})`; on a message, inject it as a new user turn through the
  entrypoint's session API (a mid-turn agent queues it for the next turn; an idle agent starts a new
  turn), then `ack(read)`. A failure `ack(failed)` with the error. On unload: deregister.
- The loop is off the agent's turn, so injection never blocks tool execution.
- **Cold restore.** OpenCode V2 resumes a surviving session as `opencode -s <session>` with the
  original launch environment gone, so `FACTOTUM_*` never reaches the plugin and an idle agent
  would register nothing while its TUI looks healthy. Every V2 load that has its configuration
  records the identity it registered as (project, actor, task, chief session, harness, bin, store
  and its options, hub URL) plus the OpenCode session it injects into, keyed by the OpenCode
  location (the project hash of the canonical directory). A V2 load with no environment recovers
  that record and registers again; two receivers for one actor split a mailbox, so the recovery
  waits while another run for that actor is live (backing off its liveness poll, because a live
  peer can hold the actor for hours) and adopts once its lease lapses. A restore keeps the record's
  OpenCode session current with the one it injects into, so a second restart resumes the live
  session instead of a stale id (the record's writes are ordered, so the seed a restore starts
  from cannot overwrite a session the host announces while that write is in flight). The record
  never carries the hub token: a restored remote receiver whose token is gone logs that and stays
  inert instead of registering against the wrong store. The record is per location, so a location hosts one agent — the loop
  gives every agent its own directory.
- `ft run`'s opencode harness injects at launch when the run itself is the reason the session
  exists (it passes the initial message, and the plugin claims the rest).
- **`ft` must be reachable where the receiver runs.** With the local transport the plugin shells
  out to `FACTOTUM_BIN` (default `ft`), so a sandboxed or containerized harness image must ship
  `ft` on PATH, or the run must stage a binary and set `FACTOTUM_BIN` to its path in the
  environment. A remote hub (`FACTOTUM_MSG_URL` + `FACTOTUM_SERVE_TOKEN`) is the escape hatch: it
  speaks the token-gated HTTP transport and needs no local `ft`.

### pi extension (t-eedgwt6g6g)

- The same protocol through pi's extension registration and message-injection hook. A structural
  mirror of the opencode plugin so the two do not drift; the shared code is the protocol client.

### cmux migration

- `ft msg send` is the primitive. The `cmux-msg.sh` wrapper was **removed** (t-wp27miss7i) once a
  real loop-agent pair round-tripped over `ft msg` (receiver loaded, role addressing, delivery
  reached `read`); there is no cmux fallback. A peer with no live receiver is a broken launch to fix
  via `ft msg runs --live`, not a surface to paste into. The chief loop addresses peers as `run:<id>`
  (precise) or `actor:<name>` (stable), replacing surface-title disambiguation.

## 11. CLI surface

```
ft msg send <address|ticket-id> [-b body | -] [--from <actor>] [--reply-to <id>] [--link 'kind=url [title=text]']
ft msg inbox [--state queued|delivered|read|failed] [--address X] [--for <actor>] [--run <id>] [--limit N]
ft msg get <id>            # show without changing state
ft msg read <id>           # show and ack read (marks terminal)
ft msg runs [--live]       # list registered runs (adapter liveness)
ft msg install [--harness opencode|opencode2|pi] [--dir <path>]   # stage a receiver for a harness
ft msg agent <register|claim|ack|nack|heartbeat|deregister>   # hidden, stable adapter verbs
```

`ft msg get`/`read` print the body, then a `=== Links ===` block when the sender attached links
(each line is `[kind] url - title`, the title left off when the link has none); a receiver's
injected turn renders the same line, so the agent sees the attachment's name the human does. A CLI
sender sets that title with `--link 'kind=url title=text'`: the separator is one literal space, and
a url is one RFC 3986 token, so the first space is an unambiguous boundary and the title takes the
rest of the argument (spaces and equals signs included); a tab is not a boundary — it stays in the
url and is refused there — any field other than `title=` is rejected, and an untitled `--link
kind=url` is unchanged. This is the same attachment the HTTP `links` array carries, so the two
senders cannot drift. A link field is one line — `core.Link` rejects a blank url or title, and any
control rune, Zl/Zp separator, or bidi embedding/override/isolate control (U+202A–E, U+2066–69) in
either — so the block cannot forge a section; the CLI parses through `core.Link.Validate`, so it
rejects exactly the links the HTTP path rejects. A message stored by an older ft can still carry
one: every backend revalidates a message on `Update`, so rather than wedge it, `MessageService`
repairs the link on the way out (a refused rune becomes a space in a title, is percent-encoded in a
url, and a title left blank is dropped). A `read` that moves a queued row persists the repair; a
delivered row its run acks keeps the stored bytes, though no surface renders them. A ticket's note
links and an artifact's links take the same repair, because their update paths revalidate them too. `get`, `read`, `inbox`, and the `agent claim` wire all
carry the same `links` array, and a linkless message omits it (no empty section, no empty key). The
inbox's text table stays a summary row — the links are in its `-o json|yaml` entry and in `get`.

**Proposed, not built.** Four operator verbs were considered for the first slice and are kept here
so the intent is not lost, but none of them exists yet: the fence is marked `design` for exactly
that reason, and the drift check requires the mark. The store port already has the calls they need
(`Nack`, `RequeueExpired`, `Prune`), so each is a thin CLI addition over an existing method; until
one lands, a receiver uses the `agent` verbs, and the serve sweeps recover expired deliveries and
prune old terminal history with no operator command.

```design
ft msg ack <id> [--failed -e <reason>]     # explicit state ack for adapters/humans
ft msg nack <id> [-e <reason>]             # requeue (or park failed past max attempts)
ft msg retry <id> [--to <address>]         # requeue a failed message, optionally re-address
ft msg prune [--before <time>] [--state read|failed]   # drop old terminal messages
```

- Output follows the repo shapes: single-result commands print `id/action/.../project/repo` yaml-
  like lines (with `sent: true`/`read: true`); list commands print a table; `get` prints a human
  block. `-o json|yaml` are the machine interfaces.
- `--from` defaults to the configured actor (`FACTOTUM_DEFAULT_ACTOR`, else the machine default).
- The hidden `ft msg agent *` verbs are the stable interface the plugins shell out to when they
  cannot speak HTTP; they are the same call the transport implements.

## 12. Dashboard, events, retention, security

### Events and the dashboard

- New event kinds: `message.sent`, `message.claimed`, `message.read`, `message.failed`. Each
  transition appends one, so the existing broker/SSE path lights up with no new mechanism.
- `Event.TicketID` is set from `Message.TaskID` when the message concerns a task (section 4), so it
  surfaces on the task detail even when `To` was resolved to `run:`/`actor:`.
- The dashboard snapshot gains a `messages` list (project-scoped, like `updates`), so the
  right-pane shows a live message stream; because the app refetches on every `/events` update, a
  send appears immediately. This answers open question (5): messages compose with events by being
  an event source, not a parallel channel.

### Retention and limits

- **Body limit:** default 16 KiB, machine-configurable. Enforced at send (`ErrInvalid`), never
  silently truncated — a sender must know.
- **Retention:** terminal `read` messages pruned after 30 days; terminal `failed` after 90 days;
  `queued`/`delivered` never auto-pruned. `ft serve` (and `ft up`) enforces it: the prune sweep
  runs once at startup and then hourly, taking its cutoff from the service clock, and the store's
  `Prune` only ever matches terminal states, so a sweep cannot delete outstanding work. There is
  no manual prune verb yet; section 11 keeps it proposed. Reuses the `Prune` port method, so it is
  backend-uniform.
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
  opencode receivers). The chief loop switched to `ft msg` once q7iwpouhr5 + the opencode receiver
  landed; the cmux fallback was removed after a real loop-agent pair round-tripped (t-wp27miss7i).

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
