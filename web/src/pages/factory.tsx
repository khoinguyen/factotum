import { AppLink } from "@/components/app-link"
import { Chip } from "@/components/chip"
import { PageShell } from "@/components/page-shell"
import { RefreshBanner } from "@/components/refresh-banner"
import { Stat } from "@/components/stat"
import { TaskRow } from "@/components/task-row"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { useLiveSnapshot } from "@/hooks/use-live-snapshot"
import type { PipelineView } from "@/lib/api"

function PipelineCard({ pipeline }: { pipeline: PipelineView }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <AppLink
            href={pipeline.capture_url}
            className="min-w-0 truncate hover:underline"
          >
            {pipeline.capture_title || pipeline.capture}
          </AppLink>
          <Chip label={pipeline.state} toneKey={pipeline.state} />
          {pipeline.gate ? <Chip label={pipeline.gate} toneKey="blocked" /> : null}
          <span className="font-mono text-xs text-muted-foreground">
            {pipeline.id}
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {pipeline.error ? (
          <p className="break-words text-sm text-red-600 dark:text-red-400">
            {pipeline.error}
          </p>
        ) : null}
        {pipeline.command ? (
          <div className="flex flex-col gap-1">
            <p className="text-xs font-medium text-muted-foreground">
              Clear the gate
            </p>
            <code className="break-words rounded-md bg-muted px-2 py-1 font-mono text-xs">
              {pipeline.command}
            </code>
          </div>
        ) : null}
        {pipeline.produced.length > 0 ? (
          <div className="flex flex-col gap-1">
            <p className="text-xs font-medium text-muted-foreground">
              Produced tasks
            </p>
            {pipeline.produced.map((task) => (
              <TaskRow key={task.id} task={task} showChip={false} />
            ))}
          </div>
        ) : null}
      </CardContent>
    </Card>
  )
}

// Factory is the /factory view: each pipeline with its origin capture, phase,
// open gate, produced tasks, and the exact command that clears the gate, live
// over SSE like the rest of the dashboard.
export function Factory() {
  const { snapshot, state, error } = useLiveSnapshot()

  if (!snapshot) {
    return (
      <PageShell
        eyebrow="factotum"
        title="Factory"
        state={state}
        back={{ href: "/", label: "dashboard" }}
      >
        {error ? (
          <RefreshBanner error={error} subject="factory" />
        ) : (
          <p className="text-sm text-muted-foreground">Loading factory…</p>
        )}
      </PageShell>
    )
  }

  const gated = snapshot.pipelines.filter((pipeline) => pipeline.gate).length

  return (
    <PageShell
      eyebrow={snapshot.project || "factotum"}
      title="Factory"
      state={state}
      back={{ href: "/", label: "dashboard" }}
    >
      {error ? <RefreshBanner error={error} stale subject="factory" /> : null}

      <section className="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <Stat label="Pipelines" value={snapshot.pipelines.length} />
        <Stat label="Open gates" value={gated} />
      </section>

      {snapshot.pipelines.length === 0 ? (
        <p className="text-sm text-muted-foreground">No pipelines yet.</p>
      ) : null}

      <section className="flex flex-col gap-3">
        {snapshot.pipelines.map((pipeline) => (
          <PipelineCard key={pipeline.id} pipeline={pipeline} />
        ))}
      </section>
    </PageShell>
  )
}
