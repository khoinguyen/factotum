import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import App from "@/App"
import type { Snapshot } from "@/lib/api"

const emptySnapshot: Snapshot = {
  title: "Acme dashboard",
  project: "acme",
  snapshot: "2026-01-01T00:00:00Z",
  stats: {
    scope: 0,
    ideas: 0,
    done: 0,
    ready_agent: 0,
    ready_human: 0,
    blocked: 0,
    cycles: 0,
    waves: 0,
  },
  next_agent: [],
  next_human: [],
  in_flight: [],
  waiting: [],
  ideas: { blocked: [], active: [], finished: [], captured: [] },
  updates: [],
  flags: [],
}

const populatedSnapshot: Snapshot = {
  ...emptySnapshot,
  stats: { ...emptySnapshot.stats, scope: 1, ready_human: 1, waves: 1 },
  next_human: [
    {
      id: "t-1",
      title: "Ship the app",
      class: "ready-human",
      chip: "human next",
      url: "/task/t-1",
    },
  ],
  updates: [{ time: "2026-01-01T00:00:00Z", summary: "created task" }],
}

// The exact payload the old server sent: every empty list marshaled to null.
// Mounting the real component exercises the render path that used to crash.
const nullListsSnapshot = {
  ...emptySnapshot,
  next_agent: null,
  next_human: null,
  in_flight: null,
  waiting: null,
  updates: null,
  flags: null,
  ideas: { blocked: null, active: null, finished: null, captured: null },
} as unknown as Snapshot

function stubSnapshot(body: Snapshot) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, status: 200, json: async () => body })),
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

test("renders an empty project without crashing", async () => {
  stubSnapshot(emptySnapshot)
  render(<App />)
  expect(await screen.findByText("Acme dashboard")).toBeTruthy()
  expect(screen.getByText(/Recent updates/)).toBeTruthy()
  expect(screen.getByText(/No events yet/)).toBeTruthy()
})

test("renders a snapshot whose empty lists are null", async () => {
  stubSnapshot(nullListsSnapshot)
  render(<App />)
  expect(await screen.findByText("Acme dashboard")).toBeTruthy()
  expect(screen.getByText(/No events yet/)).toBeTruthy()
})

test("renders tasks and updates from a populated snapshot", async () => {
  stubSnapshot(populatedSnapshot)
  render(<App />)
  expect(await screen.findByText("Ship the app")).toBeTruthy()
  expect(screen.getByText("created task")).toBeTruthy()
})
