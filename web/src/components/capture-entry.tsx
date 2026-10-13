import { useState } from "react"

import { CaptureModal } from "@/components/capture-modal"

// CaptureEntry is the app's capture affordance, replacing the link to the
// /capture page: a floating action button on desktop and a sticky bottom bar on
// mobile, both opening the same token-gated prompt modal, so capturing never
// leaves the page you are on.
export function CaptureEntry() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="fixed inset-x-0 bottom-0 z-40 flex items-center justify-center gap-2 border-t border-primary/20 bg-primary px-4 py-3 text-sm font-medium text-primary-foreground shadow-lg outline-none hover:bg-primary/90 focus-visible:ring-[3px] focus-visible:ring-ring/50 sm:inset-x-auto sm:right-6 sm:bottom-6 sm:w-auto sm:rounded-full sm:border sm:px-5"
      >
        <span aria-hidden="true" className="text-lg leading-none">
          +
        </span>
        Capture an idea or bug
      </button>
      {open ? <CaptureModal onClose={() => setOpen(false)} /> : null}
    </>
  )
}
