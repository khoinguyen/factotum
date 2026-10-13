import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import App from "@/App"
import { AppLink } from "@/components/app-link"
import type {
  ArtifactDetail,
  IdeaView,
  Snapshot,
  TaskDetail,
  SessionsDoc,
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
  pipelines: [],
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

// A factory view: one pipeline parked on a human gate with its clearing command
// and a produced task, and one ungated pipeline still advancing.
const factorySnapshot: Snapshot = {
  ...emptySnapshot,
  pipelines: [
    {
      id: "pl-1",
      capture: "t-idea",
      capture_title: "Spark of a plan",
      capture_url: "/idea/t-idea",
      state: "building",
      gate: "merge_approval",
      command: "ft up merge pl-1",
      produced: [
        {
          id: "t-promoted",
          title: "Promoted task",
          class: "review",
          chip: "review",
          url: "/task/t-promoted",
        },
      ],
    },
    {
      id: "pl-2",
      capture: "t-idea-2",
      capture_title: "Second spark",
      capture_url: "/idea/t-idea-2",
      state: "grooming",
      produced: [],
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
  todo: 0,
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
  grouped_under: [
    {
      id: "t-umbrella",
      title: "Umbrella idea",
      class: "capture",
      chip: "capture",
      url: "/idea/t-umbrella",
    },
  ],
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

// A doc artifact attached to the idea capture, the shape /api/doc returns for a
// QA verdict or design note opened from a capture.
const artifactDoc: ArtifactDetail = {
  id: "art-1",
  kind: "doc",
  title: "Design note",
  body: "the full document",
  url: "/doc/art-1",
  links: [],
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  attached_to: {
    id: "t-idea",
    title: "Spark of a plan",
    class: "capture",
    chip: "capture",
    url: "/idea/t-idea",
  },
}

// One retired session (with delivered work) and one alive successor, the shape
// /api/sessions returns.
const sessionsDoc: SessionsDoc = {
  sessions: [
    {
      id: "s-new",
      state: "alive",
      started_at: "2026-01-01T11:00:00Z",
      duration_seconds: 3600,
      delivered: {
        tasks_done: 0,
        prs_merged: 0,
        captures_advanced: 0,
        tasks: [],
        prs: [],
      },
    },
    {
      id: "s-old",
      state: "retired",
      started_at: "2026-01-01T09:00:00Z",
      stopped_at: "2026-01-01T11:00:00Z",
      duration_seconds: 7200,
      reason: "retired for s-new",
      successor: "s-new",
      delivered: {
        tasks_done: 1,
        prs_merged: 1,
        captures_advanced: 1,
        tasks: [{ id: "t-1", title: "Ship it", url: "/task/t-1" }],
        prs: [{ url: "https://example.com/pr/1", title: "PR one" }],
      },
    },
  ],
}

// The exact payload the old server sent: every empty list marshaled to null.
// Mounting the real component exercises the render path that used to crash.
const nullListsSnapshot = {  ...emptySnapshot,
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

// stubFailure makes one route fail with a JSON error at the given status,
// mirroring the server's read path, which returns 503 application/json (for
// example on SQLITE_BUSY) rather than a document.
function stubFailure(route: string, status: number, message: string) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url =
        typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url
      const path = new URL(url, "http://localhost").pathname
      if (path === route) {
        return { ok: false, status, json: async () => ({ error: message }) }
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
  window.localStorage.clear()
  window.history.pushState({}, "", "/")
})

// AppLink must only client-route the paths the SPA router owns. Anything else
// (/app/*, external URLs) has to keep the browser's default navigation, or the
// router renders its not-found page over a server-rendered page.
test("AppLink leaves a route the client router does not own to the browser", () => {
  const push = vi.spyOn(window.history, "pushState")
  render(<AppLink href="/app/">App asset</AppLink>)
  fireEvent.click(screen.getByText("App asset"))
  expect(push).not.toHaveBeenCalled()
})

test("AppLink client-routes a route the client router owns", () => {
  const push = vi.spyOn(window.history, "pushState")
  render(<AppLink href="/idea/t-1">Idea</AppLink>)
  fireEvent.click(screen.getByText("Idea"))
  expect(push).toHaveBeenCalledWith(null, "", "/idea/t-1")
})

// Regression: the dashboard's capture entry must open the capture modal in the
// SPA (the token-gated prompt box), not navigate to the /capture page.
test("the dashboard capture entry opens the capture modal", async () => {
  stubFetch({
    "/api/snapshot": emptySnapshot,
    "/api/capture": { enabled: true, project: "acme" },
  })
  renderAt("/")
  const entry = await screen.findByRole("button", { name: /capture an idea or bug/i })
  fireEvent.click(entry)
  expect(await screen.findByRole("dialog")).toBeTruthy()
  expect(window.location.pathname).toBe("/")
})

// Khoi 2026-10-09: after capturing an idea the app lands on a drill-down, which
// used to hide the capture entry, so a user could not capture again without
// navigating back. Every drill-down must offer it too.
const drillDownCapture = [
  { path: "/idea/t-idea", route: "/api/idea/t-idea", doc: ideaDoc },
  { path: "/task/t-promoted", route: "/api/task/t-promoted", doc: taskDoc },
  { path: "/memory/art-1", route: "/api/memory/art-1", doc: memoryDoc },
  {
    path: "/doc/art-1",
    route: "/api/doc/art-1",
    doc: { ...memoryDoc, id: "art-1", kind: "doc", url: "/doc/art-1" },
  },
]

for (const { path, route, doc } of drillDownCapture) {
  test(`offers capture from the drill-down at ${path}`, async () => {
    stubFetch({ [route]: doc, "/api/capture": { enabled: true, project: "acme" } })
    renderAt(path)
    const entry = await screen.findByRole("button", { name: /capture an idea or bug/i })
    fireEvent.click(entry)
    expect(await screen.findByRole("dialog")).toBeTruthy()
    expect(window.location.pathname).toBe(path)
  })
}

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

// Khoi 2026-10-09: the board section holds ideas and bugs, so its label is
// "Captures", not "Ideas".
test("the captures board section is titled Captures", async () => {
  stubFetch({ "/api/snapshot": emptySnapshot })
  renderAt("/")
  expect(await screen.findByRole("heading", { name: "Captures" })).toBeTruthy()
  expect(screen.queryByRole("heading", { name: "Ideas" })).toBeNull()
})

// Khoi 2026-10-09: the top stats row counts ideas and bugs together, so its
// label is "Captures", not "Ideas".
test("the top stats row labels the ideas+bugs count Captures", async () => {
  stubFetch({
    "/api/snapshot": {
      ...emptySnapshot,
      stats: { ...emptySnapshot.stats, ideas: 7 },
    },
  })
  renderAt("/")
  expect(await screen.findByText("Acme dashboard")).toBeTruthy()
  const value = screen.getByText("7")
  expect(within(value.parentElement!).getByText("Captures")).toBeTruthy()
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
    vi.fn(async (input: RequestInfo | URL) => {
      const url =
        typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url
      const path = new URL(url, "http://localhost").pathname
      if (path !== "/api/snapshot") {
        return { ok: false, status: 404, json: async () => ({}) }
      }
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

// Regression: a refresh that fails (for example SQLITE_BUSY under concurrent
// writes) must keep the last good snapshot and the shell, and surface a banner —
// never replace the page with the raw error.
test("keeps the shell and shows a stale banner when a refresh fails", async () => {
  let calls = 0
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      calls++
      if (calls === 1) {
        return { ok: true, status: 200, json: async () => emptySnapshot }
      }
      return {
        ok: false,
        status: 503,
        json: async () => ({
          error: "list actors: database is locked (5) (SQLITE_BUSY)",
        }),
      }
    }),
  )
  vi.stubGlobal("EventSource", FakeEventSource)

  renderAt("/")
  await screen.findByText("Acme dashboard")

  FakeEventSource.instances[0].emit("update")

  const banner = await screen.findByRole("alert")
  expect(banner.textContent).toMatch(/database is locked/)
  expect(screen.getByText("Acme dashboard")).toBeTruthy()
})

// When the very first fetch fails there is nothing stale to show, but the shell
// and the error banner must still be the page — not the raw error text.
test("renders the shell with an error banner when the first load fails", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: false,
      status: 503,
      json: async () => ({ error: "boom: database is locked" }),
    })),
  )

  renderAt("/")
  const banner = await screen.findByRole("alert")
  expect(banner.textContent).toMatch(/database is locked/)
  expect(screen.getByText("Dashboard")).toBeTruthy()
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
  expect(screen.getByText("Umbrella idea")).toBeTruthy()
  expect(screen.getByText("Grouped under · 1")).toBeTruthy()
})

