# GitHub backend: port-mapping spike + conformance gap report

Evidence and decision for task **t-ug37z2x5ef** (`GitHub backend: port-mapping spike + conformance
gap report`). It maps every store port in `pkg/store/store.go` onto GitHub primitives, names each
unclean mapping with a proposed fix, fixes the `UpdateExpected`/CAS concurrency contract, and lists
the cases in `pkg/store/conformance` a GitHub backend cannot satisfy. **No production backend code
is written here**; the implementation is t-mlgrurpe67, gated on this report.

The GitHub REST claims below were checked against the live API on a throwaway repository (script:
[`probe.sh`](probe.sh); decisive outputs quoted inline). The account used is a **GitHub Free**
personal account, which matters where a paid plan changes the answer (private wikis, issue fields).

## Verdict

**Recommend the local-index hybrid.** The port surface is not GitHub-shaped, and no arrangement of
GitHub primitives satisfies it on its own. GitHub issues are the durable, human-visible **system of
record for task content**; a local index is the **read model, serialization layer, and CAS
authority**. That local half already exists: **`pkg/store/jsondir`**, the shipped partitioned/indexed
backend (registered in `pkg/store/builtins`, passes `conformance.Run`; migration from the
single-document store is t-bhizhhclcn, search-index parity is t-bbyg7tyypx, successor idea
t-aa5c2dqqki). The older design idea t-ewr3xidxtk is still `cancelled` — use the shipped package; do
not reopen a dead idea or build a second index. Writes flow through the index to the issue; reads are
served from the index; drift is reconciled against the issue by ETag / `updated_at`.

Two findings drive the recommendation:

1. **GitHub cannot do the CAS the port demands.** `If-Match` on an issue `PATCH` is rejected
   outright — `400 Conditional request headers are not allowed in unsafe requests unless supported
   by the endpoint`. Conditional **reads** work (`If-None-Match` → `304`), so the ETag is a usable
   change detector, but it cannot be enforced server-side on a write. `UpdateExpected` has to be
   serialized somewhere, and GitHub offers no such place.
2. **Half the `Backend` interface has no GitHub representation at all.** `EventRepo`,
   `MessageRepo`, and `RunRepo` (and, for a multi-repo project, `ProjectRepo`) are not issues.
   Because the conformance suite runs every repo through one `Backend`, a GitHub backend *must*
   carry a local implementation of these regardless of where tasks live — the hybrid is forced by
   the port shape, not chosen for convenience.

The pleasant surprise: **GitHub has native issue dependencies**, and `GET
/issues/{n}/dependencies/blocking` is exactly the reverse-edge lookup `TicketFilter.DependsOn`
wants — better than the label/search convention the task body assumed. Details below.

## How this was verified

The decisive outputs quoted below came from an **ad-hoc probe run** against three live throwaway
repos on the authenticated account: `khoinguyen/ft-github-spike-1791454543` (issues, deps, CAS,
labels, milestones, search) and `khoinguyen/ft-wiki-probe-1791454706` + `khoinguyen/ft-wiki-pub-1791454718`
(wikis). [`probe.sh`](probe.sh) is that run **cleaned up and made reproducible**; it creates
`ft-spike-probe-*` / `ft-spike-wiki-*` instead, so its repo names differ from the ad-hoc ones.
What was run for real (live API) vs. taken from the docs is called out per claim. The account token
has **no `delete_repo` scope**, so neither the ad-hoc repos nor `probe.sh`'s own repos can be
deleted without a one-time `gh auth refresh -s delete_repo` (see Cleanup).

---

## TaskRepo

An issue is the task document. The clean fields title it; everything else needs a convention.

