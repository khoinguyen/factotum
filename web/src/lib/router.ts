import { useEffect, useState } from "react"

export type Route =
  | { kind: "dashboard" }
  | { kind: "idea"; id: string }
  | { kind: "task"; id: string }
  | { kind: "memory"; id: string }
  | { kind: "doc"; id: string }
  | { kind: "not-found"; path: string }

// resolveRoute maps the current path to a read-side route. The server serves the
// app shell for every one of them; only the client knows how to render each.
export function resolveRoute(path: string): Route {
  const trimmed = path.replace(/\/+$/, "")
  if (trimmed === "" || trimmed === "/") {
    return { kind: "dashboard" }
  }
  const parts = trimmed.split("/").filter(Boolean)
  if (parts.length === 2) {
    const [kind, id] = parts
    if (kind === "idea" || kind === "task" || kind === "memory" || kind === "doc") {
      return { kind, id }
    }
  }
  return { kind: "not-found", path }
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
