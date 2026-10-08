# serve dashboard checks

`internal/serve` is the `ft serve` app: open, live reads plus a token-gated
capture write (`/capture`). Its tests are layered:

- `serve_test.go`, `po_test.go`, `capture_test.go`, and `broker_test.go` assert
  behavior and structure: read-only routing, page/fragment rendering, the reason
  chip's class and wrapping rule, SSE freshness, the shared poll fan-out, and the
  capture write path (token required, unauthorised writes rejected, sentence
  split).
- Visual layout is not asserted in Go because Go has no layout engine. The
  reason-chip fix was verified out of band in headless Chrome; the procedure is
  recorded here so the CSS test has a reproducible reference.

## shadcn/ui web app

`ft serve` also hosts the shadcn/ui app (Vite + React + TypeScript + Tailwind)
under `/app`, served from `web/dist`, which `web/web.go` embeds with `go:embed`.
`mise run build-web` builds it; `mise run build` embeds the result. The app reads
`/api/snapshot` (the same projection the rendered pages use) and refetches on the
`/events` SSE stream, so both views stay live and cannot drift.

Go tests inject a small in-memory asset tree (`Options.Assets`) so they do not
depend on a frontend build. The app's own render path is covered in `web/` by
`mise run web-test` (vitest + jsdom mounts `<App>` against an empty project and a
null-list snapshot). `mise run smoke-web` is the end-to-end check against the
real binary: it starts `ft serve` on a throwaway store, fetches the SPA shell and
its hashed asset from `/app`, mutates the graph through a second `ft` process,
and asserts a live SSE `update` plus the new task in `/api/snapshot`.

## Capture auth

Writes need a shared token from the machine config (`[serve] token`) or
`FACTOTUM_SERVE_TOKEN`; the read side is never gated. A missing token disables
capture (writes return 403), and `--all` mode has no single target so it disables
capture too. Comparison is constant-time. Capture stores the first line as the
title and the rest as the body, so the raw sentence survives; enrichment is the
later grooming step, not part of the write.

## Reason-chip wrapping (t-mw2k6rwonj)

Regression: a task with several unresolved dependencies rendered its reason in a
`.chip` (which is `white-space:nowrap`), so on a phone-width viewport the chip
overflowed and was clipped by `overflow-x:hidden`.

`internal/serve/dashboard.html` gives `.chip.reason` its own wrapping rule
(`white-space:normal; overflow-wrap:anywhere; min-width:0; max-width:100%`).
`TestReasonChipWraps` asserts the served page puts the reason in a
`class="chip reason"` element and keeps those declarations.

Headless layout check (run manually; needs a local Chrome/Chromium):

1. Start a dashboard whose store has a task with several unresolved deps, e.g.
   `ft serve --bind 127.0.0.1:8484` against a throwaway sqlite store.
2. In headless Chrome at 320/375/414/1280px widths, load `/` and evaluate:
   - `document.documentElement.scrollWidth <= window.innerWidth` (no horizontal
     scroll), and
   - the `.chip.reason` element's `getBoundingClientRect().right` is within the
     viewport.

The fix was independently confirmed this way during PR #116 review: at 320/375/
414/1280px no element overflowed, and the full blocker list rendered wrapped. A
Go assertion cannot measure this, so the declarations test stands in as the
regression guard.
