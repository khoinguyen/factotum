import { AppLink } from "@/components/app-link"
import { Chip } from "@/components/chip"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import type { IdeaView, Snapshot } from "@/lib/api"

function IdeaCard({ idea }: { idea: IdeaView }) {
  const artifacts = idea.artifacts?.length ?? 0
  return (
    <AppLink
      href={idea.url}
      className="flex flex-col gap-1 rounded-lg border bg-card px-3 py-2 hover:bg-accent"
    >
      <span className="flex min-w-0 items-center gap-2">
        <Chip label={idea.state} toneKey={idea.state} />
        <span className="truncate text-sm font-medium">{idea.title}</span>
      </span>
      <span className="text-xs text-muted-foreground">
        {idea.total} task{idea.total === 1 ? "" : "s"} · {idea.done} done ·{" "}
        {idea.active} active · {idea.blocked} blocked
        {artifacts > 0
          ? ` · ${artifacts} artifact${artifacts === 1 ? "" : "s"}`
          : ""}
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
      <CardContent className="flex flex-col gap-2">
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
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <IdeaLane title="Blocked — needs unblock" ideas={ideas.blocked} />
      <IdeaLane title="In progress" ideas={ideas.active} />
      <IdeaLane title="Finished" ideas={ideas.finished} />
      <IdeaLane title="Captured" ideas={ideas.captured} />
    </div>
  )
}
