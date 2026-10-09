import { afterEach, expect, test } from "vitest"

import {
  applyTheme,
  initTheme,
  loadTheme,
  resolveTheme,
  saveTheme,
  themeStorageKey,
  type Theme,
} from "@/lib/theme"

afterEach(() => {
  window.localStorage.clear()
  document.documentElement.classList.remove("dark")
})

test("resolveTheme(system) follows the OS preference", () => {
  expect(resolveTheme("system", true)).toBe("dark")
  expect(resolveTheme("system", false)).toBe("light")
})

test("resolveTheme passes an explicit choice through", () => {
  expect(resolveTheme("dark", false)).toBe("dark")
  expect(resolveTheme("light", true)).toBe("light")
})

test("loadTheme defaults to system when nothing is stored", () => {
  expect(loadTheme()).toBe("system")
})

test("loadTheme reads a stored choice", () => {
  window.localStorage.setItem(themeStorageKey, "dark")
  expect(loadTheme()).toBe("dark")
})

test("loadTheme falls back to system for an unrecognized value", () => {
  window.localStorage.setItem(themeStorageKey, "sepia")
  expect(loadTheme()).toBe("system")
})

test("saveTheme persists the choice", () => {
  saveTheme("light")
  expect(window.localStorage.getItem(themeStorageKey)).toBe("light")
})

test("applyTheme toggles the dark class on the root", () => {
  applyTheme("dark")
  expect(document.documentElement.classList.contains("dark")).toBe(true)
  applyTheme("light")
  expect(document.documentElement.classList.contains("dark")).toBe(false)
})

test("initTheme applies the stored theme before React renders", () => {
  window.localStorage.setItem(themeStorageKey, "dark")
  expect(initTheme()).toBe("dark")
  expect(document.documentElement.classList.contains("dark")).toBe(true)
})

// The pure entry points are the contract; a typo here silently unthemes the app.
test("every Theme value round-trips through storage", () => {
  for (const theme of ["light", "dark", "system"] as Theme[]) {
    saveTheme(theme)
    expect(loadTheme()).toBe(theme)
  }
})