| Port field / op | GitHub primitive | Clean? | Fix / caveat |
| --- | --- | --- | --- |
| `Title` | issue `title` | yes | — |
| `Description` | issue `body` | yes | — |
| `CreatedAt` / `UpdatedAt` | `created_at` / `updated_at` | yes | `updated_at` is the only change token GitHub exposes on the issue |
| `ID` | `owner/repo#number` | **no** | numbers are repo-scoped and only unique within a repo; `TicketID` is opaque. Fix: stable synthetic id minted by the index, stored in the issue body front-matter, mapped to `owner/repo#number` |
| `ProjectID` | none | **no** | Fix: label `project:<id>` (or index-side mapping) |
| `Repo` | implicit: the issue's repository | **no** | a task names a repo that need not be the issue's repo; cross-repo projects have no single issue type. Fix: label `repo:<name>` and let the index hold the authoritative mapping |
| `Kind` (task/milestone/idea/bug) | none | **no** | Fix: label `kind:<k>`. **Do not** use GitHub Milestones: a GitHub milestone is a repo-scoped grouping with a due date, while a factotum milestone is a gated ticket with deps — the shared word is a trap |
| `Status` (6 states) | `state` open/closed only | **no** | 6→2. Fix: label `status:<s>`, with `done`/`cancelled` also closing the issue; the index keeps the two consistent |
| `AssigneeID` | `assignees[].login` | partial | login, not factotum `ActorID`; assignee changes are silently dropped without push access |
| `WaitingOn []ActorID` | none | **no** | Fix: labels `waiting-on:<actor>` |
| `Labels []string` | `labels[].name` | partial | **label names cannot contain commas** — `{"name":"a,b"}` → `422 Validation Failed`; labels are arbitrary strings in the port, and GitHub's own list endpoint takes `labels` as a comma-separated query, so a comma-bearing label can be neither stored nor filtered. (The factotum CLI's `--label` is repeatable AND and does not split on commas, `internal/cli/task.go`, so this is a GitHub-side limit only.) Fix: escape/reject, or keep labels index-side |
| `Priority int` | none (issue fields are org-owned only) | **no** | Fix: label `p:<n>` or index-side |
| `Deps []TicketID` | issue dependencies (`blocked_by`) | partial | native and clean within a repo; **cross-repo dependencies are not expressible**, and the conformance creates a cross-project dep. Fix: native edges for same-repo, label `dep:<id>` fallback for cross-repo |
| `Notes []Note` | issue comments | **no** | comment ids/`created_at`/`user` map, but `Note.System`, `Note.Links`, and note ids need body front-matter; generated (system) notes would surface as human noise in the issue thread |
| `Milestone *MilestoneMeta` | none (see Kind) | **no** | Fix: index-side, mirrored as labels/body |
| `NotBefore`, `Snooze` | none | **no** | Fix: body front-matter / labels; both are ranking concerns the index already owns |
| `Groomed`, `AcceptanceCriteria` | none | **no** | Fix: label + body section; the index is authoritative |
| `Create` | `POST /issues` | yes | duplicate → `ErrAlreadyExists` must be enforced by the index (GitHub will happily create duplicates) |
| `Get` | `GET /issues/{n}` | yes | 404/410 map to `ErrNotFound`; a transferred issue returns `301` |
| `List(filter)` | `GET /issues` + client filtering | **no** | the port's filter is an exact AND over fields GitHub cannot query; ordering is unpinned (conformance pins id/title order) and pagination is capped at 100/page. Fix: index-side list |
| `Update` | `PATCH /issues/{n}` | yes | last-writer-wins; no precondition |
| `UpdateExpected` | none | **no** | CAS is impossible server-side; see the concurrency contract |
| `Delete` | none usable | **no** | `DELETE /issues/{n}` → `404` on a user-owned repo (`GET` afterwards → `200`); deletion is admin/owner- and org-policy-gated, and after it `GET` returns `410`. Fix: soft-delete via `state=closed` + `state_reason=not_planned` + a `deleted` label, with the index filtering deleted tasks so `Get` returns `ErrNotFound` |

## TaskFilter.DependsOn

The port wants the direct dependents of a task without scanning every task:

```go
// pkg/store/store.go, TicketFilter.DependsOn
// A reverse-edge lookup, so a backend should serve it without scanning every task.
```

