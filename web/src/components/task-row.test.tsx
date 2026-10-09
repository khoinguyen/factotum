import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, test } from "vitest"

import { TaskRow } from "@/components/task-row"
import { Kanban } from "@/components/kanban"
import type { Snapshot, TaskView } from "@/lib/api"

afterEach(cleanup)

const childTask: TaskView = {
  id: "t-bwc4ootcvi",
  title: "A child task with a reasonably long title that should truncate",
  class: "review",
  chip: "in review",
  url: "/task/t-bwc4ootcvi",
  origin: "t-d2tmr544nk",
  origin_title: "Grouped idea",
}

// Regression: the id is a hyphenated token, so a shrinkable flex item wraps at
// the hyphen and collides with the chip when the title is long. The id must be
// held on one line and the title must absorb the shrink (truncate).
test("a task row keeps the id on one line and truncates the title", () => {
  render(<TaskRow task={childTask} />)
  const id = screen.getByText("t-bwc4ootcvi")
  expect(id.className).toContain("whitespace-nowrap")
  expect(id.className).toContain("shrink-0")
  const title = screen.getByText(childTask.title)
  expect(title.className).toContain("truncate")
})

// A task shown under its origin idea/bug is nested, so it must be indented and
// ruled to read as a child of the group header above it.
test("a nested task row is visually indented", () => {
  const { rerender } = render(<TaskRow task={childTask} />)
  const row = screen.getByText("t-bwc4ootcvi").closest("div")!
  expect(row.className).not.toContain("border-l-2")

  rerender(<TaskRow task={childTask} nested />)
  const nestedRow = screen.getByText("t-bwc4ootcvi").closest("div")!
  expect(nestedRow.className).toContain("ml-3")
  expect(nestedRow.className).toContain("border-l-2")
})

// The work board renders grouped children as nested rows; the group header is
// the idea/bug, so every task under it is a nested child.
test("the work board nests grouped tasks under their origin", () => {
  const snapshot: Snapshot = {
    title: "Acme",
    project: "acme",
    snapshot: "2026-01-01T00:00:00Z",
    stats: { scope: 1, ideas: 1, done: 0, ready_agent: 0, ready_human: 0, blocked: 0, cycles: 0, waves: 1 },
    next_agent: [],
    next_human: [],
    in_flight: [childTask],
    waiting: [],
    in_flight_groups: [{ idea_id: "t-d2tmr544nk", idea_title: "Grouped idea", tasks: [childTask] }],
    waiting_groups: [],
    ideas: { blocked: [], active: [], finished: [], captured: [] },
    updates: [],
    flags: [],
  }
  render(
    <Kanban
      columns={[
        { name: "In progress", count: 1, groups: snapshot.in_flight_groups },
        { name: "Waiting / blocked", count: 0, groups: [] },
      ]}
    />,
  )
  const row = screen.getByText("t-bwc4ootcvi").closest("div")!
  expect(row.className).toContain("border-l-2")

  // The group header's idea id is hyphenated like a task id, so it must be held
  // on one line too (or it wraps and drags the header title out of alignment).
  const header = screen.getByText("t-d2tmr544nk")
  expect(header.className).toContain("whitespace-nowrap")
  expect(header.className).toContain("shrink-0")
})
