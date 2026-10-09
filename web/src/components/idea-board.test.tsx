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
// and wasted a row. It belongs at the START of the stats row:
// "[active] 0 7 0 of 7".
test("the status pill leads the stats row, not the title row", () => {
  render(<IdeaBoard ideas={ideas} />)

  const pill = screen.getByText("active")
  const statsRow = screen.getByRole("tooltip").parentElement!.parentElement!
  expect(statsRow).toContain(pill)
  expect(statsRow.firstElementChild).toBe(pill)

  const title = screen.getByText("Spark of a plan")
  expect(title.parentElement).not.toBe(statsRow)
})

// Khoi 2026-10-08: the long "·"-separated line ("7 tasks · 0 done · 7 active ·
// 0 blocked") wrapped/truncated on narrow cards. It becomes a compact set of
// bare counts - done, active, blocked, then the total - each colored (done
// green, active blue, blocked red, total neutral), e.g. "0 7 0 of 7".
test("the idea card shows compact color-coded counts, not the long stats line", () => {
  render(<IdeaBoard ideas={ideas} />)

  expect(screen.queryByText(/tasks/)).toBeNull()

  const counts = screen.getByRole("tooltip").parentElement!
  const done = counts.querySelector('[data-count="done"]')!
  const active = counts.querySelector('[data-count="active"]')!
  const blocked = counts.querySelector('[data-count="blocked"]')!

  expect(done.textContent).toBe("0")
  expect(active.textContent).toBe("7")
  expect(blocked.textContent).toBe("0")
  expect(done.className).toContain("text-emerald-")
  expect(active.className).toContain("text-sky-")
  expect(blocked.className).toContain("text-red-")

  const total = screen.getByText(/of 7/)
  expect(total.className).toContain("text-muted-foreground")
})

// Khoi 2026-10-08: hovering the counts spells them out in a floating tooltip,
// e.g. "0 done", "7 active", "0 blocked".
test("the counts carry a hover tooltip spelling them out", () => {
  render(<IdeaBoard ideas={ideas} />)

  const tooltip = screen.getByRole("tooltip")
  expect(tooltip.textContent).toContain("0 done")
  expect(tooltip.textContent).toContain("7 active")
  expect(tooltip.textContent).toContain("0 blocked")
  expect(tooltip.className).toContain("opacity-0")
  expect(tooltip.className).toContain("group-hover:opacity-100")

  // Keyboard focus must reveal the tooltip too. The counts span holds no
  // focusable element, so the focus scope has to be the card link itself.
  expect(tooltip.className).toContain("group-focus-within/card:opacity-100")
  const card = screen.getByText("Spark of a plan").closest("a")!
  expect(card.className).toContain("group/card")
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
