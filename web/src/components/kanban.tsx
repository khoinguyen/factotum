import { AppLink } from "@/components/app-link"
import { Chip } from "@/components/chip"
import { TaskRow } from "@/components/task-row"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import type { TaskGroup } from "@/lib/api"

export type KanbanColumn = {
  name: string
  count: number
  groups: TaskGroup[]
}

// Kanban is the work board: each column holds the tasks grouped by the idea they
// were promoted from (the origin edge), with tasks that have no origin idea in a
// separate ungrouped bucket last.
export function Kanban({ columns }: { columns: KanbanColumn[] }) {
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      {columns.map((column) => (
        <Card key={column.name}>
          <CardHeader>
            <CardTitle>
              {column.name} · {column.count}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            {column.groups.length === 0 ? (
              <p className="text-sm text-muted-foreground">nothing here</p>
            ) : (
              column.groups.map((group) => (
                <TaskGroupBlock
                  key={group.idea_id || "__ungrouped__"}
                  group={group}
                />
              ))
            )}
          </CardContent>
        </Card>
      ))}
    </div>
  )
}

function TaskGroupBlock({ group }: { group: TaskGroup }) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex min-w-0 items-center gap-2">
        {group.ungrouped || !group.idea_id ? (
          <Chip label="ungrouped" toneKey="capture" />
        ) : (
          <>
            <AppLink
              href={`/idea/${group.idea_id}`}
              className="font-mono text-xs text-muted-foreground hover:underline"
            >
              {group.idea_id}
            </AppLink>
            <span className="truncate text-sm font-medium">
              {group.idea_title}
            </span>
          </>
        )}
      </div>
      <div className="flex flex-col gap-1">
        {group.tasks.map((task) => (
          <TaskRow key={task.id} task={task} />
        ))}
      </div>
    </div>
  )
}
