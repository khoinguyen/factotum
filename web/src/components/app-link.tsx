import type * as React from "react"

import { isClientRoute, navigate } from "@/lib/router"
import { cn } from "@/lib/utils"

// AppLink is a link the client router owns. Anything the router does not own
// (/capture, /app/*, external URLs) and modified clicks (new tab, download)
// keep the browser's default behavior.
export function AppLink({
  href,
  className,
  children,
  onClick,
  ...props
}: React.ComponentProps<"a">) {
  const internal = href ? isClientRoute(href) : false
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
