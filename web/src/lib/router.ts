import { useEffect, useState } from "react"

export type Route =
  | { kind: "dashboard" }
  | { kind: "capture" }
  | { kind: "sessions" }
  | { kind: "factory" }
  | { kind: "idea"; id: string }
  | { kind: "task"; id: string }
  | { kind: "memory"; id: string }
  | { kind: "doc"; id: string }
  | { kind: "not-found"; path: string }

const clientPrefixes = ["/idea/", "/task/", "/memory/", "/doc/"]

// isClientRoute reports whether the client router owns path. Links to anything
// else (/app/*, external URLs) must be left to the browser, or the router renders
// its not-found page over a server-rendered page.
export function isClientRoute(path: string): boolean {
  if (path === "/capture" || path === "/sessions" || path === "/factory") return true
  return clientPrefixes.some((prefix) => path.startsWith(prefix))
}

// resolveRoute maps the current path to a read-side route. The server serves the
// app shell for every one of them; only the client knows how to render each.
export function resolveRoute(path: string): Route {
  const trimmed = path.replace(/\/+$/, "")
  if (trimmed === "" || trimmed === "/") {
    return { kind: "dashboard" }
  }
  if (trimmed === "/capture") {
    return { kind: "capture" }
  }
  if (trimmed === "/sessions") {
    return { kind: "sessions" }
  }
  if (trimmed === "/factory") {
    return { kind: "factory" }
  }
  const parts = trimmed.split("/").filter(Boolean)
  if (parts.length === 2 && isClientRoute(trimmed)) {
    const [kind, id] = parts
    if (kind === "idea" || kind === "task" || kind === "memory" || kind === "doc") {
      return { kind, id }
    }
  }
  return { kind: "not-found", path }
}

// withFrom appends a return target to a client route, so the page it opens can
// offer a back-link to where it was opened from. The target travels in ?from=.
export function withFrom(href: string, from: string): string {
  const separator = href.includes("?") ? "&" : "?"
  return `${href}${separator}from=${encodeURIComponent(from)}`
}

// backTarget derives a page's back-link from the ?from= return target, falling
// back to the dashboard when it is absent or names a route the client router
// does not own (an external URL, or a protocol-relative path).
export function backTarget(search: string): { href: string; label: string } {
  const from = new URLSearchParams(search).get("from")
  if (!from || !isClientRoute(from)) {
    return { href: "/", label: "dashboard" }
  }
  return { href: from, label: backLabel(from) }
}

// backLabel names the destination of a back-link so the control reads naturally
// ("← idea") rather than showing a bare path.
function backLabel(href: string): string {
  if (href === "/capture") return "capture"
  if (href === "/sessions") return "sessions"
  if (href === "/factory") return "factory"
  switch (href.split("/")[1]) {
    case "idea":
      return "idea"
    case "task":
      return "task"
    case "memory":
      return "memory"
    case "doc":
      return "document"
    default:
      return "back"
  }
}

// usePath tracks the browser pathname across client-side navigations.
export function usePath(): string {
  const [path, setPath] = useState(() => window.location.pathname)
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname)
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [])
  return path
}

// navigate pushes a client-side route without a full page reload.
export function navigate(to: string): void {
  if (to === window.location.pathname) return
  window.history.pushState(null, "", to)
  window.dispatchEvent(new PopStateEvent("popstate"))
}
