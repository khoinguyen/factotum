import { normalizeSnapshot, type Snapshot } from "@/lib/api"
import { useLiveResource, type LiveState } from "@/hooks/use-live-resource"

export type { LiveState }

// useLiveSnapshot keeps the dashboard in sync with the server: it fetches the
// JSON snapshot once, then refetches whenever /events reports a mutation. error
// carries the last refresh failure so the dashboard can keep the last good
// snapshot and show a banner rather than blanking the page.
export function useLiveSnapshot() {
  const { data, state, error } = useLiveResource<Snapshot>("/api/snapshot")
  return { snapshot: data ? normalizeSnapshot(data) : null, state, error }
}
