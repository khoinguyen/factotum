import { AppLink } from "@/components/app-link"
import { Chip } from "@/components/chip"
import { cn } from "@/lib/utils"
import type { TaskLink, TaskView } from "@/lib/api"

// TaskRow shows one task or task reference. The chip and id are non-shrinking
// columns so a long title truncates instead of squeezing the id onto a second
// line; the reason (a blocked/waiting note) wraps anywhere so a long dependency
// list cannot overflow a phone-width row. When nested, the row is indented and
// ruled to read as a child of the group header above it.
export function TaskRow({
  task,
  nested = false,
}: {
  task: TaskView | TaskLink
  nested?: boolean
}) {
  const view = task as TaskView
  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md px-2 py-1.5 hover:bg-accent",
        nested && "ml-3 border-l-2 border-border pl-3",
      )}
    >
      <AppLink href={task.url} className="flex min-w-0 items-center gap-2">
        <Chip label={task.chip} toneKey={task.class} />
        <span className="shrink-0 whitespace-nowrap font-mono text-xs text-muted-foreground">
          {task.id}
        </span>
        <span className="min-w-0 truncate text-sm">{task.title}</span>
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
