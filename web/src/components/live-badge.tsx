import type { LiveState } from "@/hooks/use-live-resource"
import { cn } from "@/lib/utils"

const label: Record<LiveState, string> = {
  connecting: "connecting…",
  live: "live",
  reconnecting: "reconnecting…",
  unsupported: "no live updates",
}

const dot: Record<LiveState, string> = {
  connecting: "bg-muted-foreground",
  live: "bg-emerald-500",
  reconnecting: "bg-amber-500",
  unsupported: "bg-muted-foreground",
}

export function LiveBadge({ state }: { state: LiveState }) {
  return (
    <span className="flex items-center gap-2 text-sm text-muted-foreground">
      <span className={cn("size-2 rounded-full", dot[state])} />
      {label[state]}
    </span>
  )
}
