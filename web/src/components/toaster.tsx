import { useEffect, useRef, useState } from "react"

import { AppLink } from "@/components/app-link"
import { useLiveResource } from "@/hooks/use-live-resource"
import {
  normalizeNotifications,
  type NotificationsDoc,
  type NotificationView,
} from "@/lib/api"

// seenKey is where the toaster remembers the groomings it has already shown, so
// a reload does not replay the whole history of completed prompts.
const seenKey = "factotum.grooming-seen"

function loadSeen(): Set<string> {
  try {
    const raw = window.localStorage.getItem(seenKey)
    if (!raw) return new Set()
    const parsed: unknown = JSON.parse(raw)
    return new Set(Array.isArray(parsed) ? (parsed as string[]) : [])
  } catch {
    return new Set()
  }
}

function saveSeen(seen: Set<string>): void {
  try {
    window.localStorage.setItem(seenKey, JSON.stringify([...seen]))
  } catch {
    // Storage is best-effort: a full or blocked store only means a replay.
  }
}

// Toaster announces completed prompt groomings. It follows /api/notifications
// live, toasts each grooming it has not shown yet, and clicking a toast always
// opens the prompt artifact page — one UI, never a branch on the capture count.
// It seeds what is already complete on first load, so opening the app never
// replays a backlog.
export function Toaster() {
  const { data } = useLiveResource<NotificationsDoc>("/api/notifications")
  const [toasts, setToasts] = useState<NotificationView[]>([])
  const seen = useRef<Set<string> | null>(null)

  useEffect(() => {
    if (!data) return
    const list = normalizeNotifications(data).notifications
    if (seen.current === null) {
      const seeded = loadSeen()
      for (const note of list) seeded.add(note.id)
      seen.current = seeded
      saveSeen(seeded)
      return
    }
    const fresh = list.filter((note) => !seen.current!.has(note.id))
    if (fresh.length === 0) return
    for (const note of fresh) seen.current!.add(note.id)
    saveSeen(seen.current!)
    setToasts((prev) => [...prev, ...fresh])
  }, [data])

  function dismiss(id: string) {
    setToasts((prev) => prev.filter((note) => note.id !== id))
  }

  if (toasts.length === 0) return null
  return (
    <div
      role="status"
      aria-live="polite"
      className="fixed bottom-4 right-4 z-50 flex w-80 max-w-[calc(100vw-2rem)] flex-col gap-2"
    >
      {toasts.map((note) => (
        <div
          key={note.id}
          className="flex items-start gap-2 rounded-md border bg-card p-3 text-card-foreground shadow-lg"
        >
          <div className="flex min-w-0 flex-col gap-1">
            <span className="text-xs font-medium text-muted-foreground">
              Grooming complete
            </span>
            <AppLink
              href={note.url}
              className="truncate font-medium hover:underline"
              onClick={() => dismiss(note.id)}
            >
              {note.title}
            </AppLink>
            <span className="text-xs text-muted-foreground">
              {note.captures === 1 ? "1 capture" : `${note.captures} captures`} →
            </span>
          </div>
          <button
            type="button"
            aria-label="Dismiss"
            className="ml-auto shrink-0 text-muted-foreground hover:text-foreground"
            onClick={() => dismiss(note.id)}
          >
            ×
          </button>
        </div>
      ))}
    </div>
  )
}
