import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import { Toaster } from "@/components/toaster"
import type { NotificationsDoc } from "@/lib/api"

class FakeEventSource {
  static instances: FakeEventSource[] = []
  private listeners: Record<string, Array<() => void>> = {}

  constructor() {
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

// stubNotifications routes /api/notifications to the current value of feed, so a
// test can change it and then emit an SSE update to simulate a grooming
// completing while the app is open.
function stubNotifications(feed: () => NotificationsDoc) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url
    const path = new URL(url, "http://localhost").pathname
    if (path === "/api/notifications") {
      return { ok: true, status: 200, json: async () => feed() }
    }
    return { ok: false, status: 404, json: async () => ({}) }
  })
}

// seed renders the toaster and lets its first read resolve, which seeds the
// "seen" set so only later completions toast.
async function seed() {
  vi.stubGlobal("EventSource", FakeEventSource)
  render(<Toaster />)
  await act(async () => {})
}

afterEach(() => {
  cleanup()
  FakeEventSource.instances = []
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  window.localStorage.clear()
  window.history.pushState({}, "", "/")
})

test("toasts a grooming that completes live and opens the prompt artifact page", async () => {
  let feed: NotificationsDoc = { notifications: [] }
  vi.stubGlobal("fetch", stubNotifications(() => feed))
  await seed()
  expect(screen.queryByRole("status")).toBeNull()

  feed = {
    notifications: [
      { id: "art-1", title: "Rework onboarding", url: "/doc/art-1", captures: 2 },
    ],
  }
  await act(async () => {
    for (const source of FakeEventSource.instances) source.emit("update")
  })

  const link = await screen.findByRole("link", { name: /Rework onboarding/ })
  expect(screen.getByText("Grooming complete")).toBeTruthy()
  expect(screen.getByText("2 captures →")).toBeTruthy()

  fireEvent.click(link)
  await waitFor(() => expect(window.location.pathname).toBe("/doc/art-1"))
  expect(screen.queryByRole("status")).toBeNull()
})

test("a single capture reads as one, not a branch on the count", async () => {
  let feed: NotificationsDoc = { notifications: [] }
  vi.stubGlobal("fetch", stubNotifications(() => feed))
  await seed()

  feed = {
    notifications: [
      { id: "art-1", title: "Rework onboarding", url: "/doc/art-1", captures: 1 },
    ],
  }
  await act(async () => {
    for (const source of FakeEventSource.instances) source.emit("update")
  })

  expect(await screen.findByText("1 capture →")).toBeTruthy()
})

test("seeds already-complete groomings on first load without replaying them", async () => {
  const feed: NotificationsDoc = {
    notifications: [
      { id: "art-1", title: "Rework onboarding", url: "/doc/art-1", captures: 1 },
    ],
  }
  vi.stubGlobal("fetch", stubNotifications(() => feed))
  await seed()

  expect(screen.queryByRole("status")).toBeNull()
})

test("a seen grooming is not toasted again after it is shown", async () => {
  let feed: NotificationsDoc = { notifications: [] }
  vi.stubGlobal("fetch", stubNotifications(() => feed))
  await seed()

  feed = {
    notifications: [
      { id: "art-1", title: "Rework onboarding", url: "/doc/art-1", captures: 1 },
    ],
  }
  await act(async () => {
    for (const source of FakeEventSource.instances) source.emit("update")
  })
  expect(await screen.findByRole("status")).toBeTruthy()

  fireEvent.click(screen.getByRole("button", { name: /dismiss/i }))
  expect(screen.queryByRole("status")).toBeNull()

  await act(async () => {
    for (const source of FakeEventSource.instances) source.emit("update")
  })
  expect(screen.queryByRole("status")).toBeNull()
})
