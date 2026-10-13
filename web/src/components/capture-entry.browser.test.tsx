import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { page } from "vitest/browser"
import { afterEach, expect, test, vi } from "vitest"

import "@/index.css"

import { CaptureEntry } from "@/components/capture-entry"

// The capture entry is a floating action button on desktop and a sticky bottom
// bar on mobile. jsdom cannot lay it out, so the only way to prove the shape
// changes with the viewport (and neither the button nor the open modal scrolls
// the page horizontally) is to measure the rendered boxes in a real browser.

function stubCapture() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ enabled: true, project: "acme", authorized: false }),
    })),
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

test("desktop renders a floating action button, not a full-width bar", async () => {
  stubCapture()
  const width = 1280
  const height = 900
  await page.viewport(width, height)
  render(<CaptureEntry />)

  const button = screen.getByRole("button", { name: /capture an idea or bug/i })
  const box = button.getBoundingClientRect()

  expect(getComputedStyle(button).position).toBe("fixed")
  // A floating pill: right-aligned, well short of the viewport width.
  expect(box.right).toBeLessThanOrEqual(width)
  expect(box.right).toBeGreaterThan(width / 2)
  expect(box.width).toBeLessThan(width / 2)
  // And it sits near the bottom of the viewport.
  expect(box.bottom).toBeGreaterThan(height * 0.6)
  expect(box.bottom).toBeLessThanOrEqual(height)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

test("mobile renders a full-width bar pinned to the bottom", async () => {
  stubCapture()
  const width = 360
  const height = 800
  await page.viewport(width, height)
  render(<CaptureEntry />)

  const button = screen.getByRole("button", { name: /capture an idea or bug/i })
  const box = button.getBoundingClientRect()

  expect(getComputedStyle(button).position).toBe("fixed")
  // A bar: flush to both viewport edges.
  expect(box.left).toBeLessThanOrEqual(1)
  expect(box.right).toBeGreaterThanOrEqual(width - 1)
  // Pinned to the bottom of the viewport.
  expect(box.bottom).toBeGreaterThanOrEqual(height - 1)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

for (const { name, width, height } of [
  { name: "desktop", width: 1280, height: 900 },
  { name: "mobile", width: 360, height: 800 },
]) {
  test(`the modal opens at ${name} without horizontal overflow`, async () => {
    stubCapture()
    await page.viewport(width, height)
    render(<CaptureEntry />)

    fireEvent.click(screen.getByRole("button", { name: /capture an idea or bug/i }))

    expect(await screen.findByRole("dialog")).toBeTruthy()
    // The token step comes first, the prompt box second.
    expect(await screen.findByLabelText(/capture token/i)).toBeTruthy()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
}
