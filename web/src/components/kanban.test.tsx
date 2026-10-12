import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, test } from "vitest"

import { Kanban, type KanbanColumn } from "@/components/kanban"
import type { TaskView } from "@/lib/api"

afterEach(cleanup)

function task(id: string, origin: string, originTitle: string): TaskView {
  return {
    id,
    title: `Work for ${originTitle}`,
    class: "review",
    chip: "in review",
    url: `/task/${id}`,
    origin,
    origin_title: originTitle,
  }
}

const columns: KanbanColumn[] = [
  {
    name: "In progress",
    count: 3,
    groups: [
      {
        idea_id: "t-bug",
        idea_title: "Checkout total is wrong",
        idea_state: "active",
        idea_kind: "bug",
        tasks: [task("t-b1", "t-bug", "Checkout total is wrong")],
      },
      {
        idea_id: "t-idea",
        idea_title: "Add CSV import",
        idea_state: "active",
        idea_kind: "idea",
        tasks: [task("t-i1", "t-idea", "Add CSV import")],
      },
      {
        ungrouped: true,
        tasks: [
          {
            id: "t-plain",
            title: "Standalone task",
            class: "blocked",
            chip: "blocked",
            url: "/task/t-plain",
          },
        ],
      },
    ],
  },
]

function chipsIn(el: Element): string[] {
  return Array.from(el.querySelectorAll('[data-slot="badge"]')).map(
    (node) => node.textContent ?? "",
  )
}

function headerChips(title: string): string[] {
  return chipsIn(screen.getByText(title).closest("div")!)
}

// The parent row names its origin capture's kind, so a bug-origin group and an
// idea-origin group are not shown identically.
test("the work-board parent row shows the capture kind chip", () => {
  render(<Kanban columns={columns} />)

  const bugChips = headerChips("Checkout total is wrong")
  expect(bugChips).toContain("Bug")
  expect(bugChips).toContain("active")

  const ideaChips = headerChips("Add CSV import")
  expect(ideaChips).toContain("Idea")
  expect(ideaChips).not.toContain("Bug")
})

// Ungrouped tasks have no origin capture, so their row carries no kind chip.
test("the ungrouped bucket shows no capture kind chip", () => {
  render(<Kanban columns={columns} />)

  const header = screen.getByText("ungrouped").closest("div")!
  expect(chipsIn(header)).toEqual(["ungrouped"])
})
