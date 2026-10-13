import type * as React from "react"

import { AppLink } from "@/components/app-link"
import { CaptureEntry } from "@/components/capture-entry"
import { LiveBadge } from "@/components/live-badge"
import { ThemeToggle } from "@/components/theme-toggle"
import type { LiveState } from "@/hooks/use-live-resource"
import { cn } from "@/lib/utils"

export function PageShell({
  eyebrow,
  title,
  titleAction,
  state,
  back,
  capture = true,
  footer,
  children,
}: {
  eyebrow?: string
  title: string
  titleAction?: React.ReactNode
  state?: LiveState
  back?: { href: string; label: string }
  capture?: boolean
  footer?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div
      className={cn(
        "mx-auto flex min-h-svh max-w-6xl flex-col gap-6 p-6",
        capture && "pb-24 sm:pb-6",
      )}
    >
      {back ? (
        <AppLink
          href={back.href}
          className="text-sm text-muted-foreground hover:underline"
        >
          ← {back.label}
        </AppLink>
      ) : null}
      <header className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between sm:gap-4">
        <div className="min-w-0">
          {eyebrow ? (
            <p className="text-xs uppercase tracking-wide text-muted-foreground">
              {eyebrow}
            </p>
          ) : null}
          <div className="flex items-center gap-1">
            <h1 className="min-w-0 break-words text-xl font-semibold">{title}</h1>
            {titleAction}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 sm:shrink-0">
          {state ? <LiveBadge state={state} /> : null}
          <ThemeToggle />
        </div>
      </header>
      {children}
      <footer className="mt-auto text-xs text-muted-foreground">
        {footer ?? "Read-only · live via SSE"}
      </footer>
      {capture ? <CaptureEntry /> : null}
    </div>
  )
}
