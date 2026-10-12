import { AppLink } from "@/components/app-link"
import { Chip } from "@/components/chip"
import { PageShell } from "@/components/page-shell"
import { RefreshBanner } from "@/components/refresh-banner"
import { Stat } from "@/components/stat"
import { TaskRow } from "@/components/task-row"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { useLiveSessions } from "@/hooks/use-live-sessions"
import type { SessionView } from "@/lib/api"

// formatDuration renders a session's lifetime compactly: hours and minutes when
// it ran long, minutes and seconds when it ran short.
export function formatDuration(seconds: number): string {
  if (seconds <= 0) return "0s"
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const secs = seconds % 60
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m ${secs}s`
  return `${secs}s`
}

function SessionCard({ session }: { session: SessionView }) {
  const retired = session.state === "retired"
  const delivered = session.delivered
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-sm">{session.id}</span>
          <Chip
            label={retired ? "retired" : "alive"}
            toneKey={retired ? "waiting" : "ready-agent"}
          />
          <span className="text-xs font-normal text-muted-foreground">
            {formatDuration(session.duration_seconds)}
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="text-xs text-muted-foreground">
          started {session.started_at ?? "unknown"}
          {retired && session.stopped_at ? ` · stopped ${session.stopped_at}` : ""}
          {session.reason ? ` · ${session.reason}` : ""}
        </p>
        <p className="text-sm">
          <span className="font-medium tabular-nums">{delivered.tasks_done}</span>{" "}
          tasks done ·{" "}
          <span className="font-medium tabular-nums">{delivered.prs_merged}</span>{" "}
          PRs ·{" "}
          <span className="font-medium tabular-nums">
            {delivered.captures_advanced}
          </span>{" "}
          captures advanced
        </p>
        {delivered.tasks.length > 0 ? (
          <div className="flex flex-col gap-1">
            {delivered.tasks.map((task) => (
              <TaskRow
                key={task.id}
                task={{ ...task, class: "done", chip: "done" }}
                showChip={false}
              />
            ))}
          </div>
        ) : null}
        {delivered.prs.length > 0 ? (
          <div className="flex flex-col gap-1">
            {delivered.prs.map((pr) => (
              <AppLink
                key={pr.url}
                href={pr.url}
                className="truncate text-xs text-muted-foreground hover:underline"
              >
                {pr.title || pr.url}
              </AppLink>
            ))}
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}

// Sessions is the chief-sessions page: it lists every session alive or retired
// with when it started and stopped and why, and the work it delivered.
export function Sessions() {
  const { sessions, state, error } = useLiveSessions()

  if (!sessions) {
    return (
      <PageShell
        eyebrow="factotum"
        title="Sessions"
        state={state}
        back={{ href: "/", label: "dashboard" }}
      >
        {error ? (
          <RefreshBanner error={error} subject="sessions" />
        ) : (
          <p className="text-sm text-muted-foreground">Loading sessions…</p>
        )}
      </PageShell>
    )
  }

  const alive = sessions.filter((session) => session.state !== "retired")
  const retired = sessions.filter((session) => session.state === "retired")
  const tasksDone = sessions.reduce(
    (total, session) => total + session.delivered.tasks_done,
    0,
  )

  return (
    <PageShell
      eyebrow="factotum"
      title="Sessions"
      state={state}
      back={{ href: "/", label: "dashboard" }}
    >
      {error ? <RefreshBanner error={error} stale subject="sessions" /> : null}

      <section className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat label="Alive" value={alive.length} />
        <Stat label="Retired" value={retired.length} />
        <Stat label="Sessions" value={sessions.length} />
        <Stat label="Tasks done" value={tasksDone} />
      </section>

      {sessions.length === 0 ? (
        <p className="text-sm text-muted-foreground">No sessions recorded yet.</p>
      ) : null}

      {alive.length > 0 ? (
        <section className="flex flex-col gap-3">
          <h2 className="text-lg font-semibold">Alive · {alive.length}</h2>
          {alive.map((session) => (
            <SessionCard key={session.id} session={session} />
          ))}
        </section>
      ) : null}

      {retired.length > 0 ? (
        <section className="flex flex-col gap-3">
          <h2 className="text-lg font-semibold">Retired · {retired.length}</h2>
          {retired.map((session) => (
            <SessionCard key={session.id} session={session} />
          ))}
        </section>
      ) : null}
    </PageShell>
  )
}
