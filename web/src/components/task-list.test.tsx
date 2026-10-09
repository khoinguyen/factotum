import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, test } from "vitest"

import { Kanban } from "@/components/kanban"
import { TaskList } from "@/components/task-list"
import type { TaskView } from "@/lib/api"

afterEach(cleanup)

const agentNext: TaskView = {
  id: "t-agent",
  title: "Build the thing",
  class: "ready-agent",
  chip: "agent next",
  url: "/task/t-agent",
  repo: "factotum",
  wave: 2,
}

const waitingTask: TaskView = {
  id: "t-wait",
  title: "Wait on a dependency",
  class: "waiting",
  chip: "waiting",
  url: "/task/t-wait",
  reason: "dependency",
  detail: "t-blocker",
}

// The Agent next / Human next lists are status columns: the header already says
// the status, so the per-item chip just repeats it. Drop the chip, but keep the
// title and the metadata line.
test("a status-encoded list drops the redundant per-item chip", () => {
  render(
    <TaskList title="Agent next" tasks={[agentNext]} empty="nothing queued" />,
  )
  expect(screen.queryByText("agent next")).toBeNull()
  expect(screen.getByText("Build the thing")).toBeTruthy()
  expect(screen.getByText("factotum")).toBeTruthy()
  expect(screen.getByText("w2")).toBeTruthy()
})

// The Waiting/blocked column mixes blocked and waiting rows, so each row's chip
// still carries information the header does not.
test("a mixed-status column keeps the per-item chip", () => {
  render(
    <Kanban
      columns={[
        {
          name: "Waiting / blocked",
          count: 1,
          groups: [
            { idea_id: "", idea_title: "", ungrouped: true, tasks: [waitingTask] },
          ],
        },
      ]}
    />,
  )
  expect(screen.getByText("waiting")).toBeTruthy()
  expect(screen.getByText("Wait on a dependency")).toBeTruthy()
  expect(screen.getByText("dependency · t-blocker")).toBeTruthy()
})
