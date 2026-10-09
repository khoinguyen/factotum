import { AppLink } from "@/components/app-link"
import { Chip } from "@/components/chip"
import { PageShell } from "@/components/page-shell"
import { RefreshBanner } from "@/components/refresh-banner"
import { useLiveResource } from "@/hooks/use-live-resource"
import type { IdeaView } from "@/lib/api"

export function IdeaDetail({ id }: { id: string }) {
  const { data, state, status, error } = useLiveResource<IdeaView>(
    `/api/idea/${id}`,
  )
  const idea = data
    ? { ...data, tasks: data.tasks ?? [], artifacts: data.artifacts ?? [] }
    : null

  if (!idea) {
    return (
      <PageShell
        title="Idea"
        state={state}
        back={{ href: "/", label: "dashboard" }}
      >
        {status === 404 ? (
          <p className="text-sm text-muted-foreground">No such idea.</p>
        ) : error ? (
          <RefreshBanner error={error} subject="idea" />
        ) : (
          <p className="text-sm text-muted-foreground">Loading idea…</p>
        )}
      </PageShell>
    )
  }

  return (
    <PageShell
      eyebrow={`${idea.kind} · ${idea.state}`}
      title={idea.title}
      state={state}
      back={{ href: "/", label: "dashboard" }}
    >
      {error ? <RefreshBanner error={error} stale subject="idea" /> : null}

      <dl className="grid max-w-2xl grid-cols-[8rem_1fr] gap-x-4 gap-y-2 text-sm">
        <dt className="text-muted-foreground">Status</dt>
        <dd>
          <Chip label={idea.state} toneKey={idea.state} />
        </dd>
        <dt className="text-muted-foreground">Tasks</dt>
        <dd>
          {idea.total} · {idea.done} done · {idea.active} active ·{" "}
          {idea.blocked} blocked
        </dd>
        {idea.repo ? (
          <>
            <dt className="text-muted-foreground">Repo</dt>
            <dd className="break-words">{idea.repo}</dd>
          </>
        ) : null}
      </dl>

      {idea.description ? (
        <p className="max-w-prose whitespace-pre-wrap break-words text-sm">
          {idea.description}
        </p>
      ) : null}

      <section className="flex flex-col gap-2">
        <h2 className="text-lg font-semibold">
          Promoted tasks · {idea.tasks.length}
        </h2>
        {idea.tasks.length === 0 ? (
          <p className="text-sm text-muted-foreground">Not promoted yet.</p>
        ) : (
          idea.tasks.map((task) => (
            <AppLink
              key={task.id}
              href={task.url}
              className="flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5 hover:bg-accent"
            >
              <Chip label={task.chip} toneKey={task.class} />
              <span className="font-mono text-xs text-muted-foreground">
                {task.id}
              </span>
              <span className="truncate text-sm">{task.title}</span>
            </AppLink>
          ))
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h2 className="text-lg font-semibold">
          Artifacts · {idea.artifacts.length}
        </h2>
        {idea.artifacts.length === 0 ? (
          <p className="text-sm text-muted-foreground">No artifacts attached.</p>
        ) : (
          idea.artifacts.map((artifact) => (
            <AppLink
              key={artifact.id}
              href={artifact.url}
              className="flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5 hover:bg-accent"
            >
              <Chip label={artifact.kind} toneKey="capture" />
              <span className="font-mono text-xs text-muted-foreground">
                {artifact.id}
              </span>
              <span className="truncate text-sm">{artifact.title}</span>
            </AppLink>
          ))
        )}
      </section>
    </PageShell>
  )
}
