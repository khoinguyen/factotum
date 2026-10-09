import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, test } from "vitest"

import { IdeaBoard } from "@/components/idea-board"
import type { IdeaView, Snapshot } from "@/lib/api"

afterEach(cleanup)

const idea: IdeaView = {
  id: "t-idea",
  kind: "idea",
  title: "Spark of a plan",
  state: "active",
  chip: "active",
  url: "/idea/t-idea",
  total: 7,
  done: 0,
  active: 7,
  blocked: 0,
  artifacts: [{ id: "art-1", kind: "doc", title: "Design note", url: "/doc/art-1" }],
}

const ideas: Snapshot["ideas"] = {
  blocked: [],
  active: [idea],
  finished: [],
  captured: [],
}

// Khoi 2026-10-08: the status pill used to take its own slot beside the title
// and wasted a row. It belongs on the task-count stats line instead.
test("the status pill rides the stats row, not the title row", () => {
  render(<IdeaBoard ideas={ideas} />)

  const pill = screen.getByText("active")
  const stats = screen.getByText(/7 tasks/)
  expect(stats.parentElement).toContain(pill)

  const title = screen.getByText("Spark of a plan")
  expect(title.parentElement).not.toBe(pill.parentElement)
})

// Khoi 2026-10-08: tighten the board - card padding, the card-to-column-edge
// gap, and the gap between columns.
test("the ideas board tightens card and column spacing", () => {
  const { container } = render(<IdeaBoard ideas={ideas} />)

  const card = screen.getByText("Spark of a plan").closest("a")!
  expect(card.className).toContain("px-2")
  expect(card.className).toContain("py-1.5")

  const lane = screen.getByText(/In progress/).closest('[data-slot="card"]')!
  const laneContent = lane.querySelector('[data-slot="card-content"]')!
  expect(laneContent.className).toContain("px-2")

  const board = container.firstElementChild!
  expect(board.className).toContain("gap-2")
})
