import { normalizeSessions, type SessionsDoc } from "@/lib/api"
import { useLiveResource, type LiveState } from "@/hooks/use-live-resource"

export type { LiveState }

// useLiveSessions keeps the sessions page in sync with the server: it fetches
// /api/sessions once, then refetches whenever /events reports a mutation. error
// carries the last refresh failure so the page can keep the last good document
// and show a banner rather than blanking.
export function useLiveSessions() {
  const { data, state, error } = useLiveResource<SessionsDoc>("/api/sessions")
  return {
    sessions: data ? normalizeSessions(data).sessions : null,
    state,
    error,
  }
}
