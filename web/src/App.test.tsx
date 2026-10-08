import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import App from "@/App"
import { AppLink } from "@/components/app-link"
import type {
  ArtifactDetail,
  IdeaView,
  Snapshot,
  TaskDetail,
} from "@/lib/api"

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
  in_flight_groups: [],
  waiting_groups: [],
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

const groupedSnapshot: Snapshot = {
  ...emptySnapshot,
  in_flight: [
    {
      id: "t-1",
      title: "Build it",
      class: "waiting",
      chip: "waiting",
      url: "/task/t-1",
      origin: "t-idea",
      origin_title: "Grouped idea",
    },
  ],
  in_flight_groups: [
    {
      idea_id: "t-idea",
      idea_title: "Grouped idea",
      tasks: [
        {
          id: "t-1",
          title: "Build it",
          class: "waiting",
          chip: "waiting",
          url: "/task/t-1",
          origin: "t-idea",
          origin_title: "Grouped idea",
        },
      ],
    },
  ],
}

const ideaDoc: IdeaView = {
  id: "t-idea",
  kind: "idea",
  title: "Spark of a plan",
  description: "the full idea body",
  state: "active",
  chip: "ready-agent",
  url: "/idea/t-idea",
  total: 1,
  done: 0,
  active: 1,
  blocked: 0,
  tasks: [
    {
      id: "t-promoted",
      title: "Promoted task",
      class: "ready-agent",
      chip: "agent next",
      url: "/task/t-promoted",
    },
  ],
  artifacts: [
    {
      id: "art-1",
      kind: "doc",
      title: "Design note",
      url: "/doc/art-1",
    },
  ],
}

const taskDoc: TaskDetail = {
  id: "t-promoted",
  title: "Promoted task",
  class: "ready-agent",
  chip: "agent next",
  url: "/task/t-promoted",
  kind: "task",
  groomed: true,
  acceptance: ["works"],
  deps: [
    {
      id: "t-blocker",
      title: "Blocker task",
      class: "ready-human",
      chip: "human next",
      url: "/task/t-blocker",
    },
  ],
  dependents: [],
  notes: [],
  artifacts: [],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  origin: {
    id: "t-idea",
    title: "Spark of a plan",
    class: "capture",
    chip: "capture",
    url: "/idea/t-idea",
  },
}

const memoryDoc: ArtifactDetail = {
  id: "art-1",
  kind: "memory",
  title: "A memory",
  brief: "when this applies",
  body: "memory body",
  url: "/memory/art-1",
  links: [],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  attached_to: {
    id: "t-promoted",
    title: "Promoted task",
    class: "ready-agent",
    chip: "agent next",
    url: "/task/t-promoted",
  },
}

// The exact payload the old server sent: every empty list marshaled to null.
// Mounting the real component exercises the render path that used to crash.
const nullListsSnapshot = {
  ...emptySnapshot,
  next_agent: null,
  next_human: null,
  in_flight: null,
  waiting: null,
  in_flight_groups: null,
  waiting_groups: null,
  updates: null,
  flags: null,
  ideas: { blocked: null, active: null, finished: null, captured: null },
} as unknown as Snapshot

// stubFetch routes each request by pathname so one mount can fetch a snapshot or
// a detail document, mirroring the server's /api surface.
function stubFetch(routes: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url =
        typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url
      const path = new URL(url, "http://localhost").pathname
      if (path in routes) {
        return { ok: true, status: 200, json: async () => routes[path] }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    }),
  )
}

function renderAt(path: string) {
  window.history.pushState({}, "", path)
  return render(<App />)
}

// FakeEventSource lets jsdom (which has no EventSource) exercise the live path:
// the app subscribes, then a server `update` event triggers a refetch.
class FakeEventSource {
  static instances: FakeEventSource[] = []
  url: string
  private listeners: Record<string, Array<() => void>> = {}

  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }

  addEventListener(type: string, callback: () => void) {
    ;(this.listeners[type] ??= []).push(callback)
  }

  emit(type: string) {
    for (const callback of this.listeners[type] ?? []) callback()
  }

  close() {}
}

afterEach(() => {
  cleanup()
  FakeEventSource.instances = []
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  window.history.pushState({}, "", "/")
})

