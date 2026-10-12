import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import type { ArtifactDetail as ArtifactDetailDoc } from "@/lib/api"
import { ArtifactDetail } from "@/pages/artifact-detail"

const doc: ArtifactDetailDoc = {
  id: "art-1",
  kind: "doc",
  title: "Design note",
  body: "the full document",
  url: "/doc/art-1",
  links: [],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}

function stubDoc() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, status: 200, json: async () => doc })),
  )
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  window.history.pushState({}, "", "/")
})

// The back-link returns to wherever the document was opened from, carried in the
// ?from= param. This is the fix for a doc opened from an idea that still said
// "<- dashboard".
test("the doc back-link returns to the capture it was opened from", async () => {
  stubDoc()
  window.history.pushState({}, "", "/doc/art-1?from=%2Fidea%2Ft-idea")
  render(<ArtifactDetail id="art-1" kind="doc" />)

  const back = await screen.findByRole("link", { name: /← idea/ })
  expect(back.getAttribute("href")).toBe("/idea/t-idea")
})

test("the doc back-link falls back to the dashboard when opened directly", async () => {
  stubDoc()
  window.history.pushState({}, "", "/doc/art-1")
  render(<ArtifactDetail id="art-1" kind="doc" />)

  const back = await screen.findByRole("link", { name: /← dashboard/ })
  expect(back.getAttribute("href")).toBe("/")
})
