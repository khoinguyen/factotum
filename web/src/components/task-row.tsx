import { AppLink } from "@/components/app-link"
import { Chip } from "@/components/chip"
import type { TaskLink, TaskView } from "@/lib/api"

// TaskRow shows one task or task reference. The reason (a blocked/waiting note)
// wraps anywhere so a long dependency list cannot overflow a phone-width row.
export function TaskRow({ task }: { task: TaskView | TaskLink }) {
  const view = task as TaskView
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md px-2 py-1.5 hover:bg-accent">
      <AppLink href={task.url} className="flex min-w-0 items-center gap-2">
        <Chip label={task.chip} toneKey={task.class} />
        <span className="font-mono text-xs text-muted-foreground">
          {task.id}
        </span>
        <span className="truncate text-sm">{task.title}</span>
      </AppLink>
      {view.milestone ? <Chip label="milestone" toneKey="done" /> : null}
      {view.unblocks ? (
        <span className="text-xs text-muted-foreground">
          unblocks {view.unblocks}
        </span>
      ) : null}
      {view.wave ? (
        <span className="text-xs text-muted-foreground">w{view.wave}</span>
      ) : null}
      {view.repo ? (
        <span className="text-xs text-muted-foreground">{view.repo}</span>
      ) : null}
      {view.assignee ? (
        <span className="text-xs text-muted-foreground">{view.assignee}</span>
      ) : null}
      {view.origin ? (
        <AppLink
          href={`/idea/${view.origin}`}
          className="text-xs text-muted-foreground hover:underline"
        >
          capture {view.origin}
        </AppLink>
      ) : null}
      {task.reason ? (
        <span className="min-w-0 basis-full break-words text-xs text-muted-foreground sm:basis-auto">
          {task.reason}
          {task.detail ? ` · ${task.detail}` : ""}
        </span>
      ) : null}
    </div>
  )
}
