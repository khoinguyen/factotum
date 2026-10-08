# serve dashboard checks

`internal/serve` is the `ft serve` app: the shadcn/ui app is both the read side
and the capture write side. Its tests are layered:

- `serve_test.go`, `po_test.go`, `app_test.go`, `capture_test.go`, and
  `broker_test.go` assert behavior and structure: read-only routing, the app
  shell on every client route, the JSON snapshot and detail projections, SSE
  freshness, the shared poll fan-out, and the capture write path (token
  required, unauthorised writes rejected, kind validation, sentence split).
- Visual layout is not asserted in Go because Go has no layout engine; the app's
  components (including the capture form) are exercised by the `web/` vitest
  suite instead.

## shadcn/ui app

The app (Vite + React + TypeScript + Tailwind) is served from `web/dist`, which
`web/web.go` embeds with `go:embed`. `mise run build-web` builds it; `mise run
build` embeds the result. The app shell answers the dashboard root `/` and the
client routes `/capture`, `/idea`, `/task`, `/memory`, `/doc`; the client router
reads the id from the path. The data comes from `/api/snapshot` (dashboard) and
`/api/<kind>/<id>` (drill-downs), and the app refetches on the `/events` SSE
stream, so a mutation appears live. `/capture` renders the capture form, which
posts to `/api/capture`.

Go tests inject a small in-memory asset tree (`Options.Assets`) so they do not
depend on a frontend build. The app's own render path is covered in `web/` by
`mise run web-test` (vitest + jsdom mounts `<App>` against an empty project, a
null-list snapshot, a populated one, and the capture form). `mise run smoke-web`
is the end-to-end check against the real binary: it starts `ft serve` on a
throwaway store, fetches the app shell and its hashed asset from `/app`, mutates
the graph through a second `ft` process, and asserts a live SSE `update` plus the
new task in `/api/snapshot`.

## Capture auth

Writes need a shared token from the machine config (`[serve] token`) or
`FACTOTUM_SERVE_TOKEN`; the read side is never gated. The token travels in the
`Authorization: Bearer` header only - a token in the query string or body is
ignored - so it cannot leak through logs or a Referer header. A missing token
disables capture (writes return 403), and `--all` mode has no single target so it
disables capture too. Comparison is constant-time. `GET /api/capture` tells the
app whether to show the form; `POST /api/capture` stores a JSON
`{kind, text}` as an idea (default) or a bug, returning the stored capture's
detail URL. It stores the first line as the title and the rest as the body, so
the raw sentence survives; enrichment is the later grooming step, not part of the
write.

## Reason-chip wrapping (t-mw2k6rwonj)

That regression lived in the retired server-rendered dashboard HTML. The read
side is now the shadcn/ui app, where a long wait reason wraps with Tailwind's
`break-words`/`min-w-0` on the reason element (see `web/src/components/`).
