import { cleanup, render, screen } from "@testing-library/react"
import { page } from "vitest/browser"
import { afterEach, expect, test } from "vitest"

import "@/index.css"

import { PageShell } from "@/components/page-shell"

afterEach(cleanup)

// The number of line boxes the element's text occupies. A blockified flex item
// hides wrapping from getClientRects(), but a Range over its text contents
// reports one rect per rendered line.
function lineCount(el: Element): number {
  const range = document.createRange()
  range.selectNodeContents(el)
  return range.getClientRects().length
}

// The header row (capture link + live badge + theme toggle) once crowded the
// title into a single character per line at phone widths. jsdom cannot lay text
// out, so the only way to prove the title stays readable and the controls stay
// on screen is to measure the rendered boxes in a real browser.
const PHONE_WIDTHS = [360, 390]

for (const width of PHONE_WIDTHS) {
  test(`the header lays out at ${width}px`, async () => {
    await page.viewport(width, 800)
    render(
      <PageShell eyebrow="factotum" title="Factotum dashboard" state="live">
        <p>body</p>
      </PageShell>,
    )

    const title = screen.getByRole("heading", { name: "Factotum dashboard" })

    // No horizontal overflow: the document never grows wider than the viewport.
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)

    // The title stays readable instead of wrapping one character per line.
    expect(lineCount(title)).toBeLessThanOrEqual(2)

    // Every control stays inside the viewport (usable, not clipped).
    const controls = [
      screen.getByRole("link", { name: /capture an idea or bug/i }),
      screen.getByText("live"),
      screen.getByRole("group", { name: "Theme" }),
    ]
    for (const control of controls) {
      const box = control.getBoundingClientRect()
      expect(box.left).toBeGreaterThanOrEqual(0)
      expect(box.right).toBeLessThanOrEqual(width)
    }
  })
}