// Khoi 2026-10-12: a doc opened from a capture must offer a back-link to that
// capture, not the dashboard. The capture detail carries where it was opened
// from in ?from=, and the doc page reads it back, so the round trip lands on the
// capture again.
test("the doc back-link returns to the capture it was opened from", async () => {
  stubFetch({ "/api/idea/t-idea": ideaDoc, "/api/doc/art-1": artifactDoc })
  renderAt("/idea/t-idea")

  const artifact = await screen.findByText("Design note")
  expect(artifact.closest("a")?.getAttribute("href")).toBe(
    "/doc/art-1?from=%2Fidea%2Ft-idea",
  )

  fireEvent.click(artifact)

  const back = await screen.findByRole("link", { name: /← idea/ })
  expect(back.getAttribute("href")).toBe("/idea/t-idea")

  fireEvent.click(back)
  expect(window.location.pathname).toBe("/idea/t-idea")
  expect(await screen.findByText("Spark of a plan")).toBeTruthy()
})

// The task detail carries itself as the return target too, so an artifact opened
// from a task returns to the task.
test("a task detail's artifact link carries the task as its return target", async () => {
  stubFetch({
    "/api/task/t-promoted": {
      ...taskDoc,
      artifacts: [
        { id: "art-2", kind: "doc", title: "Spec note", url: "/doc/art-2" },
      ],
    },
  })
  renderAt("/task/t-promoted")

  const artifact = await screen.findByText("Spec note")
  expect(artifact.closest("a")?.getAttribute("href")).toBe(
    "/doc/art-2?from=%2Ftask%2Ft-promoted",
  )
})

