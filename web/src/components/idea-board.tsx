import { AppLink } from "@/components/app-link"
import { Chip } from "@/components/chip"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import type { IdeaView, Snapshot } from "@/lib/api"

function IdeaCard({ idea }: { idea: IdeaView }) {
  const artifacts = idea.artifacts?.length ?? 0
  return (
    <AppLink
      href={idea.url}
      className="group/card flex flex-col gap-1 rounded-lg border bg-card px-2 py-1.5 hover:bg-accent"
    >
      <span className="truncate text-sm font-medium">{idea.title}</span>
      <span className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
        <Chip label={idea.state} toneKey={idea.state} />
        <span className="group relative flex items-center gap-1 tabular-nums">
          <span data-count="done" className="font-medium text-emerald-700 dark:text-emerald-300">
            {idea.done}
          </span>
          <span data-count="active" className="font-medium text-sky-700 dark:text-sky-300">
            {idea.active}
          </span>
          <span data-count="blocked" className="font-medium text-red-700 dark:text-red-300">
            {idea.blocked}
          </span>
          <span className="text-muted-foreground">of {idea.total}</span>
          <span
            role="tooltip"
            className="pointer-events-none absolute left-0 top-full z-20 mt-1 whitespace-nowrap rounded-md border bg-popover px-2 py-1 text-popover-foreground opacity-0 shadow-md transition-opacity group-hover:opacity-100 group-focus-within/card:opacity-100"
          >
            {idea.done} done · {idea.active} active · {idea.blocked} blocked
            {artifacts > 0
              ? ` · ${artifacts} artifact${artifacts === 1 ? "" : "s"}`
              : ""}
          </span>
        </span>
      </span>
    </AppLink>
  )
}

function IdeaLane({ title, ideas }: { title: string; ideas: IdeaView[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {title} · {ideas.length}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-1.5 px-2">
        {ideas.length === 0 ? (
          <p className="text-sm text-muted-foreground">nothing here</p>
        ) : (
          ideas.map((idea) => <IdeaCard key={idea.id} idea={idea} />)
        )}
      </CardContent>
    </Card>
  )
}

// IdeaBoard is the ideas-primary view: four lanes of captures rolled up from
// their promoted tasks (blocked needs unblock outranks work still moving).
export function IdeaBoard({ ideas }: { ideas: Snapshot["ideas"] }) {
  return (
    <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
      <IdeaLane title="Blocked — needs unblock" ideas={ideas.blocked} />
      <IdeaLane title="In progress" ideas={ideas.active} />
      <IdeaLane title="Finished" ideas={ideas.finished} />
      <IdeaLane title="Captured" ideas={ideas.captured} />
    </div>
  )
}