// AppLink must only client-route the paths the SPA router owns. Anything else
// (/capture, /app/*) has to keep the browser's default navigation, or the router
// renders its not-found page over a server-rendered page.
test("AppLink leaves a route the client router does not own to the browser", () => {
  const push = vi.spyOn(window.history, "pushState")
  render(<AppLink href="/capture">Capture an idea →</AppLink>)
  fireEvent.click(screen.getByText("Capture an idea →"))
  expect(push).not.toHaveBeenCalled()
})

test("AppLink client-routes a route the client router owns", () => {
  const push = vi.spyOn(window.history, "pushState")
  render(<AppLink href="/idea/t-1">Idea</AppLink>)
  fireEvent.click(screen.getByText("Idea"))
  expect(push).toHaveBeenCalledWith(null, "", "/idea/t-1")
})

// Regression: the dashboard's only in-UI capture entry is an AppLink to
// /capture, which the SPA router does not own. Intercepting it landed on the
// not-found page and made capture unreachable by click.
test("the dashboard capture link does not trigger the SPA not-found page", async () => {
  stubFetch({ "/api/snapshot": emptySnapshot })
  renderAt("/")
  const link = await screen.findByText("Capture an idea →")
  fireEvent.click(link)
  expect(screen.queryByText(/Not found/)).toBeNull()
  expect(window.location.pathname).toBe("/")
})

test("renders an empty project without crashing", async () => {
  stubFetch({ "/api/snapshot": emptySnapshot })
  renderAt("/")
  expect(await screen.findByText("Acme dashboard")).toBeTruthy()
  expect(screen.getByText(/Recent updates/)).toBeTruthy()
  expect(screen.getByText(/No events yet/)).toBeTruthy()
})

test("renders a snapshot whose empty lists are null", async () => {
  stubFetch({ "/api/snapshot": nullListsSnapshot })
  renderAt("/")
  expect(await screen.findByText("Acme dashboard")).toBeTruthy()
  expect(screen.getByText(/No events yet/)).toBeTruthy()
})

test("renders tasks and updates from a populated snapshot", async () => {
  stubFetch({ "/api/snapshot": populatedSnapshot })
  renderAt("/")
  expect(await screen.findByText("Ship the app")).toBeTruthy()
  expect(screen.getByText("created task")).toBeTruthy()
})

test("refetches the snapshot when SSE reports an update", async () => {
  let calls = 0
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      calls++
      return { ok: true, status: 200, json: async () => emptySnapshot }
    }),
  )
  vi.stubGlobal("EventSource", FakeEventSource)

  renderAt("/")
  await screen.findByText("Acme dashboard")
  expect(calls).toBe(1)

  FakeEventSource.instances[0].emit("update")
  await waitFor(() => expect(calls).toBe(2))
})

test("groups in-flight tasks under their origin idea", async () => {
  stubFetch({ "/api/snapshot": groupedSnapshot })
  renderAt("/")
  expect(await screen.findByText("Grouped idea")).toBeTruthy()
  expect(screen.getByText("Build it")).toBeTruthy()
})

test("renders an idea detail with promoted tasks and artifacts", async () => {
  stubFetch({ "/api/idea/t-idea": ideaDoc })
  renderAt("/idea/t-idea")
  expect(await screen.findByText("Spark of a plan")).toBeTruthy()
  expect(screen.getByText("the full idea body")).toBeTruthy()
  expect(screen.getByText("Promoted task")).toBeTruthy()
  expect(screen.getByText("Design note")).toBeTruthy()
})

test("renders a task detail with dependencies and origin", async () => {
  stubFetch({ "/api/task/t-promoted": taskDoc })
  renderAt("/task/t-promoted")
  expect(await screen.findByText("Promoted task")).toBeTruthy()
  expect(screen.getByText("Blocker task")).toBeTruthy()
  expect(screen.getByText("Spark of a plan")).toBeTruthy()
})

test("renders a memory detail with its body and attached task", async () => {
  stubFetch({ "/api/memory/art-1": memoryDoc })
  renderAt("/memory/art-1")
  expect(await screen.findByText("A memory")).toBeTruthy()
  expect(screen.getByText("memory body")).toBeTruthy()
  expect(screen.getByText("Promoted task")).toBeTruthy()
})
