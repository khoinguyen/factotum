import { useEffect, useState, type FormEvent, type ReactNode } from "react"

import { AppLink } from "@/components/app-link"
import { PageShell } from "@/components/page-shell"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { fetchCaptureConfig, submitCapture, type CaptureKind } from "@/lib/api"
import { navigate } from "@/lib/router"
import { cn } from "@/lib/utils"

// tokenKey is where the browser remembers the shared capture token, so a phone
// captures without retyping it. It mirrors the retired server-rendered page.
const tokenKey = "factotum.capture.token"

function loadToken(): string {
  try {
    return window.localStorage.getItem(tokenKey) ?? ""
  } catch {
    return ""
  }
}

function saveToken(token: string): void {
  try {
    window.localStorage.setItem(tokenKey, token)
  } catch {
    // a private-mode browser may refuse storage; the write still works.
  }
}

// Capture is the write side of the app: a natural-language sentence posted to
// the token-gated /api/capture endpoint. The raw sentence is stored immediately
// and refined later, so the form returns as soon as it lands.
export function Capture() {
  const [enabled, setEnabled] = useState<boolean | null>(null)
  const [kind, setKind] = useState<CaptureKind>("idea")
  const [text, setText] = useState("")
  const [token, setToken] = useState(loadToken)
  const [error, setError] = useState("")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    fetchCaptureConfig()
      .then((config) => {
        if (!cancelled) setEnabled(config.enabled)
      })
      .catch(() => {
        if (!cancelled) setEnabled(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  async function onSubmit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    if (text.trim() === "") {
      setError("Type something to capture.")
      return
    }
    setBusy(true)
    setError("")
    saveToken(token)
    try {
      const result = await submitCapture(text, kind, token)
      navigate(result.url)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Capture failed.")
      setBusy(false)
    }
  }

  const noun = kind === "bug" ? "bug" : "idea"

  return (
    <PageShell
      eyebrow="capture"
      title="Capture"
      back={{ href: "/", label: "dashboard" }}
      capture={false}
      footer="Stored immediately · refined later"
    >
      {enabled === null ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : !enabled ? (
        <p className="max-w-prose text-sm text-muted-foreground">
          Capture is not configured. Set <code>serve.token</code> in the machine
          config or <code>FACTOTUM_SERVE_TOKEN</code> to enable it.
        </p>
      ) : (
        <form className="flex max-w-prose flex-col gap-4" onSubmit={onSubmit}>
          <div className="flex gap-2">
            <KindButton
              active={kind === "idea"}
              onClick={() => setKind("idea")}
            >
              Idea
            </KindButton>
            <KindButton active={kind === "bug"} onClick={() => setKind("bug")}>
              Bug
            </KindButton>
          </div>

          <div className="flex flex-col gap-2">
            <label htmlFor="capture-text" className="text-sm font-medium">
              What is it?
            </label>
            <Textarea
              id="capture-text"
              rows={5}
              required
              autoFocus
              value={text}
              onChange={(event) => setText(event.target.value)}
              placeholder="One sentence is enough."
            />
          </div>

          <div className="flex flex-col gap-2">
            <label htmlFor="capture-token" className="text-sm font-medium">
              Capture token
            </label>
            <Input
              id="capture-token"
              type="password"
              autoComplete="current-password"
              required
              value={token}
              onChange={(event) => setToken(event.target.value)}
            />
          </div>

          {error ? (
            <p role="alert" className="break-words text-sm text-red-600 dark:text-red-400">
              {error}
            </p>
          ) : null}

          <Button type="submit" disabled={busy} className="self-start">
            Capture
          </Button>

          <p className="text-xs text-muted-foreground">
            Stored as a {noun} immediately; refined later.
          </p>
        </form>
      )}

      <AppLink
        href="/"
        className="text-sm text-muted-foreground hover:underline"
      >
        ← Back to dashboard
      </AppLink>
    </PageShell>
  )
}

function KindButton({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <Button
      type="button"
      variant={active ? "default" : "outline"}
      size="sm"
      aria-pressed={active}
      onClick={onClick}
      className={cn(active ? "" : "text-muted-foreground")}
    >
      {children}
    </Button>
  )
}
