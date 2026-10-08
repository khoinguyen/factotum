import { useEffect, useState } from "react"

import { fetchSnapshot, type Snapshot } from "@/lib/api"

export type LiveState = "connecting" | "live" | "reconnecting" | "unsupported"

// useLiveSnapshot keeps the dashboard in sync with the server: it fetches the
// JSON snapshot once, then refetches whenever /events reports a mutation. The
// event stream carries no payload, so the snapshot stays the single source of
// truth and cannot drift from the rendered pages.
export function useLiveSnapshot() {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [state, setState] = useState<LiveState>("connecting")

  useEffect(() => {
    let cancelled = false

    async function refresh() {
      try {
        const next = await fetchSnapshot()
        if (cancelled) return
        setSnapshot(next)
        setState("live")
      } catch {
        if (!cancelled) setState("reconnecting")
      }
    }

    if (typeof EventSource === "undefined") {
      setState("unsupported")
      void refresh()
      return
    }

    const source = new EventSource("/events")
    source.addEventListener("open", () => void refresh())
    source.addEventListener("update", () => void refresh())
    source.addEventListener("error", () => setState("reconnecting"))
    void refresh()

    return () => {
      cancelled = true
      source.close()
    }
  }, [])

  return { snapshot, state }
}
