import { useEffect, useState } from "react"

export type LiveState = "connecting" | "live" | "reconnecting" | "unsupported"

export type LiveResource<T> = {
  data: T | null
  state: LiveState
  status: number | null
}

// useLiveResource fetches a read-side JSON document once, then refetches it
// whenever /events reports a mutation. The event stream carries no payload, so
// the document stays the single source of truth and cannot drift. status holds
// the last HTTP status so a caller can tell a missing document (404) from a
// transient error.
export function useLiveResource<T>(path: string): LiveResource<T> {
  const [data, setData] = useState<T | null>(null)
  const [state, setState] = useState<LiveState>("connecting")
  const [status, setStatus] = useState<number | null>(null)

  useEffect(() => {
    let cancelled = false

    async function refresh() {
      try {
        const res = await fetch(path, { cache: "no-store" })
        if (cancelled) return
        setStatus(res.status)
        if (!res.ok) {
          setState("reconnecting")
          return
        }
        const next = (await res.json()) as T
        if (cancelled) return
        setData(next)
        setState("live")
      } catch {
        if (!cancelled) setState("reconnecting")
      }
    }

    if (typeof EventSource === "undefined") {
      setState("unsupported")
      void refresh()
      return () => {
        cancelled = true
      }
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
  }, [path])

  return { data, state, status }
}
