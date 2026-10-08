import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { useLiveSnapshot, type LiveState } from "@/hooks/use-live-snapshot"
import type { IdeaView, TaskView, UpdateView } from "@/lib/api"

const liveLabel: Record<LiveState, string> = {
  connecting: "connecting…",
  live: "live",
  reconnecting: "reconnecting…",
  unsupported: "no live updates",
}

const liveTone: Record<LiveState, string> = {
  connecting: "bg-muted",
  live: "bg-emerald-500",
  reconnecting: "bg-amber-500",
  unsupported: "bg-muted-foreground",
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg border bg-card px-4 py-3">
      <div className="text-2xl font-semibold tabular-nums">{value}</div>
      <div className="text-xs text-muted-foreground">{label}</div>
    </div>
  )
}

function TaskRow({ task }: { task: TaskView }) {
  return (
    <a
      href={task.url}
      className="flex items-center gap-2 rounded-md px-2 py-1.5 hover:bg-accent"
    >
      <Badge variant="secondary">{task.chip}</Badge>
      <span className="font-mono text-xs text-muted-foreground">{task.id}</span>
      <span className="truncate text-sm">{task.title}</span>
    </a>
  )
}

function TaskList({
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

function IdeaCard({ idea }: { idea: IdeaView }) {
  return (
    <a
      href={idea.url}
      className="flex flex-col gap-1 rounded-lg border bg-card px-3 py-2 hover:bg-accent"
    >
      <span className="flex items-center gap-2">
        <Badge variant="outline">{idea.state}</Badge>
        <span className="truncate text-sm font-medium">{idea.title}</span>
      </span>
      <span className="text-xs text-muted-foreground">
        {idea.total} tasks · {idea.done} done · {idea.active} active ·{" "}
        {idea.blocked} blocked
      </span>
    </a>
  )
}

function IdeaLane({ title, ideas }: { title: string; ideas: IdeaView[] }) {
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-sm font-medium text-muted-foreground">
        {title} · {ideas.length}
      </h3>
      {ideas.length === 0 ? (
        <p className="text-sm text-muted-foreground">nothing here</p>
      ) : (
        ideas.map((idea) => <IdeaCard key={idea.id} idea={idea} />)
      )}
    </div>
  )
}

function UpdateList({ updates }: { updates: UpdateView[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Recent updates</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-1">
        {updates.length === 0 ? (
          <p className="text-sm text-muted-foreground">No events yet.</p>
        ) : (
          updates.map((update, i) => (
            <div key={`${update.time}-${i}`} className="flex gap-2 text-sm">
              <time className="shrink-0 font-mono text-xs text-muted-foreground">
                {update.time}
              </time>
              <span className="truncate">{update.summary}</span>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  )
}

export default function App() {
  const { snapshot, state } = useLiveSnapshot()

  return (
    <div className="mx-auto flex min-h-svh max-w-6xl flex-col gap-6 p-6">
      <header className="flex items-center justify-between">
        <div>
          <p className="text-xs uppercase tracking-wide text-muted-foreground">
            {snapshot?.project ?? "factotum"}
          </p>
          <h1 className="text-xl font-semibold">
            {snapshot?.title ?? "Dashboard"}
          </h1>
        </div>
        <span className="flex items-center gap-2 text-sm text-muted-foreground">
          <span className={`size-2 rounded-full ${liveTone[state]}`} />
          {liveLabel[state]}
        </span>
      </header>

      {snapshot === null ? (
        <p className="text-sm text-muted-foreground">Loading snapshot…</p>
      ) : (
        <>
          <p className="text-xs text-muted-foreground">
            updated {snapshot.snapshot}
          </p>

          <section className="grid grid-cols-2 gap-3 sm:grid-cols-4 lg:grid-cols-8">
            <Stat label="Ideas" value={snapshot.stats.ideas} />
            <Stat label="In scope" value={snapshot.stats.scope} />
            <Stat label="Done" value={snapshot.stats.done} />
            <Stat label="Agent next" value={snapshot.stats.ready_agent} />
            <Stat label="Human next" value={snapshot.stats.ready_human} />
            <Stat label="Blocked" value={snapshot.stats.blocked} />
            <Stat label="Cycles" value={snapshot.stats.cycles} />
            <Stat label="Waves" value={snapshot.stats.waves} />
          </section>

          <section className="grid gap-4 sm:grid-cols-2">
            <TaskList
              title="Agent next"
              tasks={snapshot.next_agent}
              empty="nothing queued"
            />
            <TaskList
              title="Human next"
              tasks={snapshot.next_human}
              empty="nothing queued"
            />
          </section>

          <section className="flex flex-col gap-3">
            <h2 className="text-lg font-semibold">Ideas</h2>
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <IdeaLane title="Blocked" ideas={snapshot.ideas.blocked} />
              <IdeaLane title="In progress" ideas={snapshot.ideas.active} />
              <IdeaLane title="Finished" ideas={snapshot.ideas.finished} />
              <IdeaLane title="Captured" ideas={snapshot.ideas.captured} />
            </div>
          </section>

          <section className="grid gap-4 sm:grid-cols-2">
            <TaskList
              title="In progress"
              tasks={snapshot.in_flight}
              empty="nothing in flight"
            />
            <TaskList
              title="Waiting"
              tasks={snapshot.waiting}
              empty="nothing waiting"
            />
          </section>

          <UpdateList updates={snapshot.updates} />
        </>
      )}
    </div>
  )
}