**Verified**: GitHub issue dependencies are a first-class API. With `B` blocked by `A` and `C`
blocked by `A` and `B`:

```
dependents of A (GET issues/A/dependencies/blocking) = [4,5]   (B,C)
A.blocked_by = []    B.blocked_by = [3]    C.blocked_by = [3,4]
```

So `GET /repos/{owner}/{repo}/issues/{n}/dependencies/blocking` *is* the `DependsOn` reverse edge,
indexed by GitHub — much cleaner than a label/search convention. It is **not sufficient alone**:

- **Cross-repo edges** are not representable (the conformance has project `prj-1` task depending on
  a `prj-2` task), so a label fallback is still needed.
- It returns whole issues, which then need the rest of the filter (project/status/kind/labels) and
  the port's deterministic ordering applied — index work.
- The conformance composes `DependsOn` with `Search` (`scoped("a")` + `"retries"`); that
  intersection must be served by the index.

**Fix:** native dependencies where the edge is same-repo; a `dep:<id>` label (or index-only edge)
for cross-repo; always serve the composed filter from the index.

## Search (task and artifact)

The port pins strict ranking and matching (`LexicalScore`: every term must prefix-match title >
description/notes; case-insensitive; empty query returns all in scope ordered by title then id;
system notes excluded). GitHub search is a different function:

**Verified** (`/search/issues`):

```
terra (prefix of "terraform") -> {"total":0}          # no token-prefix matching
terraform                     -> {"total":2,...}       # case-insensitive, works
terraform apply (AND)         -> {"total":1}           # multi-term AND matches the port
search budget                 -> 30/min
```

Plus, from the docs: results are best-match ranked (not `LexicalScore`), capped at 1000, eventually
consistent, and limited to title/body/comments with no way to exclude system notes.

**Unclean mapping:** `Search` cannot be served by the GitHub search API in a way that satisfies the
conformance suite. **Fix:** the index owns an inverted index over title/brief/body/notes (the
existing `LexicalScore`); GitHub search is at most an optional cross-repo discovery aid, never the
contract implementation.

## ArtifactRepo

The task body proposed wiki pages for artifacts (spec/doc/memory). The spike shows the wiki is a
weak substrate:

**Verified**: no REST endpoint (`/wiki` → `404`); a private repo on a **Free** account cannot even
enable the wiki (`PATCH has_wiki=true` returns `true` but a re-read is `false`); on a public repo
`has_wiki` is `true` but the `.wiki.git` repository does not exist until a page is created in the
**web UI** (empty-wiki clone fails). Per the docs, private-repo wikis require Pro/Team/Enterprise.

Wiki pages also lack every structured field artifacts carry (`ID`, `Kind`, `TicketID`, `Brief`,
`Links`, timestamps) and are not covered by the search API.

**Unclean mapping.** Fixes, in order of preference:

1. **Store artifacts as files in the main repo** (e.g. `ft/artifacts/<id>.md`) via the contents API:
   works on private repos, gives git history (a natural event log), and front-matter carries the
   structured fields. Same idea as the wiki, without the wiki's availability and bootstrap problems.
2. **Keep artifacts in the index** and treat the repo tree as an export. `task_check` artifacts in
   particular are derived caches and belong index-side.
3. Wiki pages as the task body assumed — reject on the evidence above, or accept only as a
   best-effort mirror on plans that have private wikis.

## ActorRepo

| Op | GitHub primitive | Fix / caveat |
| --- | --- | --- |
| `Create` | none — users/apps are global | register an observed `login` in the index |
| `Get` | `GET /users/{login}` | stable numeric `id`; `login` can be renamed |
| `FindByName` | `GET /users/{login}` | case-insensitive and renameable; no uniqueness the port can rely on across time |
| `List` | none for "all actors" | index-side |
| `Update` | none | `Active` has no GitHub equivalent; index-side |
| `Delete` | none | cannot delete a user; index-side deregistration |
| `Kind` (human/agent) | `type` User/Bot/Organization | `github-actions[bot]` is `type: Bot`; a human-run agent is still a `User`. Fix: index-side classification |

