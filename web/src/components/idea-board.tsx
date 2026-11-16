import { AppLink } from "@/components/app-link"
import { Chip, kindLabel } from "@/components/chip"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import type { IdeaView, Snapshot } from "@/lib/api"

function Count({
  name,
  label,
  value,
  tone,
}: {
  name: string
  label: string
  value: number
  tone: string
}) {
  return (
    <span className="flex flex-col items-center gap-0.5 leading-none">
      <span data-count={name} className={`font-medium ${tone}`}>
        {value}
      </span>
      <span data-count-label={name} className="text-xs text-muted-foreground">
        {label}
      </span>
    </span>
  )
}

function IdeaCard({ idea }: { idea: IdeaView }) {
  const artifacts = idea.artifacts?.length ?? 0
  const human = idea.human ?? 0
  const humanTasks = idea.human_tasks ?? []
  const humanTitles = humanTasks.map((task) => task.title).join(", ")
  return (
    <AppLink
      href={idea.url}
      className="group/card flex flex-col gap-1 rounded-lg border bg-card px-2 py-1.5 hover:bg-accent"
    >
      <span className="truncate text-sm font-medium">{idea.title}</span>
      <span className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
        <Chip label={kindLabel(idea.kind)} toneKey={idea.kind} />
        {/* Each count carries its own label: the tooltip alone left the five
            bare numbers unreadable at a glance and to touch input. */}
        <span className="group relative flex items-start gap-1.5 tabular-nums">
          <Count
            name="done"
            label="done"
            value={idea.done}
            tone="text-emerald-700 dark:text-emerald-300"
          />
          <Count
            name="todo"
            label="todo"
            value={idea.todo}
            tone="text-amber-700 dark:text-amber-300"
          />
          <Count
            name="active"
            label="active"
            value={idea.active}
            tone="text-sky-700 dark:text-sky-300"
          />
          <Count
            name="blocked"
            label="blocked"
            value={idea.blocked}
            tone="text-red-700 dark:text-red-300"
          />
          <Count
            name="human"
            label="human"
            value={human}
            tone="text-violet-700 dark:text-violet-300"
          />
          <span className="leading-none text-muted-foreground">of {idea.total}</span>
          <span
            role="tooltip"
            className="pointer-events-none absolute left-0 top-full z-20 mt-1 hidden whitespace-nowrap rounded-md border bg-popover px-2 py-1 text-popover-foreground shadow-md group-hover:block group-focus-within/card:block"
          >
            {idea.done} done · {idea.todo} todo · {idea.active} active ·{" "}
            {idea.blocked} blocked · {human} human
            {humanTitles ? ` (${humanTitles})` : ""}
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

// IdeaBoard is the captures-primary view: four lanes rolled up from the tasks
// promoted from each capture (blocked needs unblock outranks work still moving).
// The captured lane leads as the backlog; the rest follow in lifecycle order.
export function IdeaBoard({ ideas }: { ideas: Snapshot["ideas"] }) {
  return (
    <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-4">
      <IdeaLane title="Backlog" ideas={ideas.captured} />
      <IdeaLane title="Blocked — needs unblock" ideas={ideas.blocked} />
      <IdeaLane title="In progress" ideas={ideas.active} />
      <IdeaLane title="Done" ideas={ideas.finished} />
    </div>
  )
}
