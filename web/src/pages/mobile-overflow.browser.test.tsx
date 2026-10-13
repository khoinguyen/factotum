import { cleanup, render, screen } from "@testing-library/react"
import { page } from "vitest/browser"
import { afterEach, expect, test, vi } from "vitest"

import type * as React from "react"

import "@/index.css"

import type {
  ArtifactDetail,
  CaptureConfig,
  IdeaView,
  SessionsDoc,
  Snapshot,
  TaskDetail,
  TaskView,
} from "@/lib/api"
import { Dashboard } from "@/pages/dashboard"
import { IdeaDetail } from "@/pages/idea-detail"
import { TaskDetail as TaskDetailPage } from "@/pages/task-detail"
import { ArtifactDetail as ArtifactDetailPage } from "@/pages/artifact-detail"
import { Factory } from "@/pages/factory"
import { Sessions } from "@/pages/sessions"
import { Capture } from "@/pages/capture"
import App from "@/App"

// Every route must fit a phone. jsdom cannot lay text out, so the only way to
// prove no route scrolls horizontally is to render it at phone widths in a real
// browser and measure the document. The data is deliberately adversarial: long
// mono ids, long titles, extra row columns (repo, assignee, reason/detail), and
// unbreakable URL tokens, i.e. the shapes that blow past a 360px viewport.

// An unbreakable URL token: min-content wider than the viewport is the classic
// trigger for horizontal overflow.
const LONG_URL =
  "https://github.com/khoinguyen/factotum/pull/312/files#diff-a-longunbreakablefragment"

const readyTask: TaskView = {
  id: "t-h3ksy6ytay",
  title:
    "Web: eliminate horizontal overflow at mobile widths on ALL pages (kanban board, cards, lists, detail, modals)",
  class: "ready-agent",
  chip: "ready",
  url: "/task/t-h3ksy6ytay",
  repo: "github:khoinguyen/factotum",
  assignee: "builder-t-h3ksy6ytay",
  wave: 3,
  unblocks: 12,
  origin: "t-bqqkrvzaqm",
  origin_title: "Web app: mobile responsiveness across all pages",
}

const blockedTask: TaskView = {
  id: "t-wprtscb2qt",
  title: "Dashboard task-list cards overflow horizontally on mobile (populated board)",
  class: "blocked",
  chip: "blocked",
  url: "/task/t-wprtscb2qt",
  milestone: true,
  reason: "waiting for t-as5zefdfs7, t-a5t6zdzbnz, t-bqqkrvzaqm",
  detail: "3 dependencies still open",
}

const idea: IdeaView = {
  id: "t-bqqkrvzaqm",
  kind: "idea",
  title: "Web app: mobile responsiveness across all pages",
  description:
    "Verify every web route at phone widths and fix any horizontal overflow.",
  repo: "github:khoinguyen/factotum",
  state: "active",
  chip: "capture",
  url: "/idea/t-bqqkrvzaqm",
  total: 3,
  done: 1,
  todo: 0,
  active: 1,
  blocked: 1,
  tasks: [readyTask, blockedTask],
  artifacts: [
    {
      id: "art-2wh3x6pijk",
      kind: "memory",
      title: "Mobile overflow checklist: every route measured at 360/390",
      url: "/memory/art-2wh3x6pijk",
    },
    { id: "doc-2wh3x6pijk", kind: "doc", title: "QA sweep", url: "/doc/doc-2wh3x6pijk" },
  ],
}

