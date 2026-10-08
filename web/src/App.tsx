import { PageShell } from "@/components/page-shell"
import { ArtifactDetail } from "@/pages/artifact-detail"
import { Capture } from "@/pages/capture"
import { Dashboard } from "@/pages/dashboard"
import { IdeaDetail } from "@/pages/idea-detail"
import { TaskDetail } from "@/pages/task-detail"
import { resolveRoute, usePath } from "@/lib/router"

export default function App() {
  const path = usePath()
  const route = resolveRoute(path)

  switch (route.kind) {
    case "capture":
      return <Capture />
    case "idea":
      return <IdeaDetail id={route.id} />
    case "task":
      return <TaskDetail id={route.id} />
    case "memory":
      return <ArtifactDetail id={route.id} kind="memory" />
    case "doc":
      return <ArtifactDetail id={route.id} kind="doc" />
    case "not-found":
      return (
        <PageShell title="Not found" state="live" back={{ href: "/", label: "dashboard" }}>
          <p className="text-sm text-muted-foreground">
            No page at <span className="font-mono">{route.path}</span>.
          </p>
        </PageShell>
      )
    default:
      return <Dashboard />
  }
}
