import { TaskRow } from "@/components/task-row"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import type { TaskView } from "@/lib/api"

export function TaskList({
  title,
  tasks,
  empty,
}: {
  title: string
  tasks: TaskView[]
  empty: string
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {title} · {tasks.length}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-1">
        {tasks.length === 0 ? (
          <p className="text-sm text-muted-foreground">{empty}</p>
        ) : (
          tasks.map((task) => <TaskRow key={task.id} task={task} />)
        )}
      </CardContent>
    </Card>
  )
}