function snapshot(): Snapshot {
  return {
    title: "Factotum dashboard",
    project: "factotum",
    snapshot: "2026-10-12T17:06:41Z",
    stats: {
      scope: 42,
      ideas: 7,
      done: 108,
      ready_agent: 3,
      ready_human: 2,
      blocked: 5,
      cycles: 0,
      waves: 6,
    },
    next_agent: [readyTask, blockedTask],
    next_human: [readyTask],
    in_flight: [readyTask],
    waiting: [blockedTask],
    in_flight_groups: [
      {
        idea_id: "t-bqqkrvzaqm",
        idea_title: "Web app: mobile responsiveness across all pages",
        idea_state: "active",
        tasks: [readyTask],
      },
    ],
    waiting_groups: [{ ungrouped: true, tasks: [blockedTask] }],
    ideas: { captured: [idea], blocked: [], active: [], finished: [] },
    pipelines: [
      {
        id: "pipe-t-bqqkrvzaqm",
        capture: "t-bqqkrvzaqm",
        capture_title: "Web app: mobile responsiveness across all pages",
        capture_url: "/idea/t-bqqkrvzaqm",
        state: "active",
        gate: "human-approval",
        command: "ft gate approve pipe-t-bqqkrvzaqm",
        produced: [readyTask, blockedTask],
        updated_at: "2026-10-12T17:06:41Z",
        error: "the runner could not reach the remote",
      },
    ],
    updates: [
      {
        time: "2026-10-12T17:06:41Z",
        summary: "builder-t-h3ksy6ytay added task t-h3ksy6ytay: Investigate flaky run " + LONG_URL,
      },
    ],
    flags: ["cycle detected: t-a -> t-b -> t-a"],
  }
}

function taskDetail(): TaskDetail {
  return {
    id: "t-h3ksy6ytay",
    kind: "task",
    title:
      "Web: eliminate horizontal overflow at mobile widths on ALL pages (kanban board, cards, lists, detail, modals)",
    class: "ready-agent",
    chip: "ready",
    url: "/task/t-h3ksy6ytay",
    repo: "github:khoinguyen/factotum",
    assignee: "builder-t-h3ksy6ytay",
    groomed: true,
    description: "Long description that must wrap \u2014 with a " + LONG_URL,
    acceptance: [
      "At phone widths no web route scrolls horizontally.",
      "Verified in headless Chromium at 360/390px against a served dashboard.",
    ],
    deps: [blockedTask],
    dependents: [readyTask],
    grouped_under: [
      {
        id: "t-bqqkrvzaqm",
        title: "Web app: mobile responsiveness across all pages",
        class: "capture",
        chip: "capture",
        url: "/idea/t-bqqkrvzaqm",
      },
    ],
    notes: [
      {
        author: "khoi",
        body: "Also affects the KANBAN/work board. " + LONG_URL,
        created_at: "2026-10-12T17:16:54Z",
        links: [{ kind: "issue", url: LONG_URL, title: "overflow issue" }],
      },
    ],
    artifacts: [
      {
        id: "art-2wh3x6pijk",
        kind: "memory",
        title: "Mobile overflow checklist",
        url: "/memory/art-2wh3x6pijk",
      },
    ],
    created_at: "2026-10-12T17:06:41Z",
    updated_at: "2026-10-12T17:16:54Z",
    origin: {
      id: "t-bqqkrvzaqm",
      title: "Web app: mobile responsiveness across all pages",
      class: "capture",
      chip: "capture",
      url: "/idea/t-bqqkrvzaqm",
    },
  }
}

function artifact(): ArtifactDetail {
  return {
    id: "doc-2wh3x6pijk",
    kind: "doc",
    title: "Mobile overflow QA sweep",
    brief: "when verifying web routes at phone widths",
    body: "Every route was measured at 360 and 390px. " + LONG_URL,
    url: "/doc/doc-2wh3x6pijk",
    links: [{ kind: "issue", url: LONG_URL, title: "mobile overflow" }],
    created_at: "2026-10-12T17:06:41Z",
    updated_at: "2026-10-12T17:16:54Z",
    attached_to: {
      id: "t-h3ksy6ytay",
      title: "Web: eliminate horizontal overflow",
      class: "ready-agent",
      chip: "ready",
      url: "/task/t-h3ksy6ytay",
    },
  }
}

function sessionsDoc(): SessionsDoc {
  return {
    sessions: [
      {
        id: "chief-2026-10-12",
        state: "alive",
        started_at: "2026-10-12T09:00:00Z",
        duration_seconds: 3661,
        delivered: {
          tasks_done: 4,
          prs_merged: 2,
          captures_advanced: 3,
          tasks: [readyTask],
          prs: [{ url: LONG_URL, title: "Web: eliminate horizontal overflow" }],
        },
      },
    ],
  }
}

