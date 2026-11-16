import { useEffect, useState } from "react"

export type LiveState = "connecting" | "live" | "reconnecting" | "unsupported"

export type LiveResource<T> = {
  data: T | null
  state: LiveState
  status: number | null
  error: string | null
}

// useLiveResource fetches a read-side JSON document once, then refetches it
// whenever /events reports a mutation. The event stream carries no payload, so
// the document stays the single source of truth and cannot drift. A non-zero
// pollMs also refetches on that interval: a document whose contents are derived
// from elapsed time (session staleness) changes with no mutation to announce, so
// event-driven refresh alone would leave an open tab showing a stale answer.
// state tracks the SSE stream itself — live while it is open, reconnecting when
// it drops — so the badge never lies about the connection because a read failed.
// status holds the last HTTP status so a caller can tell a missing document
// (404) from a transient error. error carries the last failure so a caller can
// keep the last good data and show a banner instead of blanking the page.
export function useLiveResource<T>(path: string, pollMs = 0): LiveResource<T> {
  const [data, setData] = useState<T | null>(null)
  const [state, setState] = useState<LiveState>("connecting")
  const [status, setStatus] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false

    // refresh reads the document. It never touches state: the live badge is
    // about the stream, so a failed read shows as status/error, not as a
    // connection that is down.
    async function refresh() {
      try {
        const res = await fetch(path, { cache: "no-store" })
        if (cancelled) return
        setStatus(res.status)
        if (!res.ok) {
          setError(await errorText(res))
          return
        }
        const next = (await res.json()) as T
        if (cancelled) return
        setData(next)
        setError(null)
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "network error")
        }
      }
    }

    const timer = pollMs > 0 ? setInterval(() => void refresh(), pollMs) : null
    function stop() {
      cancelled = true
      if (timer) clearInterval(timer)
    }

    if (typeof EventSource === "undefined") {
      setState("unsupported")
      void refresh()
      return stop
    }

    const source = new EventSource("/events")
    source.addEventListener("open", () => {
      setState("live")
      void refresh()
    })
    source.addEventListener("update", () => void refresh())
    source.addEventListener("error", () => setState("reconnecting"))
    void refresh()

    return () => {
      stop()
      source.close()
    }
  }, [path, pollMs])

  return { data, state, status, error }
}

// errorText extracts the server's JSON error message, falling back to the status
// when the body is not the expected shape.
async function errorText(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string }
    if (body.error) return body.error
  } catch {
    // not a JSON body; fall through to the status
  }
  return `request failed (${res.status})`
}
