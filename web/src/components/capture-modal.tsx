import { useEffect, useState, type FormEvent } from "react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { CaptureError, fetchCaptureConfig, submitPrompt } from "@/lib/api"
import {
  clearCaptureToken,
  loadCaptureToken,
  saveCaptureToken,
} from "@/lib/capture-token"
import { navigate } from "@/lib/router"

// Phase is the modal's step. It loads the config, then either reports capture is
// off, asks for the token, or shows the prompt box once the token is valid.
type Phase = "loading" | "disabled" | "token" | "prompt"

// CaptureModal is the token-gated "prompt the chief" box. It validates the
// shared capture token before it shows the form, remembers a good one in
// browser storage, and re-asks when the server rejects the remembered one, so
// the token is entered once per browser rather than once per prompt.
export function CaptureModal({ onClose }: { onClose: () => void }) {
  const [phase, setPhase] = useState<Phase>("loading")
  const [token, setToken] = useState(loadCaptureToken)
  const [tokenError, setTokenError] = useState("")
  const [tokenBusy, setTokenBusy] = useState(false)
  const [prompt, setPrompt] = useState("")
  const [promptError, setPromptError] = useState("")
  const [promptBusy, setPromptBusy] = useState(false)

  // On open, validate any remembered token. A valid one skips straight to the
  // prompt box; a rejected one is forgotten so the token step returns. The read
  // also reports whether capture is enabled at all.
  useEffect(() => {
    let cancelled = false
    const remembered = loadCaptureToken()
    fetchCaptureConfig(remembered || undefined)
      .then((config) => {
        if (cancelled) return
        if (!config.enabled) {
          setPhase("disabled")
          return
        }
        if (remembered && config.authorized) {
          setToken(remembered)
          setPhase("prompt")
          return
        }
        if (remembered) clearCaptureToken()
        setPhase("token")
      })
      .catch(() => {
        if (!cancelled) setPhase("disabled")
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onClose])

  async function onSubmitToken(event: FormEvent) {
    event.preventDefault()
    if (tokenBusy) return
    const candidate = token.trim()
    if (candidate === "") {
      setTokenError("Enter the capture token.")
      return
    }
    setTokenBusy(true)
    setTokenError("")
    try {
      const config = await fetchCaptureConfig(candidate)
      if (config.enabled && config.authorized) {
        saveCaptureToken(candidate)
        setToken(candidate)
        setPhase("prompt")
      } else {
        setTokenError("That token was rejected.")
      }
    } catch (err) {
      setTokenError(err instanceof Error ? err.message : "Could not check the token.")
    } finally {
      setTokenBusy(false)
    }
  }

  async function onSubmitPrompt(event: FormEvent) {
    event.preventDefault()
    if (promptBusy) return
    if (prompt.trim() === "") {
      setPromptError("Type a prompt.")
      return
    }
    setPromptBusy(true)
    setPromptError("")
    try {
      const result = await submitPrompt(prompt, token)
      onClose()
      navigate(result.url)
    } catch (err) {
      // A rejected token means the remembered one went stale: forget it and
      // return to the token step rather than let the user retype into a form
      // that cannot land.
      if (err instanceof CaptureError && err.status === 401) {
        clearCaptureToken()
        setToken("")
        setTokenError("That token was rejected.")
        setPhase("token")
        return
      }
      setPromptError(err instanceof Error ? err.message : "Prompt failed.")
      setPromptBusy(false)
    }
  }

  return (
    <div
      role="presentation"
      onClick={onClose}
      className="fixed inset-0 z-50 flex items-end justify-center bg-black/40 p-4 sm:items-center"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="capture-modal-title"
        onClick={(event) => event.stopPropagation()}
        className="w-full max-w-lg rounded-lg border bg-card p-6 text-card-foreground shadow-lg"
      >
        <div className="mb-4 flex items-start justify-between gap-4">
          <h2 id="capture-modal-title" className="text-lg font-semibold">
            Prompt the chief
          </h2>
          <button
            type="button"
            aria-label="Close"
            onClick={onClose}
            className="text-xl leading-none text-muted-foreground hover:text-foreground"
          >
            ×
          </button>
        </div>

        {phase === "loading" ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : phase === "disabled" ? (
          <p className="max-w-prose text-sm text-muted-foreground">
            Capture is not configured. Set <code>serve.token</code> in the machine
            config or <code>FACTOTUM_SERVE_TOKEN</code> to enable it.
          </p>
        ) : phase === "token" ? (
          <form className="flex flex-col gap-4" onSubmit={onSubmitToken}>
            <p className="text-sm text-muted-foreground">
              Enter the capture token to unlock the prompt box. It is remembered
              on this browser.
            </p>
            <div className="flex flex-col gap-2">
              <label htmlFor="capture-modal-token" className="text-sm font-medium">
                Capture token
              </label>
              <Input
                id="capture-modal-token"
                type="password"
                autoComplete="current-password"
                autoFocus
                required
                value={token}
                onChange={(event) => setToken(event.target.value)}
              />
            </div>
            {tokenError ? (
              <p role="alert" className="break-words text-sm text-red-600 dark:text-red-400">
                {tokenError}
              </p>
            ) : null}
            <Button type="submit" disabled={tokenBusy} className="self-start">
              Continue
            </Button>
          </form>
        ) : (
          <form className="flex flex-col gap-4" onSubmit={onSubmitPrompt}>
            <div className="flex flex-col gap-2">
              <label htmlFor="capture-modal-prompt" className="text-sm font-medium">
                Prompt
              </label>
              <Textarea
                id="capture-modal-prompt"
                rows={5}
                autoFocus
                value={prompt}
                onChange={(event) => setPrompt(event.target.value)}
                placeholder="Describe what you want, in your own words."
              />
            </div>
            {promptError ? (
              <p role="alert" className="break-words text-sm text-red-600 dark:text-red-400">
                {promptError}
              </p>
            ) : null}
            <Button type="submit" disabled={promptBusy} className="self-start">
              Send prompt
            </Button>
            <p className="text-xs text-muted-foreground">
              Stored as a document and the chief is notified.
            </p>
          </form>
        )}
      </div>
    </div>
  )
}
