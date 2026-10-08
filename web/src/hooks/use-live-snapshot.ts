import { normalizeSnapshot, type Snapshot } from "@/lib/api"
import { useLiveResource, type LiveState } from "@/hooks/use-live-resource"

export type { LiveState }

// useLiveSnapshot keeps the dashboard in sync with the server: it fetches the
// JSON snapshot once, then refetches whenever /events reports a mutation.
export function useLiveSnapshot() {
  const { data, state } = useLiveResource<Snapshot>("/api/snapshot")
  return { snapshot: data ? normalizeSnapshot(data) : null, state }
}