test("renders a memory detail with its body and attached task", async () => {
  stubFetch({ "/api/memory/art-1": memoryDoc })
  renderAt("/memory/art-1")
  expect(await screen.findByText("A memory")).toBeTruthy()
  expect(screen.getByText("memory body")).toBeTruthy()
  expect(screen.getByText("Promoted task")).toBeTruthy()
})

// Khoi 2026-10-12: a QA verdict artifact is linked at /doc/<id>, so the doc
// viewer must render it like any other document rather than showing a missing
// page.
test("renders a QA verdict artifact on the doc route", async () => {
  stubFetch({
    "/api/doc/art-qa": {
      ...memoryDoc,
      id: "art-qa",
      kind: "qa",
      title: "QA pass t-idea",
      brief: "pass",
      body: "all checks good",
      url: "/doc/art-qa",
    },
  })
  renderAt("/doc/art-qa")
  expect(await screen.findByText("QA pass t-idea")).toBeTruthy()
  expect(screen.getByText("all checks good")).toBeTruthy()
})

// Khoi 2026-10-12: the live badge must reflect the SSE stream, not the document
// read. A failed read used to flip the badge to "reconnecting" even while the
// stream was up, which is what a QA artifact page showed.
test("the live badge tracks the SSE stream, not the document fetch", async () => {
  stubFailure("/api/doc/art-1", 503, "boom")
  vi.stubGlobal("EventSource", FakeEventSource)

  renderAt("/doc/art-1")
  const source = FakeEventSource.instances[0]

  source.emit("open")
  expect(await screen.findByText("live")).toBeTruthy()

  source.emit("error")
  expect(await screen.findByText("reconnecting…")).toBeTruthy()
})

// A drill-down whose read fails with a store error (503 application/json) must
// show the failure. Before this, the page ignored the error and sat on
// "Loading …" forever because only 404 was handled. subject is the noun the
// banner names: the artifact detail maps its kind to "memory" vs "document", so
// the memory and doc drill-downs assert that mapping here.
const drillDownFailures = [
  {
    path: "/task/t-promoted",
    route: "/api/task/t-promoted",
    title: "Task",
    subject: "task",
  },
  { path: "/idea/t-idea", route: "/api/idea/t-idea", title: "Idea", subject: "idea" },
  {
    path: "/memory/art-1",
    route: "/api/memory/art-1",
    title: "Memory",
    subject: "memory",
  },
  { path: "/doc/art-1", route: "/api/doc/art-1", title: "Document", subject: "document" },
]

for (const { path, route, title, subject } of drillDownFailures) {
  test(`surfaces a store error on the ${title} drill-down instead of endless loading`, async () => {
    stubFailure(route, 503, "list actors: database is locked (5) (SQLITE_BUSY)")
    renderAt(path)
    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toMatch(/database is locked/)
    expect(alert.textContent).toMatch(`Couldn't load the ${subject}.`)
    expect(screen.queryByText(/Loading/)).toBeNull()
  })
}

// A missing document is still a 404 message, not the store-error banner: the
// error branch must not swallow the not-found case. Every drill-down shares
// that 404-over-error precedence, so each is asserted individually.
const drillDownNotFounds = [
  { path: "/task/t-missing", route: "/api/task/t-missing", message: "No such task." },
  { path: "/idea/t-missing", route: "/api/idea/t-missing", message: "No such idea." },
  {
    path: "/memory/art-missing",
    route: "/api/memory/art-missing",
    message: "No such memory.",
  },
  { path: "/doc/art-missing", route: "/api/doc/art-missing", message: "No such doc." },
]