function routeJSON(path: string): unknown {
  if (path === "/api/snapshot") return snapshot()
  if (path === "/api/sessions") return sessionsDoc()
  if (path === "/api/capture") return { enabled: true, project: "factotum" } as CaptureConfig
  if (path.startsWith("/api/idea/")) return idea
  if (path.startsWith("/api/task/")) return taskDetail()
  if (path.startsWith("/api/memory/") || path.startsWith("/api/doc/")) return artifact()
  return {}
}

function stubRoutes() {
  vi.stubGlobal("EventSource", undefined)
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url
      const path = new URL(url, "http://localhost").pathname
      return { ok: true, status: 200, json: async () => routeJSON(path) }
    }),
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

type Route = {
  name: string
  heading: RegExp
  // ready is content that only renders once the page's live resource has
  // loaded, so the measurement happens on the populated page and not the
  // loading shell (which shares the same title).
  ready: RegExp
  before?: () => void
  render: () => React.ReactElement
}

const routes: Route[] = [
  { name: "dashboard", heading: /Factotum dashboard/, ready: /Work board/, render: () => <Dashboard /> },
  { name: "idea detail", heading: /^Web app: mobile responsiveness/, ready: /Promoted tasks/, render: () => <IdeaDetail id="t-bqqkrvzaqm" /> },
  { name: "task detail", heading: /^Web: eliminate horizontal overflow/, ready: /Acceptance criteria/, render: () => <TaskDetailPage id="t-h3ksy6ytay" /> },
  { name: "artifact detail", heading: /^Mobile overflow QA sweep$/, ready: /^Links$/, render: () => <ArtifactDetailPage id="doc-2wh3x6pijk" kind="doc" /> },
  { name: "memory detail", heading: /^Mobile overflow QA sweep$/, ready: /^Links$/, render: () => <ArtifactDetailPage id="doc-2wh3x6pijk" kind="memory" /> },
  { name: "factory", heading: /^Factory$/, ready: /Clear the gate/, render: () => <Factory /> },
  { name: "sessions", heading: /^Sessions$/, ready: /chief-2026-10-12/, render: () => <Sessions /> },
  { name: "capture", heading: /^Capture$/, ready: /What is it\?/, render: () => <Capture /> },
  {
    name: "not found",
    heading: /^Not found$/,
    ready: /No page at/,
    before: () => window.history.pushState(null, "", "/no-such-route"),
    render: () => <App />,
  },
]

const PHONE_WIDTHS = [360, 390]

for (const route of routes) {
  for (const width of PHONE_WIDTHS) {
    test(`${route.name} fits ${width}px`, async () => {
      stubRoutes()
      route.before?.()
      await page.viewport(width, 900)
      render(route.render())
      await screen.findByRole("heading", { name: route.heading })
      await screen.findByText(route.ready)

      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    })
  }
}

// 320px is tighter than the supported phone widths (360/390), but the hidden
// counts tooltip must never contribute to layout: it is absolutely positioned
// with whitespace-nowrap, so at a narrow viewport an only-opacity-0 tooltip
// still widens document.scrollWidth. A 320px dashboard with a long tooltip must
// stay flush.
test("the dashboard fits 320px with a long counts tooltip", async () => {
  stubRoutes()
  await page.viewport(320, 900)
  render(<Dashboard />)
  await screen.findByRole("heading", { name: /Factotum dashboard/ })
  await screen.findByText(/Work board/)

  // Guard the premise: the card really does carry a long counts tooltip, so a
  // future fixture trim can't quietly turn this into a no-op.
  const tooltip = document.querySelector('[role="tooltip"]')
  expect(tooltip?.textContent?.length ?? 0).toBeGreaterThan(30)

  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(320)
})

// Desktop must not regress: the widest route stays within a wide viewport.
test("the dashboard still fits a desktop viewport", async () => {
  stubRoutes()
  await page.viewport(1280, 900)
  render(<Dashboard />)
  await screen.findByRole("heading", { name: /Factotum dashboard/ })

  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1280)
})
