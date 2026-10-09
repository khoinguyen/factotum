import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import { PageShell } from "@/components/page-shell"
import { ThemeToggle } from "@/components/theme-toggle"
import { themeStorageKey } from "@/lib/theme"

type Listener = () => void

// jsdom has no matchMedia, so each test installs this fake and moves the OS
// preference with setMatches to exercise the "System" branch.
class FakeMediaQuery {
  matches: boolean
  media = "(prefers-color-scheme: dark)"
  private listeners = new Set<Listener>()

  constructor(matches: boolean) {
    this.matches = matches
  }

  addEventListener(_type: string, listener: Listener) {
    this.listeners.add(listener)
  }

  removeEventListener(_type: string, listener: Listener) {
    this.listeners.delete(listener)
  }

  setMatches(next: boolean) {
    this.matches = next
    for (const listener of this.listeners) listener()
  }
}

function stubOs(dark: boolean) {
  const query = new FakeMediaQuery(dark)
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => query),
  )
  return query
}

const isDark = () => document.documentElement.classList.contains("dark")

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  window.localStorage.clear()
  document.documentElement.classList.remove("dark")
})

// Default: no stored choice, so the app follows the OS preference. A dark OS
// must paint the dark token set via .dark on <html>.
test("defaults to the OS dark preference", () => {
  stubOs(true)
  render(<ThemeToggle />)

  expect(isDark()).toBe(true)
  expect(screen.getByRole("button", { name: "System" }).getAttribute("aria-pressed")).toBe("true")
})

test("defaults to light when the OS prefers light", () => {
  stubOs(false)
  render(<ThemeToggle />)

  expect(isDark()).toBe(false)
  expect(screen.getByRole("button", { name: "System" }).getAttribute("aria-pressed")).toBe("true")
})

test("choosing Dark applies .dark and persists it", () => {
  stubOs(false)
  render(<ThemeToggle />)

  fireEvent.click(screen.getByRole("button", { name: "Dark" }))

  expect(isDark()).toBe(true)
  expect(window.localStorage.getItem(themeStorageKey)).toBe("dark")
  expect(screen.getByRole("button", { name: "Dark" }).getAttribute("aria-pressed")).toBe("true")
})

test("choosing Light removes .dark and persists it", () => {
  stubOs(true)
  render(<ThemeToggle />)
  expect(isDark()).toBe(true)

  fireEvent.click(screen.getByRole("button", { name: "Light" }))

  expect(isDark()).toBe(false)
  expect(window.localStorage.getItem(themeStorageKey)).toBe("light")
})

// System is live: while selected, an OS preference change re-themes the app.
test("System follows the OS preference as it changes", () => {
  const os = stubOs(false)
  render(<ThemeToggle />)
  expect(isDark()).toBe(false)

  act(() => os.setMatches(true))
  expect(isDark()).toBe(true)

  act(() => os.setMatches(false))
  expect(isDark()).toBe(false)
})

// The shell is where every page picks the control up, so it must render one.
test("the page shell exposes the theme toggle", () => {
  stubOs(false)
  render(
    <PageShell title="Dashboard">
      <p>body</p>
    </PageShell>,
  )

  expect(screen.getByRole("group", { name: "Theme" })).toBeTruthy()
})

// An explicit choice is remembered and wins over the OS on the next load.
test("a stored choice survives a remount and overrides the OS", () => {
  stubOs(false)
  window.localStorage.setItem(themeStorageKey, "dark")

  render(<ThemeToggle />)

  expect(isDark()).toBe(true)
  expect(screen.getByRole("button", { name: "Dark" }).getAttribute("aria-pressed")).toBe("true")
})
