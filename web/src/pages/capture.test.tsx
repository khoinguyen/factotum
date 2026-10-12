import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import App from "@/App"

const captureUrl = "/api/capture"
const promptUrl = "/api/prompt"

// captureStub records every write so a test can assert the shape the app posts
// (method, token header, JSON body) alongside the fetch it triggers.
type writeCall = { path: string; token: string | null; body: unknown }

function stubCapture(
  config: unknown,
  opts: {
    writeStatus?: number
    writeBody?: unknown
    promptStatus?: number
    promptBody?: unknown
  } = {},
) {
  const writes: writeCall[] = []
  const writeStatus = opts.writeStatus ?? 201
  const record = (path: string, init?: RequestInit) => {
    const headers = new Headers(init?.headers)
    writes.push({
      path,
      token: headers.get("Authorization"),
      body: init?.body ? JSON.parse(String(init.body)) : null,
    })
  }
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url =
        typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url
      const path = new URL(url, "http://localhost").pathname
      const method = (init?.method ?? "GET").toUpperCase()
      if (path === captureUrl && method === "POST") {
        record(path, init)
        return {
          ok: writeStatus >= 200 && writeStatus < 300,
          status: writeStatus,
          json: async () => opts.writeBody ?? { id: "t-new", kind: "idea", url: "/idea/t-new" },
        }
      }
      if (path === promptUrl && method === "POST") {
        const status = opts.promptStatus ?? 201
        record(path, init)
        return {
          ok: status >= 200 && status < 300,
          status,
          json: async () => opts.promptBody ?? { id: "art-new", url: "/doc/art-new" },
        }
      }
      if (path === captureUrl) {
        return { ok: true, status: 200, json: async () => config }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    }),
  )
  return writes
}

function renderAt(path: string) {
  window.history.pushState({}, "", path)
  return render(<App />)
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  window.localStorage.clear()
  window.history.pushState({}, "", "/")
})

test("renders the capture form when capture is enabled", async () => {
  stubCapture({ enabled: true, project: "acme" })
  renderAt("/capture")
  expect(await screen.findByLabelText(/what is it/i)).toBeTruthy()
  expect(screen.getByLabelText(/capture token/i)).toBeTruthy()
  expect(screen.getByRole("button", { name: /^capture$/i })).toBeTruthy()
})

test("shows a single back link to the dashboard", async () => {
  stubCapture({ enabled: true, project: "acme" })
  renderAt("/capture")
  await screen.findByLabelText(/what is it/i)
  expect(screen.getAllByRole("link", { name: /dashboard/i })).toHaveLength(1)
})

test("shows the not-configured message when capture is disabled", async () => {
  stubCapture({ enabled: false })
  renderAt("/capture")
  expect(await screen.findByText(/not configured/i)).toBeTruthy()
})

test("posts an idea with the saved token and navigates to the stored capture", async () => {
  window.localStorage.setItem("factotum.capture.token", "s3cret")
  const writes = stubCapture({ enabled: true, project: "acme" })

  renderAt("/capture")
  const textarea = await screen.findByLabelText(/what is it/i)
  fireEvent.change(textarea, { target: { value: "Add a dark mode" } })
  fireEvent.click(screen.getByRole("button", { name: /^capture$/i }))

  await waitFor(() => expect(window.location.pathname).toBe("/idea/t-new"))
  expect(writes).toHaveLength(1)
  expect(writes[0].token).toBe("Bearer s3cret")
  expect(writes[0].body).toEqual({ kind: "idea", text: "Add a dark mode" })
})

