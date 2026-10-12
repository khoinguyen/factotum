import { expect, test } from "vitest"

import { backTarget, withFrom } from "@/lib/router"

test("withFrom appends the return target as a from query param", () => {
  expect(withFrom("/doc/art-1", "/idea/t-idea")).toBe(
    "/doc/art-1?from=%2Fidea%2Ft-idea",
  )
})

test("withFrom adds to an existing query with an ampersand", () => {
  expect(withFrom("/doc/art-1?x=1", "/task/t-1")).toBe(
    "/doc/art-1?x=1&from=%2Ftask%2Ft-1",
  )
})

// The back-link names where the page was opened from: an idea capture, a task,
// a memory, or a document. Unknown targets fall back to the dashboard.
const targets = [
  { search: "?from=%2Fidea%2Ft-idea", href: "/idea/t-idea", label: "idea" },
  { search: "?from=%2Ftask%2Ft-1", href: "/task/t-1", label: "task" },
  { search: "?from=%2Fmemory%2Fart-1", href: "/memory/art-1", label: "memory" },
  { search: "?from=%2Fdoc%2Fart-1", href: "/doc/art-1", label: "document" },
  { search: "", href: "/", label: "dashboard" },
]

for (const { search, href, label } of targets) {
  test(`backTarget resolves ${search || "(none)"} to ${label}`, () => {
    expect(backTarget(search)).toEqual({ href, label })
  })
}

// A from value the client router does not own (an external URL, or a
// protocol-relative path) must not become the back-link: fall back to the
// dashboard so a crafted link cannot send the user off-site.
test("backTarget falls back to the dashboard for a foreign from", () => {
  expect(backTarget("?from=https%3A%2F%2Fevil.example")).toEqual({
    href: "/",
    label: "dashboard",
  })
  expect(backTarget("?from=%2F%2Fevil.example")).toEqual({
    href: "/",
    label: "dashboard",
  })
})
