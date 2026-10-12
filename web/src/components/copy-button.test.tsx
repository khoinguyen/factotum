import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import { CopyButton } from "@/components/copy-button"

// jsdom has no clipboard, so each test installs a writeText spy on navigator.
function stubClipboard() {
  const writeText = vi.fn(async () => {})
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  })
  return writeText
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

test("copies its value to the clipboard", async () => {
  const writeText = stubClipboard()
  render(<CopyButton value="t-idea" label="Copy capture id" />)

  fireEvent.click(screen.getByRole("button", { name: "Copy capture id" }))

  await waitFor(() => expect(writeText).toHaveBeenCalledWith("t-idea"))
})

test("confirms the copy until it resets", async () => {
  stubClipboard()
  render(<CopyButton value="t-idea" label="Copy capture id" />)
  const button = screen.getByRole("button", { name: "Copy capture id" })
  expect(button.getAttribute("title")).toBe("Copy capture id")

  fireEvent.click(button)

  await waitFor(() => expect(button.getAttribute("title")).toBe("Copied"))
})

test("stays quiet when the clipboard is unavailable", async () => {
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText: vi.fn(async () => Promise.reject(new Error("denied"))) },
    configurable: true,
  })
  render(<CopyButton value="t-idea" label="Copy capture id" />)

  fireEvent.click(screen.getByRole("button", { name: "Copy capture id" }))

  // The control must not crash or flip to the copied state on a rejected write.
  await waitFor(() => expect(screen.getByRole("button", { name: "Copy capture id" })).toBeTruthy())
  expect(screen.getByRole("button", { name: "Copy capture id" }).getAttribute("title")).toBe(
    "Copy capture id",
  )
})