for (const { path, route, message } of drillDownNotFounds) {
  test(`keeps the not-found message for a missing drill-down at ${path}`, async () => {
    stubFailure(route, 404, "not found")
    renderAt(path)
    expect(await screen.findByText(message)).toBeTruthy()
    expect(screen.queryByRole("alert")).toBeNull()
  })
}

// Same contract as the dashboard: a drill-down that loaded keeps the last good
// document when a later refresh fails, and shows the failure.
test("keeps the last good task and shows a stale banner when a refresh fails", async () => {
  let calls = 0
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url =
        typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url
      const path = new URL(url, "http://localhost").pathname
      if (path !== "/api/task/t-promoted") {
        return { ok: false, status: 404, json: async () => ({}) }
      }
      calls++
      if (calls === 1) {
        return { ok: true, status: 200, json: async () => taskDoc }
      }
      return {
        ok: false,
        status: 503,
        json: async () => ({ error: "database is locked (5) (SQLITE_BUSY)" }),
      }
    }),
  )
  vi.stubGlobal("EventSource", FakeEventSource)

  renderAt("/task/t-promoted")
  await screen.findByText("Promoted task")

  FakeEventSource.instances[0].emit("update")

  const banner = await screen.findByRole("alert")
  expect(banner.textContent).toMatch(/database is locked/)
  expect(screen.getByText("Promoted task")).toBeTruthy()
})

// Khoi 2026-10-09: the sessions page shows each chief session and how much work
// it delivered. It must render both an alive and a retired session, the stop
// reason, and the delivered tasks and pull requests.
test("renders the sessions page with alive and retired sessions", async () => {
  stubFetch({ "/api/sessions": sessionsDoc })
  renderAt("/sessions")
  expect(await screen.findByText("s-old")).toBeTruthy()
  expect(screen.getByText("s-new")).toBeTruthy()
  expect(screen.getByText(/retired for s-new/)).toBeTruthy()
  expect(screen.getByText("2h 0m")).toBeTruthy()
  expect(screen.getByText("1h 0m")).toBeTruthy()
  expect(screen.getByText("Ship it")).toBeTruthy()
  expect(screen.getByText("PR one")).toBeTruthy()
})

test("renders an empty sessions page", async () => {
  stubFetch({ "/api/sessions": { sessions: [] } })
  renderAt("/sessions")
  expect(await screen.findByText("No sessions recorded yet.")).toBeTruthy()
})

// The factory view surfaces each pipeline with its origin capture, phase, open
// gate, produced tasks, and the exact operator command that clears the gate
// (docs/up/design.md §6/§7).
test("renders the factory page with each pipeline's phase, gate, and clearing command", async () => {
  stubFetch({ "/api/snapshot": factorySnapshot })
  renderAt("/factory")
  expect(await screen.findByText("Spark of a plan")).toBeTruthy()
  expect(screen.getByText("building")).toBeTruthy()
  expect(screen.getByText("merge_approval")).toBeTruthy()
  expect(screen.getByText("ft up merge pl-1")).toBeTruthy()
  expect(screen.getByText("Promoted task")).toBeTruthy()
})

// A pipeline still advancing has no open gate, so it shows its phase but no
// clearing command.
test("the factory page shows no clearing command for an ungated pipeline", async () => {
  stubFetch({ "/api/snapshot": factorySnapshot })
  renderAt("/factory")
  expect(await screen.findByText("Second spark")).toBeTruthy()
  expect(screen.getByText("grooming")).toBeTruthy()
  expect(screen.getAllByText("Clear the gate")).toHaveLength(1)
})

test("renders an empty factory page", async () => {
  stubFetch({ "/api/snapshot": emptySnapshot })
  renderAt("/factory")
  expect(await screen.findByText("No pipelines yet.")).toBeTruthy()
})

test("the dashboard links to the factory page", async () => {
  stubFetch({ "/api/snapshot": emptySnapshot })
  renderAt("/")
  const link = await screen.findByText("Factory →")
  fireEvent.click(link)
  expect(await screen.findByText("No pipelines yet.")).toBeTruthy()
  expect(window.location.pathname).toBe("/factory")
})

test("the dashboard links to the sessions page", async () => {
  stubFetch({ "/api/snapshot": emptySnapshot, "/api/sessions": sessionsDoc })
  renderAt("/")
  const link = await screen.findByText("Chief sessions →")
  fireEvent.click(link)
  expect(await screen.findByText("s-old")).toBeTruthy()
  expect(window.location.pathname).toBe("/sessions")
})

