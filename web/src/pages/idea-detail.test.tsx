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
