import { AppLink } from "@/components/app-link"
import { PageShell } from "@/components/page-shell"
import { useLiveResource } from "@/hooks/use-live-resource"
import type { ArtifactDetail as ArtifactDetailDoc } from "@/lib/api"

export function ArtifactDetail({
  id,
  kind,
}: {
  id: string
  kind: "memory" | "doc"
}) {
  const { data, state, status } = useLiveResource<ArtifactDetailDoc>(
    `/api/${kind}/${id}`,
  )

  if (!data) {
    return (
      <PageShell
        title={kind === "memory" ? "Memory" : "Document"}
        state={state}
        back={{ href: "/", label: "dashboard" }}
      >
        <p className="text-sm text-muted-foreground">
          {status === 404
            ? `No such ${kind}.`
            : `Loading ${kind}…`}
        </p>
      </PageShell>
    )
  }

  return (
    <PageShell
      eyebrow={`${data.kind}`}
      title={data.title}
      state={state}
      back={{ href: "/", label: "dashboard" }}
    >
      <dl className="grid max-w-2xl grid-cols-[8rem_1fr] gap-x-4 gap-y-2 text-sm">
        <dt className="text-muted-foreground">Kind</dt>
        <dd>{data.kind}</dd>
        {data.attached_to ? (
          <>
            <dt className="text-muted-foreground">Task</dt>
            <dd className="min-w-0">
              <AppLink href={data.attached_to.url} className="hover:underline">
                <span className="font-mono text-xs text-muted-foreground">
                  {data.attached_to.id}
                </span>{" "}
                {data.attached_to.title}
              </AppLink>
            </dd>
          </>
        ) : null}
        <dt className="text-muted-foreground">Created</dt>
        <dd>{data.created_at}</dd>
        <dt className="text-muted-foreground">Updated</dt>
        <dd>{data.updated_at}</dd>
      </dl>

      {data.brief ? (
        <p className="max-w-prose break-words text-sm italic text-muted-foreground">
          {data.brief}
        </p>
      ) : null}
      {data.body ? (
        <p className="max-w-prose whitespace-pre-wrap break-words text-sm">
          {data.body}
        </p>
      ) : null}

      {data.links.length > 0 ? (
        <section className="flex flex-col gap-2">
          <h2 className="text-lg font-semibold">Links</h2>
          <ul className="list-disc pl-6 text-sm">
            {data.links.map((link, i) => (
              <li key={i}>
                <AppLink href={link.url} className="hover:underline">
                  [{link.kind}]
                  {link.title ? ` ${link.title}` : ""}
                </AppLink>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </PageShell>
  )
}
