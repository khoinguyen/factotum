import type * as React from "react"

import { navigate } from "@/lib/router"
import { cn } from "@/lib/utils"

// AppLink is an internal link that routes client-side. External links and
// modified clicks (new tab, download) keep the browser's default behavior.
export function AppLink({
  href,
  className,
  children,
  onClick,
  ...props
}: React.ComponentProps<"a">) {
  const internal = href?.startsWith("/") ?? false
  return (
    <a
      {...props}
      href={href}
      className={cn(className)}
      onClick={(event) => {
        onClick?.(event)
        if (
          !internal ||
          !href ||
          event.defaultPrevented ||
          event.metaKey ||
          event.ctrlKey ||
          event.shiftKey ||
          event.altKey ||
          event.button !== 0
        ) {
          return
        }
        event.preventDefault()
        navigate(href)
      }}
    >
      {children}
    </a>
  )
}
