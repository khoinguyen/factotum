import type * as React from "react"

import { AppLink } from "@/components/app-link"
import { LiveBadge } from "@/components/live-badge"
import { ThemeToggle } from "@/components/theme-toggle"
import type { LiveState } from "@/hooks/use-live-resource"

export function PageShell({
  eyebrow,
  title,
  state,
  back,
  capture = true,
  footer,
  children,
}: {
  eyebrow?: string
  title: string
  state?: LiveState
  back?: { href: string; label: string }
  capture?: boolean
  footer?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="mx-auto flex min-h-svh max-w-6xl flex-col gap-6 p-6">
      {back ? (
        <AppLink
          href={back.href}
          className="text-sm text-muted-foreground hover:underline"
        >
          ← {back.label}
        </AppLink>
      ) : null}
      <header className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          {eyebrow ? (
            <p className="text-xs uppercase tracking-wide text-muted-foreground">
              {eyebrow}
            </p>
          ) : null}
          <h1 className="break-words text-xl font-semibold">{title}</h1>
        </div>
        <div className="flex shrink-0 items-center gap-4">
          {capture ? (
            <AppLink
              href="/capture"
              className="text-sm text-muted-foreground hover:underline"
            >
              Capture an idea →
            </AppLink>
          ) : null}
          {state ? <LiveBadge state={state} /> : null}
          <ThemeToggle />
        </div>
      </header>
      {children}
      <footer className="mt-auto text-xs text-muted-foreground">
        {footer ?? "Read-only · live via SSE"}
      </footer>
    </div>
  )
}