**Unclean mapping**, but low risk: the actor registry is small and effectively a cache of GitHub
identities. Fix: the index owns `Actor`, keyed by the numeric user id, with `login` as a mutable
attribute.

## EventRepo

The port is an append-only journal with `ProjectID`, `TicketID`, arbitrary `EventKind`, `Since`,
and `Limit`. GitHub offers issue timelines, but:

- They are GitHub's event vocabulary, not factotum's (`task.note_added`, `message.claimed`, …).
- There is no project-level or run-level timeline; events for something that is not an issue have
  nowhere to go.
- No append of a custom event, no guaranteed `Since`/`Limit` semantics, and retention/rate limits.

**Unclean mapping / cannot satisfy.** Fix: the index owns an append-only event log (`pkg/store/jsondir`
is the local half; per-shard JSONL is the shape to extend it with). This is the same `EventRepo`
that `MessageRepo`/`RunRepo` need, so the hybrid supplies all three.

## Out of scope in the task body, decisive for the verdict

The acceptance criteria name Task/Event/Artifact/Actor/Search/DependsOn, but `Backend` also
requires `ProjectRepo`, `MessageRepo`, and `RunRepo`, and **the conformance suite runs all of
them**:

- **ProjectRepo** — no GitHub primitive; a project spans repos. If projects map to repos, the
  cross-repo dependency case fails and `Repos []Repository` has no home. Fix: index-side project
  registry.
- **MessageRepo / RunRepo** — inbox/claim/lease/ack and run heartbeats are runtime state with no
  GitHub analogue. Fix: index-side (or a separate runtime store).

This is why the hybrid is not merely recommended but **required by the port shape**: any adapter
that passes `conformance.Run` must implement these repos locally.

---

## The concurrency contract for `UpdateExpected` / CAS

GitHub cannot host the CAS. The contract must live in the index:

1. **Token (the compared value is a time, not an ETag).** The port's signature is
   `UpdateExpected(ctx, task, expected time.Time)` and it compares the stored `UpdatedAt`, so the
   index's CAS token is the task's **`UpdatedAt` (`time.Time`)** — the same type the port compares.
   The index also caches the issue's ETag (and remote `updated_at`) beside it, but those are only
   change detectors for the conditional `GET` and drift reconciliation; they are never the value
   `UpdateExpected` compares. An opaque ETag *cannot* be compared to the caller's `time.Time`.
2. **`UpdateExpected(task, expected)`.**
   1. Compare `expected` to the index's stored `UpdatedAt` for `task.ID`. Mismatch →
      `core.ErrConflict`, no write.
   2. Acquire the per-task write lock (a process mutex for a single instance; a lock file for a
      shared directory — reuse the jsondir design).
   3. `PATCH` the issue, then re-`GET` (or read the PATCH response) to capture the new ETag /
      `updated_at`.
   4. Store the issue's new `updated_at` as the index's CAS token (refresh the cached ETag too),
      then commit the index change.
   5. On network or partial failure: do **not** report success. Mark the index entry dirty and
      reconcile on the next sync, so the caller can retry.
3. **Reconciliation.** A file watcher / poll pulls `updated_at` for tracked issues; when a remote
   change lands, re-fetch, reindex, and update the stored `UpdatedAt`. A local pending write whose base `UpdatedAt`
   changed underneath is surfaced as `ErrConflict` rather than silently clobbering.
4. **Cross-machine limit.** Two `ft` processes on different hosts with independent indexes still
   race: each index CAS succeeds locally, then both `PATCH`. GitHub applies last-writer-wins with no
   error. This is a **known limitation**, not something the port can prevent. Mitigations: detect
   the loser by re-reading after the PATCH and reporting `ErrConflict`; or, where strictness is
   required, fall back to a Git-ref mutex (create `refs/heads/ft-lock-<id>` — an atomic
   create-if-absent — and delete it on completion, with stale-lock recovery). The git-ref mutex is
   kept as a documented fallback, not the default, because of its latency and crash-recovery cost.