test("captures a bug when the bug kind is chosen", async () => {
  window.localStorage.setItem("factotum.capture.token", "s3cret")
  const writes = stubCapture(
    { enabled: true, project: "acme" },
    { writeBody: { id: "t-bug", kind: "bug", url: "/idea/t-bug" } },
  )

  renderAt("/capture")
  await screen.findByLabelText(/what is it/i)
  fireEvent.click(screen.getByRole("button", { name: /^bug$/i }))
  fireEvent.change(screen.getByLabelText(/what is it/i), {
    target: { value: "It crashes on save" },
  })
  fireEvent.click(screen.getByRole("button", { name: /^capture$/i }))

  await waitFor(() => expect(window.location.pathname).toBe("/idea/t-bug"))
  expect(writes[0].body).toEqual({ kind: "bug", text: "It crashes on save" })
})

test("shows an error and stays put when the token is rejected", async () => {
  window.localStorage.setItem("factotum.capture.token", "wrong")
  stubCapture(
    { enabled: true, project: "acme" },
    { writeStatus: 401, writeBody: { error: "capture: invalid token" } },
  )

  renderAt("/capture")
  fireEvent.change(await screen.findByLabelText(/what is it/i), {
    target: { value: "an idea" },
  })
  fireEvent.click(screen.getByRole("button", { name: /^capture$/i }))

  expect(await screen.findByText(/invalid token/i)).toBeTruthy()
  expect(window.location.pathname).toBe("/capture")
})

test("does not post an empty capture", async () => {
  window.localStorage.setItem("factotum.capture.token", "s3cret")
  const writes = stubCapture({ enabled: true, project: "acme" })

  renderAt("/capture")
  fireEvent.change(await screen.findByLabelText(/what is it/i), {
    target: { value: "   " },
  })
  fireEvent.click(screen.getByRole("button", { name: /^capture$/i }))

  await new Promise((resolve) => setTimeout(resolve, 0))
  expect(writes).toHaveLength(0)
})

test("renders the prompt box when capture is enabled", async () => {
  stubCapture({ enabled: true, project: "acme" })
  renderAt("/capture")
  expect(await screen.findByLabelText(/^prompt$/i)).toBeTruthy()
  expect(screen.getByRole("button", { name: /send prompt/i })).toBeTruthy()
})

test("posts a prompt with the saved token and opens its document", async () => {
  window.localStorage.setItem("factotum.capture.token", "s3cret")
  const writes = stubCapture({ enabled: true, project: "acme" })

  renderAt("/capture")
  fireEvent.change(await screen.findByLabelText(/^prompt$/i), {
    target: { value: "Rework the onboarding flow" },
  })
  fireEvent.click(screen.getByRole("button", { name: /send prompt/i }))

  await waitFor(() => expect(window.location.pathname).toBe("/doc/art-new"))
  expect(writes).toHaveLength(1)
  expect(writes[0].path).toBe("/api/prompt")
  expect(writes[0].token).toBe("Bearer s3cret")
  expect(writes[0].body).toEqual({ text: "Rework the onboarding flow" })
})

test("shows an error and stays put when the prompt token is rejected", async () => {
  window.localStorage.setItem("factotum.capture.token", "wrong")
  stubCapture(
    { enabled: true, project: "acme" },
    { promptStatus: 401, promptBody: { error: "prompt: invalid token" } },
  )

  renderAt("/capture")
  fireEvent.change(await screen.findByLabelText(/^prompt$/i), {
    target: { value: "an idea" },
  })
  fireEvent.click(screen.getByRole("button", { name: /send prompt/i }))

  expect(await screen.findByText(/invalid token/i)).toBeTruthy()
  expect(window.location.pathname).toBe("/capture")
})

test("does not post an empty prompt", async () => {
  window.localStorage.setItem("factotum.capture.token", "s3cret")
  const writes = stubCapture({ enabled: true, project: "acme" })

  renderAt("/capture")
  fireEvent.change(await screen.findByLabelText(/^prompt$/i), {
    target: { value: "   " },
  })
  fireEvent.click(screen.getByRole("button", { name: /send prompt/i }))

  await new Promise((resolve) => setTimeout(resolve, 0))
  expect(writes).toHaveLength(0)
})
