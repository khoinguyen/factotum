# serve dashboard checks

`internal/serve` is the `ft serve` app: the shadcn/ui read side plus a
token-gated capture write (`/capture`). Its tests are layered:

- `serve_test.go`, `po_test.go`, `app_test.go`, `capture_test.go`, and
  `broker_test.go` assert behavior and structure: read-only routing, the app
  shell on every read route, the JSON snapshot and detail projections, SSE
  freshness, the shared poll fan-out, and the capture write path (token
  required, unauthorised writes rejected, sentence split).
- Visual layout is not asserted in Go because Go has no layout engine; the
  read-side components are exercised by the `web/` vitest suite instead.

## shadcn/ui read side

The read side is the shadcn/ui app (Vite + React + TypeScript + Tailwind), served
from `web/dist`, which `web/web.go` embeds with `go:embed`. `mise run build-web`
builds it; `mise run build` embeds the result. The app shell answers the
dashboard root `/` and the client routes `/idea`, `/task`, `/memory`, `/doc`; the
client router reads the id from the path. The data comes from `/api/snapshot`
(dashboard) and `/api/<kind>/<id>` (drill-downs), and the app refetches on the
`/events` SSE stream, so a mutation appears live. The write side is still the
server-rendered `/capture` page until its own port (t-3g3qpqlhli).

Go tests inject a small in-memory asset tree (`Options.Assets`) so they do not
depend on a frontend build. The app's own render path is covered in `web/` by
`mise run web-test` (vitest + jsdom mounts `<App>` against an empty project, a
null-list snapshot, and a populated one). `mise run smoke-web` is the end-to-end
check against the real binary: it starts `ft serve` on a throwaway store, fetches
the app shell and its hashed asset from `/app`, mutates the graph through a
second `ft` process, and asserts a live SSE `update` plus the new task in
`/api/snapshot`.

## Capture auth

Writes need a shared token from the machine config (`[serve] token`) or
`FACTOTUM_SERVE_TOKEN`; the read side is never gated. A missing token disables
capture (writes return 403), and `--all` mode has no single target so it disables
capture too. Comparison is constant-time. Capture stores the first line as the
title and the rest as the body, so the raw sentence survives; enrichment is the
later grooming step, not part of the write.

## Reason-chip wrapping (t-mw2k6rwonj)

That regression lived in the retired server-rendered dashboard HTML. The read
side is now the shadcn/ui app, where a long wait reason wraps with Tailwind's
`break-words`/`min-w-0` on the reason element (see `web/src/components/`).
