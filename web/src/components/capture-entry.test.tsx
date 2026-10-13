import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import { CaptureEntry } from "@/components/capture-entry"

// stubCapture stands in for the serve endpoints the modal talks to: the
// /api/capture read reports whether a candidate token is valid, and the
// /api/prompt write stores a prompt only for the right token. It models the
// server's rule that a wrong token is a 401 and nothing is stored.
function stubCapture(
  opts: { validToken?: string; promptStatus?: number } = {},
) {
  const validToken = opts.validToken ?? "s3cret"
  const writes: Array<{ token: string | null; body: unknown }> = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url =
        typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url
      const path = new URL(url, "http://localhost").pathname
      const method = (init?.method ?? "GET").toUpperCase()
      const auth = new Headers(init?.headers).get("Authorization")
      const offered = auth?.replace(/^Bearer /, "")
      if (path === "/api/capture" && method === "GET") {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            enabled: true,
            project: "acme",
            authorized: offered === validToken,
          }),
        }
      }
      if (path === "/api/prompt" && method === "POST") {
        writes.push({ token: auth, body: init?.body ? JSON.parse(String(init.body)) : null })
        const status = opts.promptStatus ?? (auth === `Bearer ${validToken}` ? 201 : 401)
        return {
          ok: status >= 200 && status < 300,
          status,
          json: async () =>
            status >= 200 && status < 300
              ? { id: "art-1", url: "/doc/art-1" }
              : { error: "prompt: invalid token" },
        }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    }),
  )
  return writes
}

const entryName = /capture an idea or bug/i

function open() {
  fireEvent.click(screen.getByRole("button", { name: entryName }))
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.localStorage.clear()
  window.history.pushState({}, "", "/")
})

test("opens on the token step, not the prompt form", async () => {
  stubCapture()
  render(<CaptureEntry />)
  open()
  expect(await screen.findByLabelText(/capture token/i)).toBeTruthy()
  expect(screen.queryByLabelText(/^prompt$/i)).toBeNull()
})

test("remembers a valid token and opens the prompt box", async () => {
  stubCapture()
  render(<CaptureEntry />)
  open()
  fireEvent.change(await screen.findByLabelText(/capture token/i), {
    target: { value: "s3cret" },
  })
  fireEvent.click(screen.getByRole("button", { name: /continue/i }))

  expect(await screen.findByLabelText(/^prompt$/i)).toBeTruthy()
  expect(window.localStorage.getItem("factotum.capture.token")).toBe("s3cret")
})

test("rejects a wrong token and keeps the token step", async () => {
  stubCapture()
  render(<CaptureEntry />)
  open()
  fireEvent.change(await screen.findByLabelText(/capture token/i), {
    target: { value: "wrong" },
  })
  fireEvent.click(screen.getByRole("button", { name: /continue/i }))

  expect(await screen.findByText(/rejected/i)).toBeTruthy()
  expect(screen.queryByLabelText(/^prompt$/i)).toBeNull()
  expect(window.localStorage.getItem("factotum.capture.token")).toBeNull()
})

test("a remembered valid token skips the token step", async () => {
  window.localStorage.setItem("factotum.capture.token", "s3cret")
  stubCapture()
  render(<CaptureEntry />)
  open()
  expect(await screen.findByLabelText(/^prompt$/i)).toBeTruthy()
  expect(screen.queryByLabelText(/capture token/i)).toBeNull()
})

test("sends the prompt with the remembered token and opens the document", async () => {
  window.localStorage.setItem("factotum.capture.token", "s3cret")
  const writes = stubCapture()
  render(<CaptureEntry />)
  open()
  fireEvent.change(await screen.findByLabelText(/^prompt$/i), {
    target: { value: "Rework the onboarding flow" },
  })
  fireEvent.click(screen.getByRole("button", { name: /send prompt/i }))

  await waitFor(() => expect(window.location.pathname).toBe("/doc/art-1"))
  expect(writes).toHaveLength(1)
  expect(writes[0].token).toBe("Bearer s3cret")
  expect(writes[0].body).toEqual({ text: "Rework the onboarding flow" })
})

test("re-asks for the token when the server rejects it at send time", async () => {
  window.localStorage.setItem("factotum.capture.token", "s3cret")
  stubCapture({ promptStatus: 401 })
  render(<CaptureEntry />)
  open()
  fireEvent.change(await screen.findByLabelText(/^prompt$/i), {
    target: { value: "an idea" },
  })
  fireEvent.click(screen.getByRole("button", { name: /send prompt/i }))

  expect(await screen.findByLabelText(/capture token/i)).toBeTruthy()
  expect(window.localStorage.getItem("factotum.capture.token")).toBeNull()
})

test("closes when the close control is used", async () => {
  stubCapture()
  render(<CaptureEntry />)
  open()
  await screen.findByLabelText(/capture token/i)
  fireEvent.click(screen.getByRole("button", { name: /close/i }))
  expect(screen.queryByRole("dialog")).toBeNull()
})
