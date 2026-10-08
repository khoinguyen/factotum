import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

// tone maps a read-side state/class key to a badge color. Keys come from both
// the idea rollup (finished/active/blocked/captured) and the task classifier
// (ready-agent, ready-human, done, review, blocked, cycle, capture, ...).
const tone: Record<string, string> = {
  done: "border-emerald-600/40 bg-emerald-500/15 text-emerald-700 dark:text-emerald-300",
  finished: "border-emerald-600/40 bg-emerald-500/15 text-emerald-700 dark:text-emerald-300",
  "ready-agent": "border-emerald-600/40 bg-emerald-500/15 text-emerald-700 dark:text-emerald-300",
  "ready-human": "border-sky-600/40 bg-sky-500/15 text-sky-700 dark:text-sky-300",
  active: "border-sky-600/40 bg-sky-500/15 text-sky-700 dark:text-sky-300",
  review: "border-violet-600/40 bg-violet-500/15 text-violet-700 dark:text-violet-300",
  blocked: "border-red-600/40 bg-red-500/15 text-red-700 dark:text-red-300",
  cycle: "border-red-600/40 bg-red-500/15 text-red-700 dark:text-red-300",
  capture: "border-amber-600/40 bg-amber-500/15 text-amber-700 dark:text-amber-300",
  captured: "border-amber-600/40 bg-amber-500/15 text-amber-700 dark:text-amber-300",
  cancelled: "border-muted-foreground/30 bg-muted text-muted-foreground",
  waiting: "border-muted-foreground/30 bg-muted text-muted-foreground",
}

export function Chip({
  label,
  toneKey,
  className,
}: {
  label: string
  toneKey?: string
  className?: string
}) {
  return (
    <Badge
      variant="outline"
      className={cn(tone[toneKey ?? ""] ?? tone.waiting, className)}
    >
      {label}
    </Badge>
  )
}
