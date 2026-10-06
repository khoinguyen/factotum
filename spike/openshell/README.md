# OpenShell isolation-backend spike

Evidence and decision for task **t-k3duthkrxo** (`ft run: OpenShell backend spike`), the child of
the OpenShell research task t-sbbqvslnty (memory `art-4vtmhbtnyg`). It resolves the open decision
**V7: CLI shell-out vs Go SDK** and verifies the safety properties the `IsolationBackend` port
(`pkg/isolation`) will depend on.

Verified locally on macOS arm64 / OrbStack against a live OpenShell **0.1.2** gateway
(`sh.brew.openshell`, `https://localhost:17670`) and the `ghcr.io/anomalyco/opencode:latest` image
(opencode 1.18.34). Everything ran in throwaway sandboxes with a **fake** provider key; nothing
touched the host filesystem, the real factotum DB, or a real credential.

## Decision (V7): shell out to the `openshell` CLI

Implement the OpenShell `IsolationBackend` by **shelling out to the `openshell` CLI**, not by
depending on `github.com/NVIDIA/OpenShell/sdk/go`, for the first version. The SDK stays the likely
long-term choice; it is not ready today.

Rationale, in order of weight:

1. **File transfer decides it.** The SDK's `FileInterface` has no transport: `Files().Download`
   returns `ErrTransportNotAvailable` (the SSH transport in the open-source module is a stub), so the
   SDK cannot move a workspace in or results out. The CLI's `sandbox upload` is the only working bulk
   path — it is directory-aware and, inside a git work tree, honors `.gitignore`. (Download is broken
   on both; see caveats — the adapter must stream files over `exec` regardless.)
2. **The SDK's gateway helper refuses the local gateway's auth mode.** `gateway.NewClient` maps
   `auth_mode: mtls` to `ErrUnsupportedAuthMode`, and the local gateway *is* mTLS. Manual TLS wiring
   works (proven below), but then the adapter reimplements the gateway discovery and credential
   loading the CLI already does.
3. **Dependency and churn cost.** The SDK is untagged (`v0.0.0-<date>-<sha>`, pre-1.0) and its README
   documents source-incompatible changes; it pulls gRPC, protobuf, and oauth2 into the main module.
   The `IsolationBackend` surface is small and fully covered by the CLI.
4. **Structured output removes the usual CLI objection.** `sandbox create/get/list` support
   `-o json`, so lifecycle state is parsed from a stable document, not human text.

The SDK's genuine win is its **fake client**, which would let the conformance suite
(t-s5pnykouqa) run without a gateway. That is not a blocker: the port's test double belongs in
`pkg/isolation/fake` anyway, so the suite does not need the SDK. Revisit the SDK when it ships a
file transport, supports mTLS in `gateway.NewClient`, and tags a release.

## What the spike verified

The strict policy (`filesystem_policy` read-only `/usr,/lib,/bin,/etc,/proc,/dev/urandom`,
read-write `/tmp,/dev/null`, `include_workdir: true`, `process.run_as_user/run_as_group: "1000"`, no
`network_policies`), reproduced end to end (full transcript: [`transcript.txt`](transcript.txt)):

- **Headless lifecycle**: `sandbox create --detach`, `upload`, `exec`, stop/start, `delete`.
- **Non-root**: `id` → `uid=1000 gid=1000`.
- **Read-only system paths**: writing `/etc` is denied; `/sandbox` (workdir) is writable.
- **Deny-by-default egress**: `wget https://example.com` → `Permission denied`; the OCSF log shows
  `NET:OPEN DENIED /bin/busybox -> example.com:443`.
- **Process controls**: `mount -t tmpfs` → `permission denied`; `unshare -Ur` → `EPERM`.
- **Provider placeholder injection**: the sandbox env holds
  `OPENROUTER_API_KEY=openshell:resolve:env:<id>_OPENROUTER_API_KEY`, never the raw key. The
  effective policy scopes the endpoint rule to the binary `/usr/local/bin/opencode`; opencode's own
  call to `openrouter.ai:443` is `ALLOWED … engine:l7` while its phone-home to
  `models.opencode.ai` and `registry.npmjs.org` is `DENIED`.
- **Advisor is manual**: denied hosts surface as `Status: pending` proposals (observed after ~10s);
  none are auto-approved.
- **Workspace persistence**: a marker written before `stop` survives `start`, and `exec` works again.

The SDK half is in [`sdk_probe.sh`](sdk_probe.sh) / [`sdk-transcript.txt`](sdk-transcript.txt):
manually wiring the CLI's mTLS material into `v1.NewClient` connects and `Exec().Run` returns
`uid=1000 …`; `Files().Download` returns `ErrTransportNotAvailable`.

## Friction / caveats found

- **Authored policy schema**: the top-level key is `filesystem_policy`, not `filesystem` (the
  research sketch was off); unknown keys are rejected outright.
- **`sandbox download` is broken against BusyBox images.** It resolves the source with
  `realpath -e --`, which BusyBox `realpath` rejects, so it fails on the opencode (Alpine) image.
  Stream the file out with `exec` + `base64` instead (what `spike.sh` does).
- **Sandbox names are capped at 19 characters.**
- **No host bind-mount** (as researched): the workspace moves via upload/download or persists across
  stop/start; `upload` is directory-aware and, inside a git work tree, `.gitignore`-aware — which is
  what the adapter should use (a cloned repo is a work tree, so this holds in the real case).
- **Provider cleanup is asynchronous**: `sandbox delete` returns before cleanup completes, so deleting
  the provider immediately can fail with "still attached"; retry the provider delete and never swallow
  it, or the provider (and its credential material) leaks into gateway state.
- **Advisor proposals are asynchronous** (~10s) and stay pending; keep auto-approval off.
- **Alpha / version pin**: pin the OpenShell version the adapter targets; the installer guards
  breaking gateway-state changes across releases.

## Run it

```sh
mise run spike-openshell       # CLI end-to-end transcript (throwaway, fake key, self-cleaning)
mise run spike-openshell-sdk   # SDK mTLS + Files-transport probe (throwaway sandbox)
```

Both need a local OpenShell gateway and the opencode image. They create their own throwaway
sandbox, provider, and `mktemp` workspace, and delete them on exit (including on failure); the
provider delete is retried while the asynchronous sandbox deletion settles, so a successful run
leaves nothing behind. Never point them at real credentials.
