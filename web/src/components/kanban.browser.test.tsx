import { cleanup, render, screen } from "@testing-library/react"
import { page } from "vitest/browser"
import { afterEach, expect, test } from "vitest"

import "@/index.css"

import { Kanban, type KanbanColumn } from "@/components/kanban"
import type { TaskView } from "@/lib/api"

afterEach(cleanup)

// A hyphenated task id is a single mono token. jsdom cannot lay text out, so the
// only way to prove the board keeps it on one line, fits the viewport, and nests
// children is to measure the rendered boxes in a real browser.
const grouped: TaskView = {
  id: "t-bwc4ootcvi",
  title:
    "A child task with a long title that must truncate rather than wrap or push the row off screen",
  class: "review",
  chip: "in review",
  url: "/task/t-bwc4ootcvi",
  origin: "t-d2tmr544nk",
  origin_title: "Grouped idea",
}

const ungrouped: TaskView = {
  id: "t-un01",
  title: "An ungrouped task that belongs directly to the column",
  class: "blocked",
  chip: "blocked",
  url: "/task/t-un01",
}

const columns: KanbanColumn[] = [
  {
    name: "In progress",
    count: 1,
    groups: [
      {
        idea_id: "t-d2tmr544nk",
        idea_title: "Grouped idea with a fairly long title of its own",
        idea_state: "active",
        tasks: [grouped],
      },
    ],
  },
  {
    name: "Waiting / blocked",
    count: 1,
    groups: [{ ungrouped: true, tasks: [ungrouped] }],
  },
]

// The number of line boxes the element's text occupies. A blockified flex item
// hides wrapping from getClientRects(), but a Range over its text contents
// reports one rect per rendered line.
function lineCount(el: Element): number {
  const range = document.createRange()
  range.selectNodeContents(el)
  return range.getClientRects().length
}

const VIEWPORTS = [390, 640, 1024, 1280]

for (const width of VIEWPORTS) {
  test(`the work board holds its layout at ${width}px`, async () => {
    await page.viewport(width, 900)
    render(<Kanban columns={columns} />)

    const id = screen.getByText(grouped.id)
    const headerId = screen.getByText("t-d2tmr544nk")
    const childRow = id.closest("div")!
    const headerRow = headerId.closest("div")!

    // No horizontal overflow: the document never grows wider than the viewport.
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)

    // Task id (and the group header's idea id) stay on one line.
    expect(lineCount(id)).toBe(1)
    expect(lineCount(headerId)).toBe(1)

    // Grouped children are nested: indented and ruled, and to the right of the
    // group header above them.
    const childStyle = getComputedStyle(childRow)
    expect(childStyle.marginLeft).toBe("12px")
    expect(childStyle.borderLeftWidth).toBe("2px")
    expect(childRow.getBoundingClientRect().left).toBeGreaterThan(
      headerRow.getBoundingClientRect().left,
    )

    // An ungrouped task is not nested: no indent, no rule.
    const ungroupedRow = screen.getByText(ungrouped.id).closest("div")!
    const ungroupedStyle = getComputedStyle(ungroupedRow)
    expect(ungroupedStyle.marginLeft).toBe("0px")
    expect(ungroupedStyle.borderLeftWidth).toBe("0px")
  })
}
