import { AppLink } from "@/components/app-link"
import { IdeaBoard } from "@/components/idea-board"
import { Kanban } from "@/components/kanban"
import { PageShell } from "@/components/page-shell"
import { RefreshBanner } from "@/components/refresh-banner"
import { Stat } from "@/components/stat"
import { TaskList } from "@/components/task-list"
import { Updates } from "@/components/updates"
import { useLiveSnapshot } from "@/hooks/use-live-snapshot"

export function Dashboard() {
  const { snapshot, state, error } = useLiveSnapshot()

  if (!snapshot) {
    return (
      <PageShell eyebrow="factotum" title="Dashboard" state={state}>
        {error ? (
          <RefreshBanner error={error} />
        ) : (
          <p className="text-sm text-muted-foreground">Loading snapshot…</p>
        )}
      </PageShell>
    )
  }

  return (
    <PageShell
      eyebrow={snapshot.project || "factotum"}
      title={snapshot.title || "Dashboard"}
      state={state}
      action={
        <AppLink
          href="/capture"
          className="text-sm text-muted-foreground hover:underline"
        >
          Capture an idea →
        </AppLink>
      }
    >
      {error ? <RefreshBanner error={error} stale subject="snapshot" /> : null}

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

      <section className="flex flex-col gap-3">
        <h2 className="text-lg font-semibold">Captures</h2>
        <IdeaBoard ideas={snapshot.ideas} />
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
        <h2 className="text-lg font-semibold">Work board</h2>
        <Kanban
          columns={[
            {
              name: "In progress",
              count: snapshot.in_flight.length,
              groups: snapshot.in_flight_groups,
            },
            {
              name: "Waiting / blocked",
              count: snapshot.waiting.length,
              groups: snapshot.waiting_groups,
            },
          ]}
        />
      </section>

      {snapshot.flags.length > 0 ? (
        <section className="flex flex-col gap-3">
          <h2 className="text-lg font-semibold">Flags</h2>
          <div className="flex flex-col gap-1">
            {snapshot.flags.map((flag) => (
              <p key={flag} className="break-words text-sm text-red-600 dark:text-red-400">
                {flag}
              </p>
            ))}
          </div>
        </section>
      ) : null}

      <Updates updates={snapshot.updates} />
    </PageShell>
  )
}