**Why not the alternatives:** best-effort read-compare-write has a TOCTOU race and cannot honestly
return `ErrConflict`; the git-ref mutex is heavy for every write and still does not compare
`UpdatedAt` the way the port specifies. The index is the only layer that can compare `expected` to
the stored `UpdatedAt` and refuse atomically, and it is needed anyway for Search, DependsOn composition, Events,
Messages, and Runs.

---

## Conformance cases a GitHub backend cannot satisfy

Against `pkg/store/conformance`, with the hybrid these are the cases the **raw GitHub half** cannot
satisfy (the local half must cover them; several are outright blockers if taken literally as
GitHub-only):

- **`Project`** — no project entity; `Create`/`Update`/`Delete`/`List` and `Repos`/`Policy` have no
  primitive. Local-only.
- **`Actor`** — `Create`/`Delete`/`Update` (users are global), `Active`, and `FindByName`
  uniqueness over time. Local-only.
- **`Task`**
  - deterministic `List` ordering (the suite asserts id/title order; GitHub returns created/updated
    order, 100/page).
  - `Delete` then `Get` → `ErrNotFound` (issue delete unavailable/soft-deleted).
  - `UpdateExpected` current-succeeds / stale-`ErrConflict` (no server CAS).
  - `NotBefore`, `Snooze`, `WaitingOn`, `Priority`, `Groomed`, `AcceptanceCriteria`, `MilestoneMeta`,
    `Notes[].System`, `Links` — no primitives.
  - `Kind` vs. GitHub Milestone collision.
  - label names containing commas (the port's labels are arbitrary strings; GitHub rejects commas).
- **`TaskCaptureKinds`** — fine via `kind:` labels.
- **`TaskDependents`** — cross-project edges; deterministic ordering; `Delete` removes outgoing
  edges; composition with `Search`. Native `blocking` covers same-repo edges only; the rest is
  local.
- **`TaskSearch`** — prefix matching, `LexicalScore` ranking, empty-query-returns-all ordering,
  system-note exclusion, reindex-on-update/delete, determinism; GitHub search misses all of these
  (eventual, 30/min, 1000-cap, best-match).
- **`Artifact` / `ArtifactSearch`** — wiki as a store (availability, bootstrap, structured fields,
  no search API). Local-only.
- **`Event`** — append-only custom journal with `Since`/`Limit`, project/task scope. Local-only.
- **`Message` / `Run`** — no GitHub primitive whatsoever. Local-only.

Net: the GitHub-specific portion is **tasks, labels, native same-repo dependencies, comments, and
identity lookups**; everything else is the local index. That split is the whole design.

## Follow-ups (for the implementation task t-mlgrurpe67)

- Decide the issue↔task identity scheme and front-matter format before any write path.
- Extend **`pkg/store/jsondir`** as the local half rather than inventing a second store; the
  multi-writer and watcher decisions made there apply directly. (Its search-index parity is
  t-bbyg7tyypx; do not reopen the cancelled idea t-ewr3xidxtk.)
- Treat `task_check` artifacts as index-only.
- Revisit the wiki only if targeting plans with private wikis; prefer repo-tree files.
- Consider splitting `Backend` into capability interfaces (core vs. runtime) so a future backend can
  honestly declare "I serve tasks/artifacts, compose your own runtime store" — a cleaner port shape
  than forcing every adapter to embed a local store. Flag for a separate task; do not change ports
  in the spike.

## Cleanup

The probe account lacked `delete_repo`, so the three ad-hoc throwaway repositories remain and should
be deleted (`gh auth refresh -h github.com -s delete_repo` once, or via the web UI):

- `khoinguyen/ft-github-spike-1791454543`
- `khoinguyen/ft-wiki-probe-1791454706`
- `khoinguyen/ft-wiki-pub-1791454718`

`probe.sh` creates `ft-spike-probe-*` / `ft-spike-wiki-*` and requires create **and** delete scope
to clean them up after itself.
