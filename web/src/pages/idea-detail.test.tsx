import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import type { IdeaView } from "@/lib/api"
import { IdeaDetail } from "@/pages/idea-detail"

const idea: IdeaView = {
  id: "t-idea",
  kind: "idea",
  title: "Spark of a plan",
  description: "the full idea body",
  state: "active",
  chip: "capture",
  url: "/idea/t-idea",
  total: 0,
  done: 0,
  todo: 0,
  active: 0,
  blocked: 0,
  tasks: [],
  artifacts: [],
}

function stubClipboard() {
  const writeText = vi.fn(async () => {})
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  })
  return writeText
}

function stubIdea(doc: IdeaView = idea) {
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
      if (path === `/api/idea/${doc.id}`) {
        return { ok: true, status: 200, json: async () => doc }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    }),
  )
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

test("shows the capture id in the metadata table", async () => {
  stubClipboard()
  stubIdea()
  render(<IdeaDetail id="t-idea" />)
  expect(await screen.findByText("t-idea")).toBeTruthy()
})

test("the id control copies the capture id", async () => {
  const writeText = stubClipboard()
  stubIdea()
  render(<IdeaDetail id="t-idea" />)

  fireEvent.click(await screen.findByRole("button", { name: /copy capture id$/i }))

  await waitFor(() => expect(writeText).toHaveBeenCalledWith("t-idea"))
})

// Khoi 2026-10-14: the detail stats line must show the unstarted count too, so
// done + todo + active + blocked sums to the total (a cancelled, a done and an
// unstarted task read "3 · 2 done · 1 todo · 0 active · 0 blocked").
test("the stats line counts unstarted tasks and sums to the total", async () => {
  stubClipboard()
  stubIdea({ ...idea, total: 3, done: 2, todo: 1, active: 0, blocked: 0 })
  render(<IdeaDetail id="t-idea" />)

  expect(await screen.findByText(/3 · 2 done · 1 todo · 0 active · 0 blocked/)).toBeTruthy()
})

// Khoi 2026-10-14: a capture left with a human-next task reads as blocked, its
// stats line counts that work in its own bucket, and the blocking task is named
// with a link - not just a count - so a person knows what to pick up.
test("the idea detail counts human-next work and links the blocking task", async () => {
  stubClipboard()
  stubIdea({
    ...idea,
    state: "blocked",
    chip: "needs-human",
    total: 4,
    done: 3,
    todo: 0,
    active: 0,
    blocked: 0,
    human: 1,
    human_tasks: [
      {
        id: "t-human",
        title: "Decide the rollout",
        class: "needs-human",
        chip: "needs human",
        url: "/task/t-human",
      },
    ],
  })
  render(<IdeaDetail id="t-idea" />)

  expect(
    await screen.findByText(/4 · 3 done · 0 todo · 0 active · 0 blocked · 1 human/),
  ).toBeTruthy()
  const blocker = (await screen.findByText("Decide the rollout")).closest("a")!
  expect(blocker.getAttribute("href")).toBe("/task/t-human")
  expect(screen.getByText("t-human")).toBeTruthy()
})

// The title control copies one payload: the id and the title, "id: title", so a
// pasted reference carries both without opening the page.
test("the title control copies the id and the title together", async () => {
  const writeText = stubClipboard()
  stubIdea()
  render(<IdeaDetail id="t-idea" />)

  fireEvent.click(
    await screen.findByRole("button", { name: /copy capture id and title/i }),
  )

  await waitFor(() =>
    expect(writeText).toHaveBeenCalledWith("t-idea: Spark of a plan"),
  )
})
