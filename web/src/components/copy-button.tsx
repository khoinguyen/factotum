import { useRef, useState } from "react"
import { Check, Copy } from "lucide-react"

import { Button } from "@/components/ui/button"

// CopyButton copies value to the clipboard and briefly confirms with a check.
// label is the accessible name and tooltip, so a caller says what is copied
// (the capture id, the id and title together, ...).
export function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<number | undefined>(undefined)

  async function onCopy() {
    try {
      await navigator.clipboard.writeText(value)
    } catch {
      // A browser without clipboard access leaves the control unchanged.
      return
    }
    setCopied(true)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setCopied(false), 1500)
  }

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      aria-label={label}
      title={copied ? "Copied" : label}
      onClick={onCopy}
      className="size-6 text-muted-foreground"
    >
      {copied ? <Check /> : <Copy />}
    </Button>
  )
}
