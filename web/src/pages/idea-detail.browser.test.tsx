import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { userEvent } from "vitest/browser"
import { afterEach, expect, test, vi } from "vitest"

import "@/index.css"

import type { IdeaView } from "@/lib/api"
import { IdeaDetail } from "@/pages/idea-detail"

// The browser config grants clipboard-read/clipboard-write, so this suite proves
// the controls put the real text on the real clipboard in headless Chromium.
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

function stubIdea() {
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
      if (path === `/api/idea/${idea.id}`) {
        return { ok: true, status: 200, json: async () => idea }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    }),
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

async function readClipboard(): Promise<string> {
  return navigator.clipboard.readText()
}

test("the id control copies the capture id", async () => {
  stubIdea()
  vi.stubGlobal("EventSource", undefined)
  render(<IdeaDetail id="t-idea" />)

  await userEvent.click(await screen.findByRole("button", { name: /copy capture id$/i }))

  await waitFor(async () => expect(await readClipboard()).toBe("t-idea"))
})

test("the title control copies the id and the title together", async () => {
  stubIdea()
  vi.stubGlobal("EventSource", undefined)
  render(<IdeaDetail id="t-idea" />)

  await userEvent.click(
    await screen.findByRole("button", { name: /copy capture id and title/i }),
  )

  await waitFor(async () =>
    expect(await readClipboard()).toBe("t-idea: Spark of a plan"),
  )
})
