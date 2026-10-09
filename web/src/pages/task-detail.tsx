import { AppLink } from "@/components/app-link"
import { Chip } from "@/components/chip"
import { PageShell } from "@/components/page-shell"
import { useLiveResource } from "@/hooks/use-live-resource"
import type { TaskDetail as TaskDetailDoc, TaskLink } from "@/lib/api"

function TaskRefs({ title, tasks, empty }: { title: string; tasks: TaskLink[]; empty: string }) {
  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-lg font-semibold">
        {title} · {tasks.length}
      </h2>
      {tasks.length === 0 ? (
        <p className="text-sm text-muted-foreground">{empty}</p>
      ) : (
        tasks.map((task) => (
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
  )
}

export function TaskDetail({ id }: { id: string }) {
  const { data, state, status } = useLiveResource<TaskDetailDoc>(`/api/task/${id}`)

  if (!data) {
    return (
      <PageShell
        title="Task"
        state={state}
        back={{ href: "/", label: "dashboard" }}
      >
        <p className="text-sm text-muted-foreground">
          {status === 404 ? "No such task." : "Loading task…"}
        </p>
      </PageShell>
    )
  }

  return (
    <PageShell
      eyebrow={`${data.kind} · ${data.chip}`}
      title={data.title}
      state={state}
      back={{ href: "/", label: "dashboard" }}
    >
      <dl className="grid max-w-2xl grid-cols-[8rem_1fr] gap-x-4 gap-y-2 text-sm">
        <dt className="text-muted-foreground">Status</dt>
        <dd>
          <Chip label={data.chip} toneKey={data.class} />
        </dd>
        {data.origin ? (
          <>
            <dt className="text-muted-foreground">Origin capture</dt>
            <dd className="min-w-0">
              <AppLink href={data.origin.url} className="hover:underline">
                <span className="font-mono text-xs text-muted-foreground">
                  {data.origin.id}
                </span>{" "}
                {data.origin.title}
              </AppLink>
            </dd>
          </>
        ) : null}
        {data.repo ? (
          <>
            <dt className="text-muted-foreground">Repo</dt>
            <dd className="break-words">{data.repo}</dd>
          </>
        ) : null}
        {data.assignee ? (
          <>
            <dt className="text-muted-foreground">Assignee</dt>
            <dd>{data.assignee}</dd>
          </>
        ) : null}
        <dt className="text-muted-foreground">Groomed</dt>
        <dd>{String(data.groomed)}</dd>
        <dt className="text-muted-foreground">Created</dt>
        <dd>{data.created_at}</dd>
        <dt className="text-muted-foreground">Updated</dt>
        <dd>{data.updated_at}</dd>
      </dl>

      {data.description ? (
        <p className="max-w-prose whitespace-pre-wrap break-words text-sm">
          {data.description}
        </p>
      ) : null}

      {data.acceptance.length > 0 ? (
        <section className="flex flex-col gap-2">
          <h2 className="text-lg font-semibold">Acceptance criteria</h2>
          <ul className="list-disc pl-6 text-sm">
            {data.acceptance.map((criterion, i) => (
              <li key={i} className="break-words">
                {criterion}
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {data.grouped_under.length > 0 ? (
        <TaskRefs title="Grouped under" tasks={data.grouped_under} empty="" />
      ) : null}

      <TaskRefs title="Depends on" tasks={data.deps} empty="No dependencies." />
      <TaskRefs title="Blocks" tasks={data.dependents} empty="Nothing depends on this." />

      <section className="flex flex-col gap-2">
        <h2 className="text-lg font-semibold">Notes · {data.notes.length}</h2>
        {data.notes.length === 0 ? (
          <p className="text-sm text-muted-foreground">No notes.</p>
        ) : (
          data.notes.map((note, i) => (
            <div
              key={`${note.created_at}-${i}`}
              className="flex flex-col gap-1 rounded-md border px-3 py-2 text-sm"
            >
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Chip label={note.system ? "system" : note.author} toneKey="capture" />
                <time>{note.created_at}</time>
                {note.links.map((link, j) => (
                  <AppLink
                    key={j}
                    href={link.url}
                    className="hover:underline"
                  >
                    {link.kind}
                    {link.title ? ` · ${link.title}` : ""}
                  </AppLink>
                ))}
              </div>
              <p className="break-words whitespace-pre-wrap">{note.body}</p>
            </div>
          ))
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h2 className="text-lg font-semibold">
          Artifacts · {data.artifacts.length}
        </h2>
        {data.artifacts.length === 0 ? (
          <p className="text-sm text-muted-foreground">No artifacts attached.</p>
        ) : (
          data.artifacts.map((artifact) => (
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
